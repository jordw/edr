package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoCanonicalPath_ModuleRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/m\n\ngo 1.21\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cache := newGoCanonicalPathCache()
	got := cache.CanonicalPathForGoFile(filepath.Join(dir, "main.go"))
	if got != "example.com/m" {
		t.Errorf("got %q, want example.com/m", got)
	}
}

func TestGoCanonicalPath_Nested(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0644)
	os.MkdirAll(filepath.Join(dir, "internal", "output"), 0755)
	cache := newGoCanonicalPathCache()
	got := cache.CanonicalPathForGoFile(filepath.Join(dir, "internal", "output", "output.go"))
	if got != "example.com/m/internal/output" {
		t.Errorf("got %q, want example.com/m/internal/output", got)
	}
}

func TestGoCanonicalPath_NoGoMod(t *testing.T) {
	dir := t.TempDir()
	cache := newGoCanonicalPathCache()
	got := cache.CanonicalPathForGoFile(filepath.Join(dir, "main.go"))
	if got != "" {
		t.Errorf("got %q, want empty (no go.mod)", got)
	}
}

func TestGoCanonicalPath_Cached(t *testing.T) {
	// Two files in the same package should resolve via the cache on
	// the second call without re-reading go.mod.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n"), 0644)
	cache := newGoCanonicalPathCache()
	_ = cache.CanonicalPathForGoFile(filepath.Join(dir, "a.go"))
	// Remove go.mod — a cached lookup should still succeed.
	os.Remove(filepath.Join(dir, "go.mod"))
	got := cache.CanonicalPathForGoFile(filepath.Join(dir, "b.go"))
	if got != "example.com/m" {
		t.Errorf("cached lookup returned %q, want example.com/m", got)
	}
}
