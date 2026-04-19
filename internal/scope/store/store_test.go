package store

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// walkDir returns a walkFn (matching Build's signature) that walks every
// file under root and invokes fn(absPath). No gitignore; tests set up
// their own controlled trees.
func walkDir(t *testing.T) func(string, func(string) error) error {
	t.Helper()
	return func(root string, fn func(string) error) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			return fn(path)
		})
	}
}

// setupRepo writes the given files (relPath -> content) under a tmp dir
// and returns (root, edrDir).
func setupRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	if err := os.MkdirAll(edrDir, 0o755); err != nil {
		t.Fatalf("mkdir edrDir: %v", err)
	}
	for rel, content := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return root, edrDir
}

func TestBuildAndLoad(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.go":         "package a\n\nfunc Foo() {}\n",
		"sub/b.go":     "package sub\n\nfunc Bar() {}\n",
		"script.py":    "def hello():\n    pass\n",
		"ignore.txt":   "not a source file",
	})

	n, err := Build(root, edrDir, walkDir(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n != 3 {
		t.Fatalf("Build count = %d, want 3 (.go, .go, .py)", n)
	}

	if !Exists(edrDir) {
		t.Fatalf("Exists = false after Build")
	}

	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if idx == nil {
		t.Fatalf("Load returned nil index")
	}
	defer idx.Close()

	// ResultFor each file, check the file name is stamped on the result.
	for _, rel := range []string{"a.go", filepath.Join("sub", "b.go"), "script.py"} {
		r := idx.ResultFor(root, rel)
		if r == nil {
			t.Errorf("ResultFor(%s) = nil", rel)
			continue
		}
		if r.File != rel {
			t.Errorf("ResultFor(%s).File = %q, want %q", rel, r.File, rel)
		}
		if len(r.Decls) == 0 {
			t.Errorf("ResultFor(%s).Decls is empty; expected at least one decl", rel)
		}
	}

	// Unknown path returns nil (not an error).
	if r := idx.ResultFor(root, "nonexistent.go"); r != nil {
		t.Errorf("ResultFor unknown path = %+v, want nil", r)
	}

	all := idx.AllResults()
	if len(all) != 3 {
		t.Errorf("AllResults len = %d, want 3", len(all))
	}
	for rel := range all {
		if !strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, ".py") {
			t.Errorf("AllResults has unexpected key %q", rel)
		}
	}
}

func TestStaleness(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.go": "package a\n\nfunc Foo() {}\n",
	})

	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}

	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer idx.Close()

	// Fresh: ResultFor returns a result.
	if r := idx.ResultFor(root, "a.go"); r == nil {
		t.Fatalf("ResultFor a.go = nil before mutation")
	}

	// Mutate the file's mtime so the staleness check trips. We use a
	// future time to avoid same-nanosecond flakes.
	future := time.Now().Add(10 * time.Second)
	p := filepath.Join(root, "a.go")
	if err := os.Chtimes(p, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	if r := idx.ResultFor(root, "a.go"); r != nil {
		t.Errorf("ResultFor a.go after mtime change = non-nil; want nil (stale)")
	}
}

// TestStaleness_SizeChangeWithSameMtime is the regression for the
// silent-replace bug. Before RecordMeta.Size, ResultFor only
// compared mtime — a same-second rewrite (or restoring a file from
// backup with its original mtime) would return stale scope data.
func TestStaleness_SizeChangeWithSameMtime(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.go": "package a\n\nfunc Foo() {}\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer idx.Close()

	p := filepath.Join(root, "a.go")
	origInfo, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat a.go: %v", err)
	}
	origMtime := origInfo.ModTime()

	// Rewrite with a bigger body, then force the mtime back to what
	// the index has. Size differs; mtime matches.
	if err := os.WriteFile(p, []byte("package a\n\nfunc Foo() { println(\"hello\") }\n"), 0o644); err != nil {
		t.Fatalf("rewrite a.go: %v", err)
	}
	if err := os.Chtimes(p, origMtime, origMtime); err != nil {
		t.Fatalf("chtimes a.go: %v", err)
	}

	if r := idx.ResultFor(root, "a.go"); r != nil {
		t.Errorf("ResultFor a.go after same-mtime rewrite = non-nil; want nil (size changed)")
	}
}

func TestLoadMissing(t *testing.T) {
	edrDir := t.TempDir()
	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load missing: err = %v, want nil", err)
	}
	if idx != nil {
		t.Errorf("Load missing: idx = %+v, want nil", idx)
	}
	if Exists(edrDir) {
		t.Errorf("Exists on empty edrDir = true, want false")
	}
}

// TestResultFor_DoesNotMaterializeAll builds an index with many files
// and verifies that Load + ResultFor(single path) doesn't trigger the
// AllResults lazy materialization. Uses a package-private view of the
// cache since we control this package.
func TestResultFor_DoesNotMaterializeAll(t *testing.T) {
	files := make(map[string]string, 50)
	for i := 0; i < 50; i++ {
		files[fmt.Sprintf("pkg%d/f.go", i)] = fmt.Sprintf("package pkg%d\n\nfunc Fn%d() {}\n", i, i)
	}
	root, edrDir := setupRepo(t, files)

	n, err := Build(root, edrDir, walkDir(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n != 50 {
		t.Fatalf("Build count = %d, want 50", n)
	}

	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer idx.Close()

	// After Load: header populated, cache empty.
	if idx.cached != nil {
		t.Fatalf("after Load, idx.cached = %v, want nil", idx.cached)
	}
	if got, want := len(idx.header.Records), 50; got != want {
		t.Fatalf("after Load, header.Records len = %d, want %d", got, want)
	}

	// ResultFor a single file: decodes the one record, cache still nil.
	target := filepath.Join("pkg7", "f.go")
	r := idx.ResultFor(root, target)
	if r == nil {
		t.Fatalf("ResultFor(%s) = nil", target)
	}
	if r.File != target {
		t.Errorf("ResultFor(%s).File = %q, want %q", target, r.File, target)
	}
	if idx.cached != nil {
		t.Errorf("after single ResultFor, idx.cached populated (len=%d); ResultFor should not materialize all records", len(idx.cached))
	}

	// AllResults: now cache populates.
	all := idx.AllResults()
	if len(all) != 50 {
		t.Errorf("AllResults len = %d, want 50", len(all))
	}
	if idx.cached == nil {
		t.Errorf("after AllResults, idx.cached is still nil")
	}
	if len(idx.cached) != 50 {
		t.Errorf("after AllResults, idx.cached len = %d, want 50", len(idx.cached))
	}

	// Second AllResults returns the same cached map (no re-decode).
	// Go maps don't support pointer identity, so probe via mutation: set
	// a sentinel on one and check the other sees it.
	all2 := idx.AllResults()
	all["__probe__"] = nil
	if _, ok := all2["__probe__"]; !ok {
		t.Errorf("AllResults returned a fresh map on second call (cache not reused)")
	}
	delete(all, "__probe__")
}
