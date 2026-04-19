package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestDogfood_ImportGraph_TS runs the full Build pipeline (parse +
// reconcile + import-graph rewrite) against a real TypeScript repo and
// reports aggregate ref resolution counts. Skipped unless
// EDR_SCOPE_DOGFOOD_DIR is set (also accepts EDR_SCOPE_TS_DOGFOOD_DIR
// for parity with earlier docs).
//
// This test exists to measure the delta from the Phase 1 import graph.
// Per-file dogfood tests (internal/scope/ts/dogfood_test.go) only see
// local-scope resolution; they cannot observe refs rewritten from
// "direct_scope → local Import" to "import_export → exported decl in
// another file".
func TestDogfood_ImportGraph_TS(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_DOGFOOD_DIR")
	if dir == "" {
		dir = os.Getenv("EDR_SCOPE_TS_DOGFOOD_DIR")
	}
	if dir == "" {
		t.Skip("EDR_SCOPE_DOGFOOD_DIR (or EDR_SCOPE_TS_DOGFOOD_DIR) not set")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}

	// walkFn: same filter as Build but tightened to TS-only so the test
	// runs in reasonable time on big monorepos. We still run Build over
	// the full TS tree; non-TS files would just be filtered by Build's
	// ext switch anyway, but walking them costs I/O.
	walk := func(root string, fn func(string) error) error {
		return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if name == "node_modules" || name == ".git" || name == "build" ||
					name == "dist" || name == "compiled" {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(path))
			switch ext {
			case ".ts", ".tsx", ".mts", ".cts":
				return fn(path)
			}
			return nil
		})
	}

	edrDir := t.TempDir()
	n, err := Build(abs, edrDir, walk)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Logf("Build: indexed %d files", n)

	idx, err := Load(edrDir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Aggregate stats across all TS records.
	var totalRefs, resolved, unresolved int
	reasonCounts := map[string]int{}
	importExportCount := 0
	for rel := range idx.header.Records {
		if !isTSLike(rel) {
			continue
		}
		r := idx.ResultFor(abs, rel)
		if r == nil {
			continue
		}
		for _, ref := range r.Refs {
			totalRefs++
			switch ref.Binding.Kind {
			case scope.BindResolved:
				resolved++
				if ref.Binding.Reason == "import_export" {
					importExportCount++
				}
				reasonCounts["resolved:"+ref.Binding.Reason]++
			case scope.BindUnresolved:
				unresolved++
				reasonCounts["unresolved:"+ref.Binding.Reason]++
			}
		}
	}

	t.Logf("=== Phase 1 import-graph dogfood ===")
	t.Logf("total refs:         %d", totalRefs)
	if totalRefs > 0 {
		t.Logf("resolved:           %d (%.1f%%)", resolved, 100*float64(resolved)/float64(totalRefs))
		t.Logf("  of which import_export: %d (%.1f%% of all refs)",
			importExportCount, 100*float64(importExportCount)/float64(totalRefs))
		t.Logf("unresolved:         %d (%.1f%%)", unresolved, 100*float64(unresolved)/float64(totalRefs))
	}
	type rc struct {
		r string
		n int
	}
	var rcs []rc
	for r, c := range reasonCounts {
		rcs = append(rcs, rc{r, c})
	}
	sort.Slice(rcs, func(i, j int) bool { return rcs[i].n > rcs[j].n })
	t.Logf("reason breakdown:")
	for _, x := range rcs {
		t.Logf("  %-40s %d", x.r, x.n)
	}
}
