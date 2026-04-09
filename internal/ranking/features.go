package ranking

import (
	"math"
	"path/filepath"
	"strings"
)

// Feature indices into the flat feature vector.
const (
	FCaseExact    = iota // 1 if name == query (case-sensitive)
	FCaseMatch           // 1 if case-insensitive match
	FIsPrefix            // 1 if query is prefix of name (case-insensitive)
	FIsSuffix            // 1 if query is suffix of name
	FNameLenRatio        // len(query) / len(name), clamped to [0,1]
	// Symbol type (one-hot)
	FTypeFunc
	FTypeMethod
	FTypeStruct
	FTypeIface
	FTypeOther
	// Symbol properties
	FIsDefinition // struct/class/interface/type
	FLogSpan      // log2(span+1) / 10, clamped to [0,1]
	// Path features
	FDepth       // path separator count / 8, clamped to [0,1]
	FIsInclude   // starts with include/
	FIsCore      // core infrastructure directory
	FIsPeripheral
	FIsTools
	FIsTest
	FIsVendor
	FIsDoc
	FIsSample
	FIsScripts
	// File extension (one-hot)
	FExtC     // .c or .h
	FExtGo    // .go
	FExtRust  // .rs
	FExtPyTS // .py, .ts, .js
	// = 26 features = NumFeatures
	// Note: "other extension" is implicit when all ext features are 0.
)

// CandidateFeatures holds the raw info needed to extract features.
type CandidateFeatures struct {
	Name      string
	Type      string // "function", "method", "struct", etc.
	File      string // relative path from repo root
	StartLine uint32
	EndLine   uint32
}

// ExtractFeatures computes the feature vector for a single candidate
// relative to a query. Returns a [NumFeatures]float32 slice.
func ExtractFeatures(query string, c CandidateFeatures) [NumFeatures]float32 {
	var f [NumFeatures]float32
	queryLower := strings.ToLower(query)
	nameLower := strings.ToLower(c.Name)

	// Name match features
	if c.Name == query {
		f[FCaseExact] = 1
	}
	if nameLower == queryLower {
		f[FCaseMatch] = 1
	}
	if strings.HasPrefix(nameLower, queryLower) {
		f[FIsPrefix] = 1
	}
	if strings.HasSuffix(nameLower, queryLower) {
		f[FIsSuffix] = 1
	}
	if len(c.Name) > 0 {
		ratio := float32(len(query)) / float32(len(c.Name))
		if ratio > 1 {
			ratio = 1
		}
		f[FNameLenRatio] = ratio
	}

	// Symbol type (one-hot)
	switch c.Type {
	case "function":
		f[FTypeFunc] = 1
	case "method":
		f[FTypeMethod] = 1
	case "struct", "class":
		f[FTypeStruct] = 1
	case "interface", "trait":
		f[FTypeIface] = 1
	default:
		f[FTypeOther] = 1
	}

	// Definition type
	switch c.Type {
	case "struct", "class", "interface", "type", "trait", "enum":
		f[FIsDefinition] = 1
	}

	// Span (log-normalized)
	span := int(c.EndLine) - int(c.StartLine) + 1
	if span < 1 {
		span = 1
	}
	f[FLogSpan] = clamp(float32(math.Log2(float64(span)))/10.0, 0, 1)

	// Path features
	rel := c.File
	depth := strings.Count(rel, string(filepath.Separator))
	f[FDepth] = clamp(float32(depth)/8.0, 0, 1)

	if strings.HasPrefix(rel, "include/") || strings.HasPrefix(rel, "include\\") {
		f[FIsInclude] = 1
	}

	topDir := strings.SplitN(rel, string(filepath.Separator), 2)[0]

	// Core infrastructure
	switch topDir {
	case "kernel", "core", "init", "mm", "fs", "net", "block", "ipc", "security",
		"internal", "pkg", "cmd", "src", "lib":
		f[FIsCore] = 1
	}

	// Peripheral
	switch topDir {
	case "drivers", "plugins", "extensions", "addons", "contrib",
		"adapters", "connectors", "integrations":
		f[FIsPeripheral] = 1
	}

	// Tools
	switch topDir {
	case "tools", "tool", "util", "utils", "hack", "misc":
		f[FIsTools] = 1
	}

	// Test
	if isTestFile(rel) {
		f[FIsTest] = 1
	}

	// Vendor
	switch topDir {
	case "vendor", "node_modules", "third_party":
		f[FIsVendor] = 1
	}

	// Doc
	switch topDir {
	case "docs", "doc", "documentation", "Documentation":
		f[FIsDoc] = 1
	}

	// Sample
	switch topDir {
	case "examples", "example", "samples", "sample", "demo", "demos":
		f[FIsSample] = 1
	}

	// Scripts/build
	switch topDir {
	case "scripts", "script", "build", "ci", "deploy":
		f[FIsScripts] = 1
	}

	// File extension (one-hot)
	ext := strings.ToLower(filepath.Ext(rel))
	switch ext {
	case ".c", ".h", ".cc", ".cpp", ".hpp", ".cxx":
		f[FExtC] = 1
	case ".go":
		f[FExtGo] = 1
	case ".rs":
		f[FExtRust] = 1
	case ".py", ".ts", ".js", ".tsx", ".jsx":
		f[FExtPyTS] = 1
	}

	return f
}

// ExtractAll builds the flat feature matrix for a batch of candidates.
// Returns [n * NumFeatures]float32.
func ExtractAll(query string, candidates []CandidateFeatures) []float32 {
	out := make([]float32, len(candidates)*NumFeatures)
	for i, c := range candidates {
		f := ExtractFeatures(query, c)
		copy(out[i*NumFeatures:], f[:])
	}
	return out
}

func isTestFile(rel string) bool {
	lower := strings.ToLower(rel)
	for _, seg := range []string{"test/", "tests/", "testing/", "spec/", "__tests__/", "_test.", "test_"} {
		if strings.Contains(lower, seg) {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
