package idx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSymbolIndexRoundTrip(t *testing.T) {
	// Build a minimal index with symbols
	d := &IndexData{
		Header: Header{
			NumFiles: 1, NumTrigrams: 0, GitMtime: 12345,
		},
		Files: []FileEntry{{Path: "main.go", Mtime: 100, Size: 50}},
		Symbols: []SymbolEntry{
			{FileID: 0, Name: "hello", Kind: KindFunction, StartLine: 3, EndLine: 5, StartByte: 15, EndByte: 45},
			{FileID: 0, Name: "Config", Kind: KindStruct, StartLine: 7, EndLine: 10, StartByte: 47, EndByte: 80},
			{FileID: 0, Name: "hello", Kind: KindFunction, StartLine: 20, EndLine: 22, StartByte: 100, EndByte: 130},
		},
	}

	// Build name postings
	npData, npEntries := BuildNamePostings(d.Symbols)
	d.NamePostings = npData
	d.NamePosts = npEntries
	d.Header.NumSymbols = uint32(len(d.Symbols))
	d.Header.NumNameKeys = uint32(len(npEntries))

	// Marshal
	data := d.Marshal()

	// Write to temp dir
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MainFile), data, 0600); err != nil {
		t.Fatal(err)
	}

	// Read header
	h, err := ReadHeader(dir)
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 3 {
		t.Errorf("version = %d, want 3", h.Version)
	}
	if h.NumSymbols != 3 {
		t.Errorf("numSymbols = %d, want 3", h.NumSymbols)
	}

	// Unmarshal
	d2, err := Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(d2.Symbols) != 3 {
		t.Fatalf("symbols = %d, want 3", len(d2.Symbols))
	}
	if d2.Symbols[0].Name != "hello" || d2.Symbols[0].Kind != KindFunction {
		t.Errorf("symbol 0 = %v", d2.Symbols[0])
	}
	if d2.Symbols[1].Name != "Config" || d2.Symbols[1].Kind != KindStruct {
		t.Errorf("symbol 1 = %v", d2.Symbols[1])
	}

	// Query by name
	results := QuerySymbolsByName(d2, "hello")
	if len(results) != 2 {
		t.Fatalf("query 'hello' = %d results, want 2", len(results))
	}
	results = QuerySymbolsByName(d2, "Config")
	if len(results) != 1 {
		t.Fatalf("query 'Config' = %d results, want 1", len(results))
	}
	results = QuerySymbolsByName(d2, "nonexistent")
	if len(results) != 0 {
		t.Fatalf("query 'nonexistent' = %d results, want 0", len(results))
	}
}
