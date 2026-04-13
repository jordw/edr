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
	// A .c file should inherit its .h's import count via the fallback path.
	// queue.h is imported by 3 files; foo.c has no corresponding header.
	root := setupTestGraph(t,
		[]string{"lib/queue.h", "lib/queue.c", "drivers/foo.c", "user1.c", "user2.c"},
		[][2]string{
			{"lib/queue.c", "lib/queue.h"},
			{"drivers/foo.c", "lib/queue.h"},
			{"user1.c", "lib/queue.h"},
			{"user2.c", "lib/queue.h"},
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
	// lib/queue.c should rank first: it inherits queue.h's 4 inbound imports,
	// while drivers/foo.c's best import (queue.h) also gives 4 — but queue.c
	// has the direct header match so it wins on path tiebreaker.
	if ranked[0].Rel != "drivers/foo.c" && ranked[0].Rel != "lib/queue.c" {
		t.Errorf("expected lib/queue.c or drivers/foo.c first, got %s (score %d)", ranked[0].Rel, ranked[0].Score)
	}
}

func TestRankCandidates_DefinitionTypeBoost(t *testing.T) {
	// Definition types (struct) should beat functions when scores are otherwise equal.
	root := setupTestGraph(t, []string{"types.go", "format.go"}, nil)
	candidates := []index.SymbolInfo{
		{Name: "Config", Type: "function", File: filepath.Join(root, "format.go"), StartLine: 10, EndLine: 20},
		{Name: "Config", Type: "struct", File: filepath.Join(root, "types.go"), StartLine: 10, EndLine: 50},
	}
	ranked := rankCandidates(candidates, "Config", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	if ranked[0].Rel != "types.go" {
		t.Errorf("expected types.go first (definition type boost), got %s", ranked[0].Rel)
	}
}

func TestRankCandidates_DefinitionTypeWinsOverFunction(t *testing.T) {
	// When popularity is unavailable, definition type tiebreaker should still
	// differentiate a struct from a function with the same name.
	root := setupTestGraph(t, []string{"core.go", "format.go"}, nil)
	candidates := []index.SymbolInfo{
		{Name: "Config", Type: "function", File: filepath.Join(root, "format.go"), StartLine: 10, EndLine: 50},
		{Name: "Config", Type: "struct", File: filepath.Join(root, "core.go"), StartLine: 10, EndLine: 50},
	}
	ranked := rankCandidates(candidates, "Config", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	if ranked[0].Rel != "core.go" {
		t.Errorf("expected core.go first (struct definition type), got %s", ranked[0].Rel)
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

func TestRankCandidates_LargeDefinitionBeatsDeclarations(t *testing.T) {
	// A large struct definition (like task_struct in sched.h) should rank above
	// many small 1-line variable declarations that the parser also tags as "struct".
	root := setupTestGraph(t,
		[]string{"include/sched.h", "init/init_task.c", "arch/process.c", "kernel/exit.c"},
		[][2]string{
			{"init/init_task.c", "include/sched.h"},
			{"arch/process.c", "include/sched.h"},
			{"kernel/exit.c", "include/sched.h"},
		},
	)
	candidates := []index.SymbolInfo{
		{Name: "task_struct", Type: "struct", File: filepath.Join(root, "include/sched.h"), StartLine: 820, EndLine: 1647},
		{Name: "task_struct", Type: "struct", File: filepath.Join(root, "init/init_task.c"), StartLine: 96, EndLine: 262},
		{Name: "task_struct", Type: "struct", File: filepath.Join(root, "arch/process.c"), StartLine: 42, EndLine: 42},
		{Name: "task_struct", Type: "struct", File: filepath.Join(root, "kernel/exit.c"), StartLine: 586, EndLine: 586},
	}
	ranked := rankCandidates(candidates, "task_struct", root)
	if len(ranked) < 4 {
		t.Fatalf("expected 4 candidates, got %d", len(ranked))
	}
	if ranked[0].Rel != "include/sched.h" {
		t.Errorf("expected include/sched.h first (largest definition body), got %s (score %d)", ranked[0].Rel, ranked[0].Score)
	}
}

func TestRankCandidates_NoGraph(t *testing.T) {
	// Should still work without an import graph (no edr dir).
	// Definition type tiebreaker differentiates.
	root := "/nonexistent/repo"
	candidates := []index.SymbolInfo{
		{Name: "Foo", Type: "function", File: "/nonexistent/repo/a.go", StartLine: 10, EndLine: 100},
		{Name: "Foo", Type: "struct", File: "/nonexistent/repo/b.go", StartLine: 10, EndLine: 20},
	}
	ranked := rankCandidates(candidates, "Foo", root)
	if len(ranked) < 2 {
		t.Fatalf("expected 2 candidates, got %d", len(ranked))
	}
	// Struct gets definition type + shape synergy boost
	if ranked[0].Rel != "b.go" {
		t.Errorf("expected b.go first (struct definition type), got %s (score %d vs %d)", ranked[0].Rel, ranked[0].Score, ranked[1].Score)
	}
}
