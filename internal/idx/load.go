package idx

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
)

// IndexedPaths returns the set of file paths in the index.
// Returns nil if no index exists.
func IndexedPaths(edrDir string) map[string]struct{} {
	d := loadIndex(edrDir)
	if d == nil {
		return nil
	}
	m := make(map[string]struct{}, len(d.Files))
	for _, f := range d.Files {
		m[f.Path] = struct{}{}
	}
	return m
}

// ReadHeaderBytes parses a header from an already-read byte slice.
func ReadHeaderBytes(data []byte) (*Header, error) {
	if len(data) < v2HeaderSize {
		return nil, fmt.Errorf("too small")
	}
	if data[0] != 'E' || data[1] != 'D' || data[2] != 'R' {
		return nil, fmt.Errorf("bad magic")
	}
	h := &Header{
		Version:      binary.LittleEndian.Uint32(data[8:12]),
		NumFiles:     binary.LittleEndian.Uint32(data[12:16]),
		NumTrigrams:  binary.LittleEndian.Uint32(data[16:20]),
		GitMtime:     int64(binary.LittleEndian.Uint64(data[20:28])),
		FileTableOff: binary.LittleEndian.Uint64(data[28:36]),
		PostingOff:   binary.LittleEndian.Uint64(data[36:44]),
	}
	if h.Version >= 3 && len(data) >= headerSize {
		h.NumSymbols = binary.LittleEndian.Uint32(data[44:48])
		h.SymbolOff = binary.LittleEndian.Uint64(data[48:56])
		h.NamePostOff = binary.LittleEndian.Uint64(data[56:64])
		h.NumNameKeys = binary.LittleEndian.Uint32(data[64:68])
	}
	return h, nil
}

func loadIndex(edrDir string) *Snapshot {
	data, err := os.ReadFile(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil
	}
	d, err := Unmarshal(data)
	if err != nil {
		return nil
	}
	return d
}

// loadIndexTrigrams loads only file table + trigrams + postings, skipping
// symbol parsing. ~2x faster than loadIndex on large repos.
func loadIndexTrigrams(edrDir string) *Snapshot {
	data, err := os.ReadFile(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil
	}
	d, err := UnmarshalTrigrams(data)
	if err != nil {
		return nil
	}
	return d
}
