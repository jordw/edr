package idx

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testWalkFn returns a walkFn that skips .edr/.git by path segment.
// Matches the phantom test's walker — kept as a helper so the modify/
// add tests can share it without coupling to TestPhantomSymbols_* internals.
func testWalkFn() func(string, func(string) error) error {
	return func(rootArg string, fn func(string) error) error {
		return filepath.Walk(rootArg, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, relErr := filepath.Rel(rootArg, p)
			if relErr != nil {
				return nil
			}
			for _, seg := range strings.Split(rel, string(filepath.Separator)) {
				if seg == ".edr" || seg == ".git" {
					return nil
				}
			}
			return fn(p)
		})
	}
}

// testExtractSymbols mirrors the phantom test's toy "func NAME(" parser.
// Good enough for the modify/add regressions; the real extractor lives
// in internal/dispatch.
func testExtractSymbols(_ string, data []byte) []SymbolEntry {
	var out []SymbolEntry
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

// newPatchTestRepo creates a fresh tempdir, stubs .git/index so
// gitIndexMtime returns non-zero, writes the given files, then
// builds a full index over them with testExtractSymbols.
func newPatchTestRepo(t *testing.T, files map[string]string) (root, edrDir string) {
	t.Helper()
	root = t.TempDir()
	edrDir = filepath.Join(root, ".edr")
	if err := os.MkdirAll(edrDir, 0o755); err != nil {
		t.Fatalf("mkdir edrDir: %v", err)
	}
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write .git/index: %v", err)
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	if err := BuildFullFromWalk(root, edrDir, testWalkFn(), nil, testExtractSymbols); err != nil {
		t.Fatalf("BuildFullFromWalk: %v", err)
	}
	return root, edrDir
}

// bumpGitIndex writes new bytes to .git/index so IncrementalTick's
// fast path (Staleness check) trips.
func bumpGitIndex(t *testing.T, root string, payload string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, ".git", "index"), []byte(payload), 0o600); err != nil {
		t.Fatalf("bump .git/index: %v", err)
	}
}

// TestPatchSymbols_ModifyReextracts covers the modify path: a file's
// content is rewritten so its previous symbol is gone and a new symbol
// takes its place. Without the extractor wiring, the old symbol would
// survive (or both would disappear), leaving the index progressively
// sparser. With the extractor, `bar` shows up and `foo` is gone.
func TestPatchSymbols_ModifyReextracts(t *testing.T) {
	root, edrDir := newPatchTestRepo(t, map[string]string{
		"a.go": "package a\nfunc foo() {}\n",
	})

	// Sanity: foo is findable right after build.
	d := loadIndex(edrDir)
	if d == nil {
		t.Fatalf("loadIndex returned nil after build")
	}
	if got := QuerySymbolsByName(d, "foo"); len(got) != 1 {
		t.Fatalf("pre-modify: foo matches = %d, want 1", len(got))
	}

	// Rewrite the file: foo is gone, bar takes its place.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc bar() {}\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.go: %v", err)
	}
	bumpGitIndex(t, root, "xx")

	IncrementalTick(root, edrDir, testWalkFn(), testExtractSymbols)

	d2 := loadIndex(edrDir)
	if d2 == nil {
		t.Fatalf("loadIndex returned nil after tick")
	}
	if got := QuerySymbolsByName(d2, "foo"); len(got) != 0 {
		t.Errorf("post-modify: foo still present (%d hits) — stale symbol not pruned", len(got))
	}
	if got := QuerySymbolsByName(d2, "bar"); len(got) != 1 {
		t.Errorf("post-modify: bar matches = %d, want 1 — new symbol not indexed", len(got))
	}
}

// TestPatchSymbols_AddReextracts covers the added-file path: a brand
// new file is created between ticks. Without including changes.New in
// the dirty set and running the extractor, `baz` would not be queryable
// via the symbol table until the next full rebuild.
func TestPatchSymbols_AddReextracts(t *testing.T) {
	root, edrDir := newPatchTestRepo(t, map[string]string{
		"existing.go": "package a\nfunc existingFn() {}\n",
	})

	// Add a brand new file with a unique symbol.
	if err := os.WriteFile(filepath.Join(root, "added.go"), []byte("package a\nfunc baz() {}\n"), 0o644); err != nil {
		t.Fatalf("write added.go: %v", err)
	}
	bumpGitIndex(t, root, "xx")

	IncrementalTick(root, edrDir, testWalkFn(), testExtractSymbols)

	d := loadIndex(edrDir)
	if d == nil {
		t.Fatalf("loadIndex returned nil after tick")
	}
	if got := QuerySymbolsByName(d, "baz"); len(got) != 1 {
		t.Errorf("post-add: baz matches = %d, want 1 — added-file symbol not indexed", len(got))
	}
	// The pre-existing symbol should still be there.
	if got := QuerySymbolsByName(d, "existingFn"); len(got) != 1 {
		t.Errorf("post-add: existingFn matches = %d, want 1 — tick clobbered existing symbols", len(got))
	}
	// The added file must also show up in the file table so dispatch
	// can resolve its FileID when opening the symbol.
	var sawAdded bool
	for _, f := range d.Files {
		if f.Path == "added.go" {
			sawAdded = true
			break
		}
	}
	if !sawAdded {
		t.Errorf("post-add: added.go missing from file table")
	}
}

// TestPatchSymbols_NilExtractorPreservesDropBehavior guards the
// backward-compatibility path. Non-dispatch callers (bench, tests)
// pass nil. They should see the legacy behavior: modified-file symbols
// drop without replacement. If this ever starts re-extracting, those
// callers would get surprise parsing work on every tick.
func TestPatchSymbols_NilExtractorPreservesDropBehavior(t *testing.T) {
	root, edrDir := newPatchTestRepo(t, map[string]string{
		"a.go": "package a\nfunc foo() {}\n",
	})

	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nfunc bar() {}\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.go: %v", err)
	}
	bumpGitIndex(t, root, "xx")

	IncrementalTick(root, edrDir, testWalkFn(), nil)

	d := loadIndex(edrDir)
	if d == nil {
		t.Fatalf("loadIndex returned nil after tick")
	}
	if got := QuerySymbolsByName(d, "foo"); len(got) != 0 {
		t.Errorf("nil-extractor: foo still present (%d hits) — dirty symbols not dropped", len(got))
	}
	if got := QuerySymbolsByName(d, "bar"); len(got) != 0 {
		t.Errorf("nil-extractor: bar present (%d hits) — nil extractor should NOT re-extract", len(got))
	}
}
