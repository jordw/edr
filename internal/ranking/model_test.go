package ranking

import (
	"math"
	"testing"
)

func TestScoreNilWeights(t *testing.T) {
	scores := Score(nil, make([]float32, NumFeatures*3), 3)
	if scores != nil {
		t.Error("nil weights should return nil scores")
	}
}

func TestScoreZeroCandidates(t *testing.T) {
	w := &Weights{}
	initLayerNorms(w)
	scores := Score(w, nil, 0)
	if scores != nil {
		t.Error("zero candidates should return nil scores")
	}
}

func TestScoreDeterministic(t *testing.T) {
	w := randomWeights(42)
	features := make([]float32, 5*NumFeatures)
	for i := range features {
		features[i] = float32(i%7) * 0.1
	}
	s1 := Score(w, features, 5)
	s2 := Score(w, features, 5)
	for i := range s1 {
		if s1[i] != s2[i] {
			t.Errorf("score[%d]: %f != %f", i, s1[i], s2[i])
		}
	}
}

func TestScoreProducesFinite(t *testing.T) {
	w := randomWeights(123)
	features := make([]float32, 10*NumFeatures)
	for i := range features {
		features[i] = float32(i%13)*0.2 - 1.0
	}
	scores := Score(w, features, 10)
	for i, s := range scores {
		if math.IsNaN(float64(s)) || math.IsInf(float64(s), 0) {
			t.Errorf("score[%d] is not finite: %f", i, s)
		}
	}
}

func TestScoreVariesByInput(t *testing.T) {
	w := randomWeights(99)
	features := make([]float32, 3*NumFeatures)
	// Make each candidate different
	features[0*NumFeatures+FCaseExact] = 1
	features[0*NumFeatures+FIsCore] = 1
	features[1*NumFeatures+FIsPeripheral] = 1
	features[2*NumFeatures+FIsTest] = 1
	scores := Score(w, features, 3)
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

func TestWeightCount(t *testing.T) {
	want := NumFeatures*Dim + Dim // proj
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
	if f[FIsPeripheral] != 1 {
		t.Error("expected IsPeripheral=1 for drivers/")
	}
	if f[FExtC] != 1 {
		t.Error("expected ExtC=1 for .c file")
	}
	if f[FLogSpan] <= 0 {
		t.Error("expected positive LogSpan")
	}
}

func TestFeatureExtractionCore(t *testing.T) {
	f := ExtractFeatures("sched_tick", CandidateFeatures{
		Name:      "sched_tick",
		Type:      "function",
		File:      "kernel/sched/core.c",
		StartLine: 5546,
		EndLine:   5594,
	})
	if f[FIsCore] != 1 {
		t.Error("expected IsCore=1 for kernel/")
	}
	if f[FIsPeripheral] != 0 {
		t.Error("expected IsPeripheral=0 for kernel/")
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

// randomWeights creates deterministic pseudo-random weights for testing.
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
		// LayerNorm: gamma=1, beta=0 (identity init)
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

// initLayerNorms sets layer norm gamma=1 so zero weights don't produce NaN.
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
