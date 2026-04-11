// Package ranking implements a tiny transformer model for symbol ranking.
//
// Architecture: character-level embeddings of query + path, combined with
// scalar features, projected to d=16, then self-attention across candidates.
//
// The char encoder captures string similarity (shared trigram embeddings)
// so "sched_tick" and "sched/core.c" produce similar vectors automatically.
//
// ~8k weights, d=16, 2 heads, 2 layers. Inference is pure Go, no dependencies.
package ranking

import "math"

// Model dimensions.
const (
	// ScalarFeatures is the number of non-string features per candidate.
	// These are the structural/relative features from features.go.
	ScalarFeatures = 30

	// InputDim is the total input to the projection layer:
	// EncoderOut (char embeddings of query+path) + ScalarFeatures
	InputDim = EncoderOut + ScalarFeatures // 16 + 30 = 46

	Dim       = 16 // embedding dimension
	NumHeads  = 2  // attention heads
	HeadDim   = Dim / NumHeads
	FFNHidden = 48 // feed-forward hidden size
	NumLayers = 2
)

// For backward compatibility with code that references NumFeatures
const NumFeatures = ScalarFeatures

// Weights holds all model parameters.
type Weights struct {
	// Character encoder (shared trigram embeddings)
	CharEnc CharEncoderWeights

	// Input projection: InputDim → Dim
	ProjW [InputDim * Dim]float32
	ProjB [Dim]float32

	// Transformer layers
	Layers [NumLayers]LayerWeights

	// Score head: Dim → 1
	ScoreW [Dim]float32
	ScoreB float32
}

// LayerWeights holds parameters for one transformer layer.
type LayerWeights struct {
	QW, KW, VW [Dim * Dim]float32
	QB, KB, VB [Dim]float32
	OW         [Dim * Dim]float32
	OB         [Dim]float32
	LN1G, LN1B [Dim]float32
	FFN1W      [Dim * FFNHidden]float32
	FFN1B      [FFNHidden]float32
	FFN2W      [FFNHidden * Dim]float32
	FFN2B      [Dim]float32
	LN2G, LN2B [Dim]float32
}

// Score runs the model forward pass.
// scalarFeatures is [N * ScalarFeatures] (from ExtractAll).
// queries[i] is the query string, paths[i] is the candidate path.
// For bare focus, all queries are the same string.
// Returns N scores (higher = better).
func Score(w *Weights, scalarFeatures []float32, queries, paths []string, n int) []float32 {
	if w == nil || n == 0 {
		return nil
	}

	// Build input: char embeddings + scalar features per candidate
	input := make([]float32, n*InputDim)
	for i := 0; i < n; i++ {
		q := ""
		if i < len(queries) {
			q = queries[i]
		}
		p := ""
		if i < len(paths) {
			p = paths[i]
		}
		charEmb := EncodePair(&w.CharEnc, q, p)
		off := i * InputDim
		copy(input[off:off+EncoderOut], charEmb[:])
		if len(scalarFeatures) >= (i+1)*ScalarFeatures {
			copy(input[off+EncoderOut:off+InputDim], scalarFeatures[i*ScalarFeatures:(i+1)*ScalarFeatures])
		}
	}

	// Project: [N, InputDim] → [N, Dim]
	x := make([]float32, n*Dim)
	matMulBias(x, input, w.ProjW[:], w.ProjB[:], n, InputDim, Dim)

	// Transformer layers
	for l := 0; l < NumLayers; l++ {
		x = transformerLayer(x, n, &w.Layers[l])
	}

	// Score head: [N, Dim] → [N]
	scores := make([]float32, n)
	for i := 0; i < n; i++ {
		s := w.ScoreB
		for d := 0; d < Dim; d++ {
			s += x[i*Dim+d] * w.ScoreW[d]
		}
		scores[i] = s
	}
	return scores
}

func transformerLayer(x []float32, n int, lw *LayerWeights) []float32 {
	attnOut := selfAttention(x, n, lw)
	res1 := make([]float32, n*Dim)
	for i := range res1 {
		res1[i] = x[i] + attnOut[i]
	}
	layerNorm(res1, lw.LN1G[:], lw.LN1B[:], n, Dim)

	hidden := make([]float32, n*FFNHidden)
	matMulBias(hidden, res1, lw.FFN1W[:], lw.FFN1B[:], n, Dim, FFNHidden)
	gelu(hidden)

	ffnOut := make([]float32, n*Dim)
	matMulBias(ffnOut, hidden, lw.FFN2W[:], lw.FFN2B[:], n, FFNHidden, Dim)

	for i := range ffnOut {
		ffnOut[i] += res1[i]
	}
	layerNorm(ffnOut, lw.LN2G[:], lw.LN2B[:], n, Dim)
	return ffnOut
}

func selfAttention(x []float32, n int, lw *LayerWeights) []float32 {
	q := make([]float32, n*Dim)
	k := make([]float32, n*Dim)
	v := make([]float32, n*Dim)
	matMulBias(q, x, lw.QW[:], lw.QB[:], n, Dim, Dim)
	matMulBias(k, x, lw.KW[:], lw.KB[:], n, Dim, Dim)
	matMulBias(v, x, lw.VW[:], lw.VB[:], n, Dim, Dim)

	scale := float32(1.0 / math.Sqrt(float64(HeadDim)))
	out := make([]float32, n*Dim)

	for h := 0; h < NumHeads; h++ {
		off := h * HeadDim
		attn := make([]float32, n*n)
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				dot := float32(0)
				for d := 0; d < HeadDim; d++ {
					dot += q[i*Dim+off+d] * k[j*Dim+off+d]
				}
				attn[i*n+j] = dot * scale
			}
			softmaxRow(attn[i*n : i*n+n])
		}
		for i := 0; i < n; i++ {
			for d := 0; d < HeadDim; d++ {
				sum := float32(0)
				for j := 0; j < n; j++ {
					sum += attn[i*n+j] * v[j*Dim+off+d]
				}
				out[i*Dim+off+d] = sum
			}
		}
	}

	projected := make([]float32, n*Dim)
	matMulBias(projected, out, lw.OW[:], lw.OB[:], n, Dim, Dim)
	return projected
}

// --- Linear algebra primitives ---

func matMulBias(out, in, w, bias []float32, rows, inDim, outDim int) {
	for i := 0; i < rows; i++ {
		for j := 0; j < outDim; j++ {
			sum := bias[j]
			for k := 0; k < inDim; k++ {
				sum += in[i*inDim+k] * w[k*outDim+j]
			}
			out[i*outDim+j] = sum
		}
	}
}

func softmaxRow(x []float32) {
	max := x[0]
	for _, v := range x[1:] {
		if v > max {
			max = v
		}
	}
	sum := float32(0)
	for i := range x {
		x[i] = float32(math.Exp(float64(x[i] - max)))
		sum += x[i]
	}
	if sum > 0 {
		for i := range x {
			x[i] /= sum
		}
	}
}

func layerNorm(x, gamma, beta []float32, rows, dim int) {
	for i := 0; i < rows; i++ {
		row := x[i*dim : i*dim+dim]
		mean := float32(0)
		for _, v := range row {
			mean += v
		}
		mean /= float32(dim)
		variance := float32(0)
		for _, v := range row {
			d := v - mean
			variance += d * d
		}
		variance /= float32(dim)
		invStd := float32(1.0 / math.Sqrt(float64(variance)+1e-5))
		for j := range row {
			row[j] = (row[j]-mean)*invStd*gamma[j] + beta[j]
		}
	}
}

func gelu(x []float32) {
	for i, v := range x {
		x[i] = float32(0.5) * v * (1 + float32(math.Tanh(float64(
			0.7978845608*float32(v+0.044715*v*v*v)))))
	}
}
