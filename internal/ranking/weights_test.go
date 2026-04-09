package ranking

import (
	"bytes"
	"testing"
)

func TestWeightRoundTrip(t *testing.T) {
	w := randomWeights(42)

	var buf bytes.Buffer
	if err := writeWeights(&buf, w); err != nil {
		t.Fatal(err)
	}

	t.Logf("weight file size: %d bytes (%.1fK)", buf.Len(), float64(buf.Len())/1024)

	w2 := readWeights(&buf)
	if w2 == nil {
		t.Fatal("failed to read weights back")
	}

	// Spot-check a few values
	if w.ProjW[0] != w2.ProjW[0] {
		t.Errorf("ProjW[0]: %f != %f", w.ProjW[0], w2.ProjW[0])
	}
	if w.Layers[1].FFN2W[0] != w2.Layers[1].FFN2W[0] {
		t.Errorf("Layer1.FFN2W[0]: %f != %f", w.Layers[1].FFN2W[0], w2.Layers[1].FFN2W[0])
	}
	if w.ScoreB != w2.ScoreB {
		t.Errorf("ScoreB: %f != %f", w.ScoreB, w2.ScoreB)
	}
}

func TestWeightFileSize(t *testing.T) {
	w := randomWeights(1)
	var buf bytes.Buffer
	writeWeights(&buf, w)
	want := TotalWeights * 4
	if buf.Len() != want {
		t.Errorf("weight file size = %d bytes, want %d", buf.Len(), want)
	}
}

func TestLoadWeightsNonexistent(t *testing.T) {
	w := LoadWeights("/nonexistent/path")
	if w != nil {
		t.Error("should return nil for missing file")
	}
}
