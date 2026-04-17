package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildAndLoad(t *testing.T) {
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")

	// Seed with one Go, one TS, one Python file.
	files := map[string]string{
		"a.go": `package p

func Hello() string { return "hi" }
`,
		"b.ts": `export function greet(name: string): string {
  return "hi " + name
}
`,
		"c.py": `def add(x, y):
    return x + y
`,
		"README.md": "# not indexed\n",
	}
	for name, content := range files {
		path := filepath.Join(tmp, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	walk := func(root string, fn func(string) error) error {
		return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			return fn(path)
		})
	}

	n, err := Build(tmp, edrDir, walk)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n != 3 {
		t.Errorf("expected 3 indexed files, got %d", n)
	}
	if !Exists(edrDir) {
		t.Error("scope.bin not present after Build")
	}

	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if idx == nil {
		t.Fatal("Load returned nil")
	}

	for _, rel := range []string{"a.go", "b.ts", "c.py"} {
		r := idx.ResultFor(tmp, rel)
		if r == nil {
			t.Errorf("no result for %q", rel)
			continue
		}
		if len(r.Decls) == 0 {
			t.Errorf("%q: no decls extracted", rel)
		}
	}

	// README.md should not be in the index.
	if r := idx.ResultFor(tmp, "README.md"); r != nil {
		t.Errorf("README.md should not be indexed")
	}
}

func TestStaleness(t *testing.T) {
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")

	path := filepath.Join(tmp, "x.py")
	if err := os.WriteFile(path, []byte("def foo(): pass\n"), 0644); err != nil {
		t.Fatal(err)
	}

	walk := func(root string, fn func(string) error) error {
		return fn(path)
	}

	if _, err := Build(tmp, edrDir, walk); err != nil {
		t.Fatal(err)
	}
	idx, _ := Load(edrDir)
	if idx.ResultFor(tmp, "x.py") == nil {
		t.Fatal("expected fresh result")
	}

	// Modify the file to make the cache stale.
	if err := os.WriteFile(path, []byte("def bar(): pass\n"), 0644); err != nil {
		t.Fatal(err)
	}
	// Force a different mtime in case the write was within the same nanosecond.
	if err := os.Chtimes(path, time.Now().Add(time.Second), time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	if idx.ResultFor(tmp, "x.py") != nil {
		t.Errorf("expected stale result to return nil after file change")
	}
}
