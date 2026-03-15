package dispatch

import (
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
