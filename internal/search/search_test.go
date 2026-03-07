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

func TestScoreSymbolMatch(t *testing.T) {
	tests := []struct {
		name    string
		symbol  string
		pattern string
		want    float64
	}{
		{"exact match", "Config", "Config", 1.0},
		{"case-insensitive exact", "config", "Config", 0.95},
		{"prefix match", "ConfigParser", "Config", 0.8},
		{"case-insensitive prefix", "configParser", "Config", 0.75},
		{"suffix match", "parseConfig", "Config", 0.7},
		{"case-insensitive suffix", "parseconfig", "Config", 0.65},
		{"contains", "MyConfigParser", "Config", 0.5},
		{"contains case-insensitive", "myconfigparser", "Config", 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoreSymbolMatch(tt.symbol, tt.pattern)
			if got != tt.want {
				t.Errorf("scoreSymbolMatch(%q, %q) = %v, want %v", tt.symbol, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestScoreSymbolMatch_ExactBeatsAll(t *testing.T) {
	exact := scoreSymbolMatch("Config", "Config")
	ciExact := scoreSymbolMatch("config", "Config")
	prefix := scoreSymbolMatch("ConfigParser", "Config")
	ciPrefix := scoreSymbolMatch("configParser", "Config")
	suffix := scoreSymbolMatch("parseConfig", "Config")
	ciSuffix := scoreSymbolMatch("parseconfig", "Config")
	contains := scoreSymbolMatch("MyConfigParser", "Config")

	if exact <= ciExact {
		t.Error("exact should beat case-insensitive exact")
	}
	if ciExact <= prefix {
		t.Error("case-insensitive exact should beat prefix")
	}
	if prefix <= ciPrefix {
		t.Error("prefix should beat case-insensitive prefix")
	}
	if ciPrefix <= suffix {
		t.Error("case-insensitive prefix should beat suffix")
	}
	if suffix <= ciSuffix {
		t.Error("suffix should beat case-insensitive suffix")
	}
	if ciSuffix <= contains {
		t.Error("case-insensitive suffix should beat contains")
	}
}

func TestScoreSymbolMatch_SuffixBeatsContains(t *testing.T) {
	// This is important for method name searches like searching "Config"
	// and wanting "parseConfig" to rank above "MyConfigParser"
	suffix := scoreSymbolMatch("parseConfig", "Config")
	contains := scoreSymbolMatch("MyConfigParser", "Config")
	if suffix <= contains {
		t.Errorf("suffix score (%v) should beat contains score (%v)", suffix, contains)
	}
}

func TestScoreSymbolMatch_PrefixBeatsContains(t *testing.T) {
	prefix := scoreSymbolMatch("ConfigDSL", "Config")
	contains := scoreSymbolMatch("RetryConfigDSL", "Config")
	if prefix <= contains {
		t.Errorf("prefix score (%v) should beat contains score (%v)", prefix, contains)
	}
}

// Ensure filepath is used (it's needed for isSourceFile via filepath.Ext)
var _ = filepath.Ext
