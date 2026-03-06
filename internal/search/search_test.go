package search

import (
	"path/filepath"
	"testing"
)

func TestIsSourceFile(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"main.go", true},
		{"app.py", true},
		{"index.js", true},
		{"component.tsx", true},
		{"lib.rs", true},
		{"Main.java", true},
		{"config.yaml", false},
		{"README.md", false},
		{"Dockerfile", false},
		{"data.json", false},
	}
	for _, tt := range tests {
		got := isSourceFile(tt.path)
		if got != tt.want {
			t.Errorf("isSourceFile(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestScoreTextMatch_SourceFileBonus(t *testing.T) {
	srcScore := scoreTextMatch("main.go", "func main()", "main", "main", nil)
	docScore := scoreTextMatch("README.md", "func main()", "main", "main", nil)
	if srcScore <= docScore {
		t.Errorf("source file score (%f) should be > doc score (%f)", srcScore, docScore)
	}
}

func TestScoreTextMatch_ExactCaseBonus(t *testing.T) {
	exactScore := scoreTextMatch("main.go", "Dispatch function", "Dispatch", "dispatch", nil)
	caseScore := scoreTextMatch("main.go", "dispatch function", "Dispatch", "dispatch", nil)
	if exactScore <= caseScore {
		t.Errorf("exact case score (%f) should be > case-insensitive score (%f)", exactScore, caseScore)
	}
}

func TestMatchDoublestar(t *testing.T) {
	tests := []struct {
		path, pattern string
		want          bool
	}{
		{"src/main.go", "**/*.go", true},
		{"main.go", "**/*.go", true},
		{"src/deep/file.py", "**/*.py", true},
		{"src/main.go", "**/*.py", false},
	}
	for _, tt := range tests {
		got := matchDoublestar(tt.path, tt.pattern)
		if got != tt.want {
			t.Errorf("matchDoublestar(%q, %q) = %v, want %v", tt.path, tt.pattern, got, tt.want)
		}
	}
}

func TestMatchesAnyPath(t *testing.T) {
	tests := []struct {
		base, rel string
		patterns  []string
		want      bool
	}{
		{"main.go", "src/main.go", []string{"*.go"}, true},
		{"main.go", "src/main.go", []string{"*.py"}, false},
		{"main.go", "src/main.go", []string{"**/*.go"}, true},
	}
	for _, tt := range tests {
		got := matchesAnyPath(tt.base, tt.rel, tt.patterns)
		if got != tt.want {
			t.Errorf("matchesAnyPath(%q, %q, %v) = %v, want %v",
				tt.base, tt.rel, tt.patterns, got, tt.want)
		}
	}
}

// Ensure filepath is used (it's needed for isSourceFile via filepath.Ext)
var _ = filepath.Ext
