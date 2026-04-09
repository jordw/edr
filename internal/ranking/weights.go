package ranking

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
)

// WeightsFile is the filename for model weights in the edr directory.
const WeightsFile = "rank_model.bin"

// TotalWeights is the total number of float32 parameters in the model.
// Computed from the architecture constants.
var TotalWeights = countWeights()

func countWeights() int {
	n := NumFeatures*Dim + Dim // projection
	for range [NumLayers]struct{}{} {
		n += 3 * (Dim*Dim + Dim) // Q, K, V
		n += Dim*Dim + Dim       // O
		n += 2 * Dim             // LN1
		n += Dim*FFNHidden + FFNHidden
		n += FFNHidden*Dim + Dim
		n += 2 * Dim // LN2
	}
	n += Dim + 1 // score head
	return n
}

// LoadWeights reads model weights from the edr directory.
// Returns nil if the weights file doesn't exist (fallback to heuristic).
func LoadWeights(edrDir string) *Weights {
	if edrDir == "" {
		return nil
	}
	path := filepath.Join(edrDir, WeightsFile)
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return readWeights(f)
}

func readWeights(r io.Reader) *Weights {
	var w Weights
	read := func(dst []float32) error {
		return binary.Read(r, binary.LittleEndian, dst)
	}
	readOne := func() (float32, error) {
		var v float32
		err := binary.Read(r, binary.LittleEndian, &v)
		return v, err
	}

	if read(w.ProjW[:]) != nil {
		return nil
	}
	if read(w.ProjB[:]) != nil {
		return nil
	}
	for l := 0; l < NumLayers; l++ {
		lw := &w.Layers[l]
		for _, dst := range [][]float32{
			lw.QW[:], lw.QB[:], lw.KW[:], lw.KB[:], lw.VW[:], lw.VB[:],
			lw.OW[:], lw.OB[:],
			lw.LN1G[:], lw.LN1B[:],
			lw.FFN1W[:], lw.FFN1B[:], lw.FFN2W[:], lw.FFN2B[:],
			lw.LN2G[:], lw.LN2B[:],
		} {
			if read(dst) != nil {
				return nil
			}
		}
	}
	if read(w.ScoreW[:]) != nil {
		return nil
	}
	v, err := readOne()
	if err != nil {
		return nil
	}
	w.ScoreB = v

	// Validate: no NaN/Inf
	if !validateWeights(&w) {
		return nil
	}
	return &w
}

// SaveWeights writes model weights to the edr directory.
func SaveWeights(edrDir string, w *Weights) error {
	path := filepath.Join(edrDir, WeightsFile)
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeWeights(f, w)
}

func writeWeights(wr io.Writer, w *Weights) error {
	write := func(src []float32) error {
		return binary.Write(wr, binary.LittleEndian, src)
	}
	if err := write(w.ProjW[:]); err != nil {
		return err
	}
	if err := write(w.ProjB[:]); err != nil {
		return err
	}
	for l := 0; l < NumLayers; l++ {
		lw := &w.Layers[l]
		for _, src := range [][]float32{
			lw.QW[:], lw.QB[:], lw.KW[:], lw.KB[:], lw.VW[:], lw.VB[:],
			lw.OW[:], lw.OB[:],
			lw.LN1G[:], lw.LN1B[:],
			lw.FFN1W[:], lw.FFN1B[:], lw.FFN2W[:], lw.FFN2B[:],
			lw.LN2G[:], lw.LN2B[:],
		} {
			if err := write(src); err != nil {
				return err
			}
		}
	}
	if err := write(w.ScoreW[:]); err != nil {
		return err
	}
	return binary.Write(wr, binary.LittleEndian, w.ScoreB)
}

func validateWeights(w *Weights) bool {
	check := func(v float32) bool {
		return !math.IsNaN(float64(v)) && !math.IsInf(float64(v), 0)
	}
	checkSlice := func(s []float32) bool {
		for _, v := range s {
			if !check(v) {
				return false
			}
		}
		return true
	}
	if !checkSlice(w.ProjW[:]) || !checkSlice(w.ProjB[:]) {
		return false
	}
	for l := 0; l < NumLayers; l++ {
		lw := &w.Layers[l]
		for _, s := range [][]float32{
			lw.QW[:], lw.QB[:], lw.KW[:], lw.KB[:], lw.VW[:], lw.VB[:],
			lw.OW[:], lw.OB[:], lw.LN1G[:], lw.LN1B[:],
			lw.FFN1W[:], lw.FFN1B[:], lw.FFN2W[:], lw.FFN2B[:],
			lw.LN2G[:], lw.LN2B[:],
		} {
			if !checkSlice(s) {
				return false
			}
		}
	}
	return checkSlice(w.ScoreW[:]) && check(w.ScoreB)
}

// NumParams returns a human-readable count of model parameters.
func NumParams() string {
	return fmt.Sprintf("%d parameters (%d bytes)", TotalWeights, TotalWeights*4)
}
