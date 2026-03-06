package gather

import (
	"context"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/jordw/edr/internal/index"
)

// isTestFile returns true if the file path matches common test file patterns.
func isTestFile(path string) bool {
	base := filepath.Base(path)
	return strings.Contains(base, "_test.") ||
		strings.HasPrefix(base, "test_") ||
		strings.Contains(base, ".test.") ||
		strings.Contains(base, ".spec.") ||
		strings.Contains(base, "Test.java") ||
		filepath.Base(filepath.Dir(path)) == "__tests__"
}

// testFilePatterns returns candidate test file paths for a given source file.
func testFilePatterns(file string) []string {
	dir := filepath.Dir(file)
	ext := filepath.Ext(file)
	base := strings.TrimSuffix(filepath.Base(file), ext)

	var patterns []string
	switch ext {
	case ".go":
		patterns = append(patterns, filepath.Join(dir, base+"_test.go"))
	case ".py":
		patterns = append(patterns,
			filepath.Join(dir, "test_"+base+".py"),
			filepath.Join(dir, base+"_test.py"),
		)
	case ".js", ".jsx", ".ts", ".tsx":
		patterns = append(patterns,
			filepath.Join(dir, base+".test"+ext),
			filepath.Join(dir, base+".spec"+ext),
			filepath.Join(dir, "__tests__", base+ext),
		)
	case ".rs":
		// For Rust, tests are typically in the same file via `mod tests`.
		// We handle this by looking for test symbols in the same file.
		patterns = append(patterns, file)
	case ".java":
		patterns = append(patterns, filepath.Join(dir, base+"Test.java"))
	}
	return patterns
}

// upperFirst returns s with the first letter uppercased.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// FindRelatedTests finds test functions/files related to a symbol using
// file-based, name-based, and reference-based heuristics.
func FindRelatedTests(ctx context.Context, db *index.DB, symbolName, file string) []index.SymbolInfo {
	seen := make(map[string]bool) // file:name dedup key
	var results []index.SymbolInfo

	addUnique := func(s index.SymbolInfo) {
		key := s.File + ":" + s.Name
		if seen[key] {
			return
		}
		seen[key] = true
		results = append(results, s)
	}

	// --- (a) File-based: look for symbols in companion test files ---
	candidates := testFilePatterns(file)
	for _, testFile := range candidates {
		syms, err := db.GetSymbolsByFile(ctx, testFile)
		if err != nil {
			continue
		}
		for _, s := range syms {
			// For Rust same-file tests, only include symbols whose name contains "test"
			if testFile == file {
				if !strings.Contains(strings.ToLower(s.Name), "test") {
					continue
				}
			}
			addUnique(s)
		}
	}

	// --- (b) Name-based: search index for Test+SymbolName patterns ---
	// Go convention: TestParseConfig for parseConfig
	ucName := upperFirst(symbolName)
	namePatterns := []string{
		"Test" + ucName,
		"test_" + symbolName,
		"test_" + strings.ToLower(symbolName),
	}
	for _, pat := range namePatterns {
		syms, err := db.SearchSymbols(ctx, pat)
		if err != nil {
			continue
		}
		for _, s := range syms {
			addUnique(s)
		}
	}

	// Also look for symbols containing the target name in test files
	allMatches, err := db.SearchSymbols(ctx, symbolName)
	if err == nil {
		for _, s := range allMatches {
			if isTestFile(s.File) {
				addUnique(s)
			}
		}
	}

	// --- (c) Reference-based: find test files that reference the symbol ---
	refs, err := index.FindReferencesInFile(ctx, db, symbolName, file)
	if err == nil {
		// Build a set of test files that contain references
		testFiles := make(map[string]bool)
		for _, ref := range refs {
			if isTestFile(ref.File) && ref.File != file {
				testFiles[ref.File] = true
			}
		}
		// Get symbols from those test files
		for tf := range testFiles {
			syms, err := db.GetSymbolsByFile(ctx, tf)
			if err != nil {
				continue
			}
			for _, s := range syms {
				addUnique(s)
			}
		}
	}

	return results
}
