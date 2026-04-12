package cmd

import (
	"os"
	"testing"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/idx"
)

func TestParseDebug(t *testing.T) {
	data, err := os.ReadFile("../internal/idx/importgraph.go")
	if err != nil {
		t.Fatal(err)
	}
	syms := index.Parse("internal/idx/importgraph.go", data)
	t.Logf("Parse returned %d symbols", len(syms))
	for i, s := range syms {
		if i < 5 {
			t.Logf("  %s %s lines %d-%d", s.Type, s.Name, s.StartLine, s.EndLine)
		}
	}

	// Also test the symbolExtractor conversion
	entries := make([]idx.SymbolEntry, len(syms))
	for i, s := range syms {
		entries[i] = idx.SymbolEntry{
			Name:      s.Name,
			Kind:      idx.ParseKind(s.Type),
			StartLine: s.StartLine,
			EndLine:   s.EndLine,
			StartByte: s.StartByte,
			EndByte:   s.EndByte,
		}
	}
	t.Logf("Converted to %d SymbolEntries", len(entries))
	
	// Check if BuildFullFromWalk produces symbols
	dir := t.TempDir()
	repoRoot, _ := os.Getwd()
	repoRoot += "/.."
	extractor := func(path string, data []byte) []idx.SymbolEntry {
		syms := index.Parse(path, data)
		entries := make([]idx.SymbolEntry, len(syms))
		for i, s := range syms {
			entries[i] = idx.SymbolEntry{
				Name: s.Name, Kind: idx.ParseKind(s.Type),
				StartLine: s.StartLine, EndLine: s.EndLine,
				StartByte: s.StartByte, EndByte: s.EndByte,
			}
		}
		return entries
	}
	err = idx.BuildFullFromWalk(repoRoot, dir, index.WalkRepoFiles, nil, extractor)
	if err != nil {
		t.Fatal(err)
	}
	allSyms, _ := idx.LoadAllSymbols(dir)
	t.Logf("Index produced %d symbols", len(allSyms))
}
