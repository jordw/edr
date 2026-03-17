package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGoVerifyScope(t *testing.T) {
	tests := []struct {
		name  string
		files []string
		want  string
	}{
		{"no files falls back to ./...", nil, "./..."},
		{"empty files falls back to ./...", []string{}, "./..."},
		{"root package file", []string{"main.go"}, "."},
		{"single subpackage", []string{"internal/dispatch/dispatch_verify.go"}, "./internal/dispatch"},
		{"multiple files same package", []string{"cmd/batch.go", "cmd/root.go"}, "./cmd"},
		{"multiple packages", []string{"cmd/batch.go", "internal/dispatch/dispatch_verify.go"}, ""},
		{"mixed root and sub", []string{"main.go", "cmd/batch.go"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			flags := map[string]any{}
			if tt.files != nil {
				flags["files"] = tt.files
			}
			got := goVerifyScope("/fake/root", flags)
			if tt.want == "" {
				// Multi-package: check expected dirs are present (map iteration order varies)
				if got == "./..." {
					t.Errorf("expected scoped packages, got ./...")
				}
				if tt.name == "mixed root and sub" {
					if !contains(got, ".") || !contains(got, "./cmd") {
						t.Errorf("expected both . and ./cmd in %q", got)
					}
				} else {
					if !contains(got, "./cmd") || !contains(got, "./internal/dispatch") {
						t.Errorf("expected both ./cmd and ./internal/dispatch in %q", got)
					}
				}
			} else if got != tt.want {
				t.Errorf("goVerifyScope() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGoReverseImporters(t *testing.T) {
	// Use the real repo root — goReverseImporters shells out to `go list`.
	root := findRepoRoot(t)

	t.Run("finds importers of internal/search", func(t *testing.T) {
		edited := map[string]bool{"./internal/search": true}
		importers := goReverseImporters(root, edited)
		// internal/dispatch imports internal/search
		found := false
		for _, pkg := range importers {
			if pkg == "./internal/dispatch" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected ./internal/dispatch in importers of ./internal/search, got %v", importers)
		}
	})

	t.Run("returns nil for nonexistent package", func(t *testing.T) {
		edited := map[string]bool{"./internal/nonexistent": true}
		importers := goReverseImporters(root, edited)
		if len(importers) != 0 {
			t.Errorf("expected no importers for nonexistent package, got %v", importers)
		}
	})

	t.Run("returns nil for fake root", func(t *testing.T) {
		edited := map[string]bool{"./internal/search": true}
		importers := goReverseImporters("/fake/nonexistent/root", edited)
		if importers != nil {
			t.Errorf("expected nil for fake root, got %v", importers)
		}
	})

	t.Run("scope includes importers", func(t *testing.T) {
		flags := map[string]any{"files": []string{"internal/search/search.go"}}
		scope := goVerifyScope(root, flags)
		if !contains(scope, "./internal/dispatch") {
			t.Errorf("verify scope should include ./internal/dispatch (importer of internal/search), got %q", scope)
		}
		if !contains(scope, "./internal/search") {
			t.Errorf("verify scope should include ./internal/search (edited package), got %q", scope)
		}
	})
}

// findRepoRoot walks up from the current directory to find go.mod.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > len(sub) && containsStr(s, sub))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
