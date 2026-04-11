package idx

import (
	"os"
	"testing"
)

func TestImportGraphRoundTrip(t *testing.T) {
	files := []string{"a/b.c", "a/c.c", "include/d.h", "include/e.h"}
	edges := [][2]string{
		{"a/b.c", "include/d.h"},
		{"a/b.c", "include/e.h"},
		{"a/c.c", "include/d.h"},
	}
	g := BuildImportGraph(files, edges)
	if g == nil {
		t.Fatal("expected non-nil graph")
	}
	if len(g.Edges) != 3 {
		t.Errorf("expected 3 edges, got %d", len(g.Edges))
	}
	if g.Inbound("include/d.h") != 2 {
		t.Errorf("d.h inbound = %d, want 2", g.Inbound("include/d.h"))
	}
	if g.Inbound("include/e.h") != 1 {
		t.Errorf("e.h inbound = %d, want 1", g.Inbound("include/e.h"))
	}
	if g.Inbound("a/b.c") != 0 {
		t.Errorf("b.c inbound = %d, want 0", g.Inbound("a/b.c"))
	}

	// Write and read back
	dir := t.TempDir()
	if err := WriteImportGraph(dir, g); err != nil {
		t.Fatal(err)
	}
	g2 := ReadImportGraph(dir)
	if g2 == nil {
		t.Fatal("read back nil")
	}
	if len(g2.Edges) != 3 {
		t.Errorf("read back %d edges, want 3", len(g2.Edges))
	}
	if g2.Inbound("include/d.h") != 2 {
		t.Errorf("read back d.h inbound = %d, want 2", g2.Inbound("include/d.h"))
	}

	// Test Importers/Imports
	importers := g2.Importers("include/d.h")
	if len(importers) != 2 {
		t.Errorf("d.h importers = %d, want 2", len(importers))
	}
	imports := g2.Imports("a/b.c")
	if len(imports) != 2 {
		t.Errorf("b.c imports = %d, want 2", len(imports))
	}

	// Cleanup
	os.Remove(dir + "/" + ImportGraphFile)
}
