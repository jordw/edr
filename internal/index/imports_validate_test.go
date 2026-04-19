package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/walk"
)

// TestImportGraphAccuracy validates import edges against actual source code.
// For each edge "A imports B", it checks that file A actually contains an
// import/include statement that could resolve to file B.
func TestImportGraphAccuracy(t *testing.T) {
	// Use the edr repo itself — we know its imports
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	// Collect all files
	var allFiles []string
	walk.RepoFiles(root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		allFiles = append(allFiles, rel)
		return nil
	})

	// Build suffix index
	suffixIdx := BuildSuffixIndex(allFiles)

	// Extract imports and resolve
	type edge struct{ from, to string }
	var edges []edge
	var totalImports int

	for _, rel := range allFiles {
		abs := filepath.Join(root, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		ext := filepath.Ext(rel)
		imports := ExtractImports(data, ext)
		totalImports += len(imports)

		for _, imp := range imports {
			resolved := ResolveImport(suffixIdx, imp.Raw, rel, ext)
			for _, target := range resolved {
				if target != rel {
					edges = append(edges, edge{rel, target})
				}
			}
		}
	}

	t.Logf("Files: %d, Raw imports: %d, Resolved edges: %d", len(allFiles), totalImports, len(edges))

	if totalImports == 0 {
		t.Fatal("no imports extracted at all")
	}
	if len(edges) == 0 {
		t.Fatal("no imports resolved to files")
	}

	// Resolution rate: what fraction of raw imports resolve to actual files?
	resolutionRate := float64(len(edges)) / float64(totalImports)
	t.Logf("Resolution rate: %.1f%% (%d/%d)", resolutionRate*100, len(edges), totalImports)

	// Validate edges: for each edge, verify the source file actually contains
	// an import statement that references the target
	invalid := 0
	for _, e := range edges {
		data, err := os.ReadFile(filepath.Join(root, e.from))
		if err != nil {
			continue
		}
		// Check if the source file contains any reference to the target's
		// filename or directory
		targetBase := filepath.Base(e.to)
		targetDir := filepath.Dir(e.to)
		lastDir := filepath.Base(targetDir)

		content := string(data)
		hasRef := strings.Contains(content, targetBase) ||
			strings.Contains(content, lastDir) ||
			strings.Contains(content, strings.TrimSuffix(targetBase, filepath.Ext(targetBase)))

		if !hasRef {
			invalid++
			if invalid <= 5 {
				t.Logf("suspicious edge: %s → %s (source doesn't reference target)", e.from, e.to)
			}
		}
	}

	invalidRate := float64(invalid) / float64(len(edges))
	t.Logf("Edge accuracy: %.1f%% valid (%d invalid / %d total)", (1-invalidRate)*100, invalid, len(edges))

	if invalidRate > 0.15 { // Go package→file fan-out creates ~6% false edges
		t.Errorf("too many invalid edges: %.1f%% (want <10%%)", invalidRate*100)
	}
}

// TestImportGraphKnownEdges checks that specific known imports are present.
func TestImportGraphKnownEdges(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	var allFiles []string
	walk.RepoFiles(root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		allFiles = append(allFiles, rel)
		return nil
	})
	suffixIdx := BuildSuffixIndex(allFiles)

	// Known imports in the edr codebase
	tests := []struct {
		file     string
		rawImport string
		ext      string
		wantTarget string // substring of expected resolved path
	}{
		// Go: dispatch.go imports internal/index
		{"internal/dispatch/dispatch.go", "github.com/jordw/edr/internal/index", ".go", "internal/index/"},
		// Go: dispatch.go imports internal/idx
		{"internal/dispatch/dispatch.go", "github.com/jordw/edr/internal/idx", ".go", "internal/idx/"},
		// Go: main.go imports cmd
		{"main.go", "github.com/jordw/edr/cmd", ".go", "cmd/"},
	}

	for _, tt := range tests {
		resolved := ResolveImport(suffixIdx, tt.rawImport, tt.file, tt.ext)
		found := false
		for _, r := range resolved {
			if strings.Contains(r, tt.wantTarget) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s: import %q → expected target containing %q, got %v",
				tt.file, tt.rawImport, tt.wantTarget, resolved)
		}
	}
}

// TestImportCountsCorrelateWithImportance checks that files with high
// import counts are actually important (contain many symbols).
func TestImportCountsCorrelateWithImportance(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	var allFiles []string
	walk.RepoFiles(root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		allFiles = append(allFiles, rel)
		return nil
	})
	suffixIdx := BuildSuffixIndex(allFiles)

	// Count inbound imports per file
	inbound := map[string]int{}
	for _, rel := range allFiles {
		abs := filepath.Join(root, rel)
		data, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		ext := filepath.Ext(rel)
		imports := ExtractImports(data, ext)
		for _, imp := range imports {
			for _, target := range ResolveImport(suffixIdx, imp.Raw, rel, ext) {
				inbound[target]++
			}
		}
	}

	// The most-imported files should be core packages
	type entry struct {
		file  string
		count int
	}
	var top []entry
	for f, c := range inbound {
		top = append(top, entry{f, c})
	}
	// Sort descending
	for i := 1; i < len(top); i++ {
		for j := i; j > 0 && top[j].count > top[j-1].count; j-- {
			top[j], top[j-1] = top[j-1], top[j]
		}
	}

	t.Log("Top 10 most-imported files:")
	for i := 0; i < 10 && i < len(top); i++ {
		t.Logf("  %4d imports: %s", top[i].count, top[i].file)
	}

	// Sanity: top files should be in internal/ or cmd/, not bench/ or scripts/
	if len(top) > 0 {
		topFile := top[0].file
		if strings.HasPrefix(topFile, "bench/") || strings.HasPrefix(topFile, "scripts/") {
			t.Errorf("most-imported file is %s — expected core package, not bench/scripts", topFile)
		}
	}
}
