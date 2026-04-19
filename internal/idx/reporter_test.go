package idx

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReporter_NoIndex(t *testing.T) {
	edrDir := t.TempDir()
	r := NewReporter("/tmp/fake", edrDir)
	got := r.Status()
	if got.Name != "index" {
		t.Errorf("Name: got %q, want index", got.Name)
	}
	if got.Exists {
		t.Error("Exists: want false with no index on disk")
	}
	if !got.Stale {
		t.Error("Stale: want true with no index on disk")
	}
	if got.Extra != nil {
		t.Errorf("Extra: want nil when index missing, got %v", got.Extra)
	}
}

func TestReporter_WithIndex(t *testing.T) {
	dir := t.TempDir()
	edrDir := t.TempDir()

	writeFile(t, dir, "a.go", "package a\nfunc test() {}")
	d := BuildFull(dir, []string{filepath.Join(dir, "a.go")}, 0)
	if err := os.WriteFile(filepath.Join(edrDir, MainFile), d.Marshal(), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}

	r := NewReporter(dir, edrDir)
	got := r.Status()
	if !got.Exists {
		t.Fatal("Exists: want true after writing index")
	}
	if got.Files != 1 {
		t.Errorf("Files: got %d, want 1", got.Files)
	}
	if got.Bytes == 0 {
		t.Error("Bytes: want > 0")
	}
	if got.Extra == nil {
		t.Fatal("Extra: want non-nil when index exists")
	}
	if _, ok := got.Extra["trigrams"]; !ok {
		t.Error("Extra: missing trigrams key")
	}
}
