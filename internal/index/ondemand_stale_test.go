package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jordw/edr/internal/idx"
)

// buildSymbolIndexForTest writes a v3 symbol index for the files under root.
// It mirrors the setup that dispatch_index.runIndex performs in production.
func buildSymbolIndexForTest(t *testing.T, root, edrDir string) {
	t.Helper()
	symFn := func(path string, data []byte) []idx.SymbolEntry {
		syms := Parse(path, data)
		out := make([]idx.SymbolEntry, len(syms))
		for i, s := range syms {
			out[i] = idx.SymbolEntry{
				Name:      s.Name,
				Kind:      idx.ParseKind(s.Type),
				StartLine: s.StartLine,
				EndLine:   s.EndLine,
				StartByte: s.StartByte,
				EndByte:   s.EndByte,
			}
		}
		return out
	}
	walk := func(root string, fn func(string) error) error {
		return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			return fn(path)
		})
	}
	if err := idx.BuildFullFromWalk(root, edrDir, walk, nil, symFn); err != nil {
		t.Fatalf("build index: %v", err)
	}
	idx.InvalidateSymbolCache()
}

// setupStaleRepo creates a temp repo with one Go file and a built index.
func setupStaleRepo(t *testing.T, filename, body string) (*OnDemand, string) {
	t.Helper()
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	if err := os.MkdirAll(edrDir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, filename)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	buildSymbolIndexForTest(t, root, edrDir)

	o := &OnDemand{root: root, edrDir: edrDir, cache: map[string]*cachedFile{}, fileCount: -1}
	return o, path
}

func symbolNames(syms []SymbolInfo) []string {
	names := make([]string, len(syms))
	for i, s := range syms {
		names[i] = s.Name
	}
	return names
}

func containsName(syms []SymbolInfo, name string) bool {
	for _, s := range syms {
		if s.Name == name {
			return true
		}
	}
	return false
}

func TestFilteredSymbols_DetectsExternalEdit(t *testing.T) {
	original := `package main

func Alpha() {}
func Beta() {}
`
	o, path := setupStaleRepo(t, "main.go", original)

	// Sanity: index returns the original symbols.
	before, err := o.FilteredSymbols(context.Background(), "", "function", "")
	if err != nil {
		t.Fatal(err)
	}
	if !containsName(before, "Alpha") || !containsName(before, "Beta") {
		t.Fatalf("pre-edit missing Alpha/Beta: %v", symbolNames(before))
	}

	// Rewrite the file externally (no edr.MarkDirty). The filesystem-stale
	// detection must notice and re-parse, dropping Beta and surfacing Gamma.
	rewritten := `package main

func Alpha() {}
func Gamma() {}
`
	if err := os.WriteFile(path, []byte(rewritten), 0644); err != nil {
		t.Fatal(err)
	}
	// Bump mtime explicitly — some filesystems only resolve to seconds,
	// so identical-second writes would not be detected via mtime alone.
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	after, err := o.FilteredSymbols(context.Background(), "", "function", "")
	if err != nil {
		t.Fatal(err)
	}
	if containsName(after, "Beta") {
		t.Errorf("Beta should be gone after external edit: %v", symbolNames(after))
	}
	if !containsName(after, "Gamma") {
		t.Errorf("Gamma should be picked up after external edit: %v", symbolNames(after))
	}
	if !containsName(after, "Alpha") {
		t.Errorf("Alpha should still be present: %v", symbolNames(after))
	}
}

func TestFilteredSymbols_DetectsDeletedFile(t *testing.T) {
	o, path := setupStaleRepo(t, "main.go", `package main

func Ghost() {}
`)

	// Before deletion: Ghost is found.
	before, _ := o.FilteredSymbols(context.Background(), "", "function", "")
	if !containsName(before, "Ghost") {
		t.Fatalf("pre-delete missing Ghost: %v", symbolNames(before))
	}

	// Remove the file. Index still references it; staleness check must
	// mark it dirty so the dirty-patch path suppresses its symbols.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	after, _ := o.FilteredSymbols(context.Background(), "", "function", "")
	if containsName(after, "Ghost") {
		t.Errorf("Ghost should be gone after file deletion: %v", symbolNames(after))
	}
}

func TestFilteredSymbols_CleanIndexUsesFastPath(t *testing.T) {
	// Sanity check: when nothing changed, the fast path still returns the
	// original symbols (regression guard for the new staleness check).
	o, _ := setupStaleRepo(t, "main.go", `package main

func Keep() {}
`)
	syms, err := o.FilteredSymbols(context.Background(), "", "function", "")
	if err != nil {
		t.Fatal(err)
	}
	if !containsName(syms, "Keep") {
		t.Errorf("Keep missing on clean index: %v", symbolNames(syms))
	}
}

func TestDetectFilesystemStale_ScopesToDir(t *testing.T) {
	root := t.TempDir()
	// Stat a real file so FileEntry values reflect actual mtime/size.
	in := filepath.Join(root, "in", "a.go")
	out := filepath.Join(root, "out", "b.go")
	if err := os.MkdirAll(filepath.Dir(in), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(out), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, []byte("y"), 0644); err != nil {
		t.Fatal(err)
	}
	// Pretend the index captured both files at (wrong) size 99 —
	// both should look stale to an unscoped check.
	files := []idx.FileEntry{
		{Path: "in/a.go", Mtime: 0, Size: 99},
		{Path: "out/b.go", Mtime: 0, Size: 99},
	}

	all := detectFilesystemStale(root, files, "")
	if len(all) != 2 {
		t.Errorf("unscoped stale = %v, want 2 entries", all)
	}

	scoped := detectFilesystemStale(root, files, filepath.Join(root, "in"))
	if len(scoped) != 1 || scoped[0] != "in/a.go" {
		t.Errorf("scoped stale = %v, want [in/a.go]", scoped)
	}
}

func TestMergeDirtyPaths_Deduplicates(t *testing.T) {
	merged := mergeDirtyPaths([]string{"a", "b"}, []string{"b", "c"})
	if len(merged) != 3 {
		t.Fatalf("merged = %v, want 3 unique entries", merged)
	}
	seen := map[string]bool{}
	for _, f := range merged {
		if seen[f] {
			t.Errorf("duplicate in merged: %s (%v)", f, merged)
		}
		seen[f] = true
	}
	for _, want := range []string{"a", "b", "c"} {
		if !seen[want] {
			t.Errorf("missing %s in merged %v", want, merged)
		}
	}
}
