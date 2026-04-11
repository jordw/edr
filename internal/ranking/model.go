// Package ranking implements a tiny transformer model for symbol ranking.
//
// Architecture: self-attention over candidate symbols, allowing the model
// to reason about candidates relative to each other (e.g., "most candidates
// are from drivers/, so drivers/ is normal for this query").
//
// ~6k weights, d=16, 2 heads, 2 layers. Inference is pure Go, no dependencies.
package ranking

import "math"

// Model dimensions.
const (
	NumFeatures = 40 // input feature vector size
	Dim         = 16 // embedding dimension
	NumHeads    = 2  // attention heads
	HeadDim     = Dim / NumHeads
	FFNHidden   = 48 // feed-forward hidden size
	NumLayers   = 2
)

// Weights holds all model parameters. Loaded from embedded binary data
// or left nil to fall back to heuristic scoring.
type Weights struct {
	// Feature projection: NumFeatures → Dim
	ProjW [NumFeatures * Dim]float32
	ProjB [Dim]float32

	// Transformer layers
	Layers [NumLayers]LayerWeights

	// Score head: Dim → 1
	ScoreW [Dim]float32
	ScoreB float32
}

// LayerWeights holds parameters for one transformer layer.
type LayerWeights struct {
	// Self-attention: Q, K, V projections
	QW, KW, VW [Dim * Dim]float32
	QB, KB, VB [Dim]float32
	// Output projection
	OW [Dim * Dim]float32
	OB [Dim]float32
	// Layer norm 1 (post-attention)
	LN1G, LN1B [Dim]float32
	// Feed-forward network
	FFN1W [Dim * FFNHidden]float32
	FFN1B [FFNHidden]float32
	FFN2W [FFNHidden * Dim]float32
	FFN2B [Dim]float32
	// Layer norm 2 (post-FFN)
	LN2G, LN2B [Dim]float32
}

// Score runs the transformer forward pass and returns a score per candidate.
// features is [N][NumFeatures]float32, flattened row-major.
// Returns N scores (higher = better candidate).
func Score(w *Weights, features []float32, n int) []float32 {
	if w == nil || n == 0 {
		return nil
	}

	// Project features: [N, NumFeatures] → [N, Dim]
	x := make([]float32, n*Dim)
	matMulBias(x, features, w.ProjW[:], w.ProjB[:], n, NumFeatures, Dim)

	// Transformer layers
	for l := 0; l < NumLayers; l++ {
		x = transformerLayer(x, n, &w.Layers[l])
	}

	// Score head: [N, Dim] → [N, 1]
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
	// Self-attention
	attnOut := selfAttention(x, n, lw)

	// Residual + LayerNorm
	res1 := make([]float32, n*Dim)
	for i := range res1 {
		res1[i] = x[i] + attnOut[i]
	}
	layerNorm(res1, lw.LN1G[:], lw.LN1B[:], n, Dim)

	// Feed-forward: Dim → FFNHidden (GELU) → Dim
	hidden := make([]float32, n*FFNHidden)
	matMulBias(hidden, res1, lw.FFN1W[:], lw.FFN1B[:], n, Dim, FFNHidden)
	gelu(hidden)

	ffnOut := make([]float32, n*Dim)
	matMulBias(ffnOut, hidden, lw.FFN2W[:], lw.FFN2B[:], n, FFNHidden, Dim)

	// Residual + LayerNorm
	for i := range ffnOut {
		ffnOut[i] += res1[i]
	}
	layerNorm(ffnOut, lw.LN2G[:], lw.LN2B[:], n, Dim)

	return ffnOut
}

func selfAttention(x []float32, n int, lw *LayerWeights) []float32 {
	// Q, K, V projections: [N, Dim] → [N, Dim]
	q := make([]float32, n*Dim)
	k := make([]float32, n*Dim)
	v := make([]float32, n*Dim)
	matMulBias(q, x, lw.QW[:], lw.QB[:], n, Dim, Dim)
	matMulBias(k, x, lw.KW[:], lw.KB[:], n, Dim, Dim)
	matMulBias(v, x, lw.VW[:], lw.VB[:], n, Dim, Dim)

	// Multi-head attention
	scale := float32(1.0 / math.Sqrt(float64(HeadDim)))
	out := make([]float32, n*Dim)

	for h := 0; h < NumHeads; h++ {
		off := h * HeadDim
		// Compute attention scores: [N, N]
		attn := make([]float32, n*n)
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				dot := float32(0)
				for d := 0; d < HeadDim; d++ {
					dot += q[i*Dim+off+d] * k[j*Dim+off+d]
				}
				attn[i*n+j] = dot * scale
			}
			// Softmax over j for row i
			softmaxRow(attn[i*n : i*n+n])
		}
		// Weighted sum of values
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

	// Output projection
	projected := make([]float32, n*Dim)
	matMulBias(projected, out, lw.OW[:], lw.OB[:], n, Dim, Dim)
	return projected
}

// --- Linear algebra primitives ---

// matMulBias computes out = in @ W^T + bias
// in: [rows, inDim], W: [inDim * outDim] (row-major: W[i*outDim+j] = W_ij), out: [rows, outDim]
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
		// Mean
		mean := float32(0)
		for _, v := range row {
			mean += v
		}
		mean /= float32(dim)
		// Variance
		variance := float32(0)
		for _, v := range row {
			d := v - mean
			variance += d * d
		}
		variance /= float32(dim)
		// Normalize
		invStd := float32(1.0 / math.Sqrt(float64(variance)+1e-5))
		for j := range row {
			row[j] = (row[j]-mean)*invStd*gamma[j] + beta[j]
		}
	}
}

func gelu(x []float32) {
	for i, v := range x {
		// Approximation: 0.5 * x * (1 + tanh(sqrt(2/pi) * (x + 0.044715 * x^3)))
		x[i] = float32(0.5) * v * (1 + float32(math.Tanh(float64(
			0.7978845608*float32(v+0.044715*v*v*v)))))
	}
}
