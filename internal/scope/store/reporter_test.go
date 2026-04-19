package store

import (
	"testing"
)

func TestReporter_NoStore(t *testing.T) {
	edrDir := t.TempDir()
	r := NewReporter(edrDir)
	got := r.Status()
	if got.Name != "scope" {
		t.Errorf("Name: got %q, want scope", got.Name)
	}
	if got.Exists {
		t.Error("Exists: want false with no scope.bin")
	}
	if got.Files != 0 || got.Bytes != 0 {
		t.Errorf("want zero values when missing, got Files=%d Bytes=%d", got.Files, got.Bytes)
	}
}

func TestReporter_WithStore(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.go":      "package a\n\nfunc Foo() {}\n",
		"sub/b.go":  "package sub\n\nfunc Bar() {}\n",
		"script.py": "def hello():\n    pass\n",
	})
	n, err := Build(root, edrDir, walkDir(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n == 0 {
		t.Fatal("Build indexed zero files")
	}

	r := NewReporter(edrDir)
	got := r.Status()
	if !got.Exists {
		t.Fatal("Exists: want true after Build")
	}
	if got.Files != n {
		t.Errorf("Files: got %d, want %d", got.Files, n)
	}
	if got.Bytes == 0 {
		t.Error("Bytes: want > 0")
	}
	if got.Stale {
		t.Error("Stale: scope Reporter does not set Stale (per-file staleness); want false")
	}
}
