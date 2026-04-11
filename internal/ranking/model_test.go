package ranking

import (
	"math"
	"strings"
	"testing"
)

// helper: make query/path arrays for n candidates
func makeStrings(query string, paths []string) ([]string, []string) {
	queries := make([]string, len(paths))
	for i := range queries {
		queries[i] = query
	}
	return queries, paths
}

func TestScoreNilWeights(t *testing.T) {
	q, p := makeStrings("test", []string{"a.go", "b.go", "c.go"})
	scores := Score(nil, make([]float32, ScalarFeatures*3), q, p, 3)
	if scores != nil {
		t.Error("nil weights should return nil scores")
	}
}

func TestScoreZeroCandidates(t *testing.T) {
	w := &Weights{}
	initLayerNorms(w)
	scores := Score(w, nil, nil, nil, 0)
	if scores != nil {
		t.Error("zero candidates should return nil scores")
	}
}

func TestScoreDeterministic(t *testing.T) {
	w := randomWeights(42)
	features := make([]float32, 5*ScalarFeatures)
	for i := range features {
		features[i] = float32(i%7) * 0.1
	}
	q, p := makeStrings("init", []string{"a/b.c", "c/d.c", "e/f.c", "g/h.c", "i/j.c"})
	s1 := Score(w, features, q, p, 5)
	s2 := Score(w, features, q, p, 5)
	for i := range s1 {
		if s1[i] != s2[i] {
			t.Errorf("score[%d]: %f != %f", i, s1[i], s2[i])
		}
	}
}

func TestScoreProducesFinite(t *testing.T) {
	w := randomWeights(123)
	features := make([]float32, 10*ScalarFeatures)
	for i := range features {
		features[i] = float32(i%13)*0.2 - 1.0
	}
	paths := make([]string, 10)
	for i := range paths {
		paths[i] = "some/path/file.go"
	}
	q, p := makeStrings("probe", paths)
	scores := Score(w, features, q, p, 10)
	for i, s := range scores {
		if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) {
			t.Errorf("score[%d] is not finite: %f", i, s)
		}
	}
}

func TestScoreVariesByInput(t *testing.T) {
	w := randomWeights(99)
	features := make([]float32, 3*ScalarFeatures)
	features[0*ScalarFeatures+FLogSpan] = 0.8
	features[1*ScalarFeatures+FIsTestPath] = 1
	features[2*ScalarFeatures+FNameIsShort] = 1
	// Different paths so char encoder produces different embeddings
	q, p := makeStrings("open", []string{
		"kernel/sched/core.c",
		"test/unit_test.go",
		"x.py",
	})
	scores := Score(w, features, q, p, 3)
	allSame := true
	for i := 1; i < len(scores); i++ {
		if scores[i] != scores[0] {
			allSame = false
			break
		}
	}
	if allSame {
		t.Error("different inputs should produce different scores")
	}
}

func TestCharEncoderSimilarity(t *testing.T) {
	w := randomWeights(77)
	// "sched" in query and "sched/core.c" in path should share trigrams
	e1 := EncodeString(&w.CharEnc, "sched")
	e2 := EncodeString(&w.CharEnc, "sched/core.c")
	e3 := EncodeString(&w.CharEnc, "totally_different")

	// Cosine similarity: e1·e2 should be higher than e1·e3
	dot12, dot13 := float32(0), float32(0)
	for d := 0; d < CharDim; d++ {
		dot12 += e1[d] * e2[d]
		dot13 += e1[d] * e3[d]
	}
	// Not guaranteed with random weights, but the shared "sch","che","hed" trigrams
	// pull from the same embedding rows. Just check they're different.
	if dot12 == dot13 {
		t.Error("expected different similarity scores for similar vs different strings")
	}
}

func TestWeightCount(t *testing.T) {
	want := NumBuckets*CharDim + InputDim*Dim + Dim // char enc + proj
	for range [NumLayers]struct{}{} {
		want += 3 * (Dim*Dim + Dim) // QKV
		want += Dim*Dim + Dim       // O
		want += 2 * Dim             // LN1
		want += Dim*FFNHidden + FFNHidden
		want += FFNHidden*Dim + Dim
		want += 2 * Dim // LN2
	}
	want += Dim + 1 // score head
	if TotalWeights != want {
		t.Errorf("TotalWeights = %d, want %d", TotalWeights, want)
	}
	t.Logf("Model: %s", NumParams())
}

func TestFeatureExtraction(t *testing.T) {
	f := ExtractFeatures("probe", CandidateFeatures{
		Name:      "probe",
		Type:      "function",
		File:      "drivers/media/dvb/mxl5xx.c",
		StartLine: 1698,
		EndLine:   1750,
	})
	if f[FCaseExact] != 1 {
		t.Error("expected CaseExact=1")
	}
	if f[FCaseMatch] != 1 {
		t.Error("expected CaseMatch=1")
	}
	if f[FTypeFunc] != 1 {
		t.Error("expected TypeFunc=1")
	}
	if f[FDepth] <= 0 {
		t.Error("expected positive Depth for drivers/media/dvb/mxl5xx.c")
	}
	if f[FLogSpan] <= 0 {
		t.Error("expected positive LogSpan")
	}
}

func TestFeatureExtractionNameInPath(t *testing.T) {
	f := ExtractFeatures("sched", CandidateFeatures{
		Name:      "sched_tick",
		Type:      "function",
		File:      "kernel/sched/core.c",
		StartLine: 5546,
		EndLine:   5594,
	})
	if f[FNameInPath] != 1 {
		t.Error("expected NameInPath=1 for 'sched' in kernel/sched/core.c")
	}
	if f[FNameHasUnderscore] != 1 {
		t.Error("expected NameHasUnderscore=1 for sched_tick")
	}
}

func TestExtractAll(t *testing.T) {
	candidates := []CandidateFeatures{
		{Name: "open", Type: "function", File: "fs/open.c", StartLine: 100, EndLine: 200},
		{Name: "open", Type: "method", File: "tools/lib/parse.py", StartLine: 10, EndLine: 20},
	}
	flat := ExtractAll("open", candidates)
	if len(flat) != 2*NumFeatures {
		t.Errorf("expected %d floats, got %d", 2*NumFeatures, len(flat))
	}
}

// --- test helpers ---

func randomWeights(seed int) *Weights {
	w := &Weights{}
	i := seed
	next := func() float32 {
		i = (i*1103515245 + 12345) & 0x7fffffff
		return float32(i)/float32(0x7fffffff)*0.2 - 0.1
	}
	fillSlice := func(s []float32) {
		for j := range s {
			s[j] = next()
		}
	}
	fillSlice(w.CharEnc.TrigramEmbed[:])
	fillSlice(w.ProjW[:])
	fillSlice(w.ProjB[:])
	for l := 0; l < NumLayers; l++ {
		lw := &w.Layers[l]
		fillSlice(lw.QW[:])
		fillSlice(lw.QB[:])
		fillSlice(lw.KW[:])
		fillSlice(lw.KB[:])
		fillSlice(lw.VW[:])
		fillSlice(lw.VB[:])
		fillSlice(lw.OW[:])
		fillSlice(lw.OB[:])
		for j := range lw.LN1G {
			lw.LN1G[j] = 1
		}
		for j := range lw.LN2G {
			lw.LN2G[j] = 1
		}
		fillSlice(lw.FFN1W[:])
		fillSlice(lw.FFN1B[:])
		fillSlice(lw.FFN2W[:])
		fillSlice(lw.FFN2B[:])
	}
	fillSlice(w.ScoreW[:])
	w.ScoreB = next()
	return w
}

func initLayerNorms(w *Weights) {
	for l := 0; l < NumLayers; l++ {
		for j := range w.Layers[l].LN1G {
			w.Layers[l].LN1G[j] = 1
		}
		for j := range w.Layers[l].LN2G {
			w.Layers[l].LN2G[j] = 1
		}
	}
}

var _ = strings.Contains // suppress unused import
