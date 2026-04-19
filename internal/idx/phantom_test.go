package idx

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPhantomSymbols_PruneOnDelete is the load-bearing regression for
// the phantom-symbols bug class: before IncrementalTick learned to
// call PatchDirtyFiles on Diff.Deleted, a deleted file's symbols
// lingered in the symbol table forever. Queries would find them,
// dispatch would try to open the nonexistent path, and edr would
// return stale references.
//
// Scenario:
//  1. Build an index over a tiny repo with one unique symbol per file.
//  2. Delete the file that declares `phantomSymbol`.
//  3. Re-stamp the git index mtime so IncrementalTick's fast path
//     doesn't early-return.
//  4. Run IncrementalTick.
//  5. Query the symbol table for `phantomSymbol`. The surviving index
//     must have zero matches. If it has any, the deletion never
//     reached the symbol table.
//
// This test fails without the IncrementalTick → PatchDirtyFiles
// wiring. Do not gut it.
func TestPhantomSymbols_PruneOnDelete(t *testing.T) {
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	if err := os.MkdirAll(edrDir, 0o755); err != nil {
		t.Fatalf("mkdir edrDir: %v", err)
	}
	// Fake a .git/index so gitIndexMtime returns something non-zero.
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write .git/index: %v", err)
	}

	// Two files, each with a unique symbol. Body is deterministic so
	// our toy extractor can find the names.
	files := map[string]string{
		"alive.go":   "package a\nfunc aliveSymbol() {}\n",
		"phantom.go": "package a\nfunc phantomSymbol() {}\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	walkFn := func(root string, fn func(string) error) error {
		return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if strings.Contains(p, ".edr") || strings.Contains(p, ".git") {
				return nil
			}
			return fn(p)
		})
	}

	// Toy extractor: find literal "func NAME(" patterns. Enough for a
	// two-file regression; real extractors live in internal/index.
	extractSymbols := func(absPath string, data []byte) []SymbolEntry {
		var out []SymbolEntry
		// Split on "func " and pull the identifier that follows.
		parts := bytes.Split(data, []byte("func "))
		for _, p := range parts[1:] {
			end := bytes.IndexByte(p, '(')
			if end < 0 {
				continue
			}
			name := string(bytes.TrimSpace(p[:end]))
			if name == "" {
				continue
			}
			out = append(out, SymbolEntry{
				Name:      name,
				Kind:      KindFunction,
				StartLine: 1,
				EndLine:   2,
				StartByte: 0,
				EndByte:   uint32(len(data)),
			})
		}
		return out
	}

	if err := BuildFullFromWalk(root, edrDir, walkFn, nil, extractSymbols); err != nil {
		t.Fatalf("BuildFullFromWalk: %v", err)
	}

	// Sanity: phantomSymbol is findable right after build.
	d := loadIndex(edrDir)
	if d == nil {
		t.Fatalf("loadIndex returned nil after build")
	}
	if got := QuerySymbolsByName(d, "phantomSymbol"); len(got) != 1 {
		t.Fatalf("pre-delete: phantomSymbol matches = %d, want 1", len(got))
	}

	// Delete the phantom file and bump the .git/index mtime so
	// IncrementalTick doesn't fast-path out.
	if err := os.Remove(filepath.Join(root, "phantom.go")); err != nil {
		t.Fatalf("remove phantom.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("xx"), 0o600); err != nil {
		t.Fatalf("bump .git/index: %v", err)
	}

	IncrementalTick(root, edrDir, walkFn)

	// The deleted file's symbols must be pruned. If they survive,
	// IncrementalTick isn't wired to PatchDirtyFiles — regression.
	d2 := loadIndex(edrDir)
	if d2 == nil {
		t.Fatalf("loadIndex returned nil after tick")
	}
	if got := QuerySymbolsByName(d2, "phantomSymbol"); len(got) != 0 {
		t.Errorf("post-delete: phantomSymbol still present (%d hits) — phantom-symbols regression", len(got))
	}
	// The surviving file's symbol must still be present.
	if got := QuerySymbolsByName(d2, "aliveSymbol"); len(got) != 1 {
		t.Errorf("post-delete: aliveSymbol matches = %d, want 1 (deletion pruned too aggressively)", len(got))
	}
	// File table must also drop the deleted file.
	for _, f := range d2.Files {
		if f.Path == "phantom.go" {
			t.Errorf("post-delete: phantom.go still in file table")
		}
	}
}
