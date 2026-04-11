package dispatch

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
)

// setupTestGraph creates a temp edr dir with an import graph for testing.
func setupTestGraph(t *testing.T, files []string, edges [][2]string) string {
	t.Helper()
	dir := t.TempDir()
	edrDir := filepath.Join(dir, ".edr")
	os.MkdirAll(edrDir, 0755)

	// Write a root.txt so HomeEdrDir can find it
	// (we'll pass root directly to heuristicRank)

	graph := idx.BuildImportGraph(files, edges)
	if err := idx.WriteImportGraph(edrDir, graph); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRankCandidates_ImportCountPrimary(t *testing.T) {
	// File with high import count should rank first
	root := setupTestGraph(t,
		[]string{"core/api.h", "core/api.c", "plugins/ext.c", "test/test.c"},
		[][2]string{
			{"core/api.c", "core/api.h"},
			{"plugins/ext.c", "core/api.h"},
			{"test/test.c", "core/api.h"},
		},
	)
	candidates := []index.SymbolInfo{
		{Name: "init", Type: "function", File: filepath.Join(root, "plugins/ext.c"), StartLine: 10, EndLine: 20},
		{Name: "init", Type: "function", File: filepath.Join(root, "core/api.h"), StartLine: 5, EndLine: 50},
		{Name: "init", Type: "function", File: filepath.Join(root, "test/test.c"), StartLine: 1, EndLine: 5},
	}
	ranked := rankCandidates(candidates, "init", root)
	if len(ranked) < 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ranked))
	}
	// core/api.h has 3 inbound imports — should rank first
	if ranked[0].Rel != "core/api.h" {
		t.Errorf("expected core/api.h first (most imported), got %s (score %d)", ranked[0].Rel, ranked[0].Score)
	}
	// test/test.c should rank last (0 imports, small span)
	if ranked[2].Rel != "test/test.c" {
		t.Errorf("expected test/test.c last, got %s", ranked[2].Rel)
	}
}

func TestRankCandidates_HeaderInheritance(t *testing.T) {
	// A .c file should inherit its .h's import count
	root := setupTestGraph(t,
		[]string{"lib/queue.h", "lib/queue.c", "drivers/foo.c"},
		[][2]string{
			{"lib/queue.c", "lib/queue.h"},
			{"drivers/foo.c", "lib/queue.h"},
		},
	)
	candidates := []index.SymbolInfo{
		{Name: "enqueue", Type: "function", File: filepath.Join(root, "lib/queue.c"), StartLine: 50, EndLine: 100},
		{Name: "enqueue", Type: "function", File: filepath.Join(root, "drivers/foo.c"), StartLine: 10, EndLine: 30},
	}
	ranked := rankCandidates(candidates, "enqueue", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	// lib/queue.c should rank first: its .h has 2 inbound imports
	if ranked[0].Rel != "lib/queue.c" {
		t.Errorf("expected lib/queue.c first (header inherited), got %s (score %d)", ranked[0].Rel, ranked[0].Score)
	}
}

func TestRankCandidates_SpanTiebreaker(t *testing.T) {
	// When import counts are equal (both 0), larger span wins
	root := setupTestGraph(t, []string{"a.go", "b.go"}, nil)
	candidates := []index.SymbolInfo{
		{Name: "Run", Type: "function", File: filepath.Join(root, "a.go"), StartLine: 10, EndLine: 20},
		{Name: "Run", Type: "function", File: filepath.Join(root, "b.go"), StartLine: 10, EndLine: 150},
	}
	ranked := rankCandidates(candidates, "Run", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	if ranked[0].Rel != "b.go" {
		t.Errorf("expected b.go first (larger span), got %s", ranked[0].Rel)
	}
}

func TestRankCandidates_TestPenalty(t *testing.T) {
	root := setupTestGraph(t, []string{"core.go", "test/core_test.go"}, nil)
	candidates := []index.SymbolInfo{
		{Name: "Config", Type: "struct", File: filepath.Join(root, "test/core_test.go"), StartLine: 10, EndLine: 50},
		{Name: "Config", Type: "struct", File: filepath.Join(root, "core.go"), StartLine: 10, EndLine: 50},
	}
	ranked := rankCandidates(candidates, "Config", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	if ranked[0].Rel != "core.go" {
		t.Errorf("expected core.go first (test penalty), got %s", ranked[0].Rel)
	}
}

func TestRankCandidates_NameMatchQuality(t *testing.T) {
	root := setupTestGraph(t, []string{"a.go", "b.go"}, nil)
	candidates := []index.SymbolInfo{
		{Name: "HandleRequest", Type: "function", File: filepath.Join(root, "a.go"), StartLine: 10, EndLine: 50},
		{Name: "handle", Type: "function", File: filepath.Join(root, "b.go"), StartLine: 10, EndLine: 50},
	}
	// Querying "handle" — exact case-insensitive match on b.go, prefix on a.go
	ranked := rankCandidates(candidates, "handle", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	// Exact match should win (tier 1 vs tier 2)
	if ranked[0].Rel != "b.go" {
		t.Errorf("expected b.go first (exact match), got %s", ranked[0].Rel)
	}
}

func TestRankCandidates_NoGraph(t *testing.T) {
	// Should still work without an import graph (no edr dir)
	root := "/nonexistent/repo"
	candidates := []index.SymbolInfo{
		{Name: "foo", Type: "function", File: "/nonexistent/repo/a.go", StartLine: 10, EndLine: 100},
		{Name: "foo", Type: "function", File: "/nonexistent/repo/b.go", StartLine: 10, EndLine: 20},
	}
	ranked := rankCandidates(candidates, "foo", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	// Larger span should still win when no graph
	if ranked[0].Rel != "a.go" {
		t.Errorf("expected a.go first (larger span, no graph), got %s", ranked[0].Rel)
	}
}

func TestIsTestPath(t *testing.T) {
	yes := []string{
		"test/unit_test.go", "tests/foo.py", "testing/bar.c",
		"spec/models_spec.rb", "__tests__/App.test.tsx",
		"pkg/foo_test.go", "test_helper.rb",
	}
	no := []string{
		"src/main.go", "lib/config.go", "kernel/sched/core.c",
	}
	for _, p := range yes {
		if !isTestPath(p) {
			t.Errorf("expected test path: %s", p)
		}
	}
	for _, p := range no {
		if isTestPath(p) {
			t.Errorf("should not be test path: %s", p)
		}
	}
}

func TestIsVendorPath(t *testing.T) {
	yes := []string{"vendor/lib.go", "node_modules/react/index.js", "third_party/foo.c"}
	no := []string{"src/vendor.go", "lib/config.go"}
	for _, p := range yes {
		if !isVendorPath(p) {
			t.Errorf("expected vendor path: %s", p)
		}
	}
	for _, p := range no {
		if isVendorPath(p) {
			t.Errorf("should not be vendor path: %s", p)
		}
	}
}
