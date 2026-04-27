package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/output"
)

// TestStrict_RenameSucceedsOnResolved: a clean Go function call where
// every ref binds to BindResolved should pass --strict and apply the
// rename normally.
func TestStrict_RenameSucceedsOnResolved(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"main.go": "package main\n\nfunc Compute(x int) int { return x * 2 }\n\nfunc main() { _ = Compute(5) }\n",
	})

	res, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"main.go:Compute"},
		map[string]any{"new_name": "Calculate", "strict": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rr := mustRename(t, res)
	if rr.Status != "applied" {
		t.Fatalf("status=%q want applied; result=%+v", rr.Status, rr)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(data), "Calculate") || strings.Contains(string(data), "Compute") {
		t.Errorf("rename did not apply: %s", data)
	}
}

// TestStrict_RenameRefusesOnProbable: a Ruby file with two classes
// each defining `compute` plus a call on a non-locally-typed receiver
// produces BindProbable refs in the target file. Strict must refuse
// and surface counts + an example.
func TestStrict_RenameRefusesOnProbable(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"both.rb": `class A
  def compute
    1
  end
end

class B
  def compute
    2
  end
end

def runner(obj)
  obj.compute
end
`,
	})

	res, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"both.rb:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true, "strict": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rr := mustRename(t, res)
	if rr.Status != "refused" {
		t.Fatalf("status=%q want refused; result=%+v", rr.Status, rr)
	}
	if rr.RefusedReason != "strict_refused" {
		t.Errorf("refused_reason=%q want strict_refused", rr.RefusedReason)
	}
	if len(rr.RefusedCounts) == 0 {
		t.Errorf("refused_counts empty: %+v", rr)
	}
	if !strings.Contains(rr.SeeAlso, "include-name-match") {
		t.Errorf("see_also did not point at refs-to --include-name-match: %q", rr.SeeAlso)
	}
}

// TestStrict_RenameRefusesOnCrossFileProbable: rename target lives in
// a.rb, but the call sites that the Ruby cross-file renamer pulls into
// fileSpans include a property_access ref in b.rb (BindProbable). The
// audit must walk b.rb (not just a.rb) and refuse on the cross-file
// probable ref. This is the gap the same-file-only audit had.
func TestStrict_RenameRefusesOnCrossFileProbable(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.rb": `class A
  def compute
    1
  end
end
`,
		"b.rb": `require_relative 'a'

a = A.new
a.compute
`,
	})

	res, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.rb:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true, "strict": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rr := mustRename(t, res)
	if rr.Status != "refused" {
		t.Fatalf("status=%q want refused; result=%+v", rr.Status, rr)
	}
	if rr.RefusedReason != "strict_refused" {
		t.Errorf("refused_reason=%q want strict_refused", rr.RefusedReason)
	}
	// At least one refused example must come from b.rb — that's the
	// whole point of walking files beyond sym.File.
	sawBRb := false
	for _, ex := range rr.RefusedExamples {
		if strings.HasSuffix(ex.File, "b.rb") {
			sawBRb = true
			break
		}
	}
	if !sawBRb {
		t.Errorf("no refused example came from b.rb; examples=%+v", rr.RefusedExamples)
	}
}

// TestStrict_RenameCrossFileSucceedsOnResolved: cross-file rename of a
// Go function where every call site binds BindResolved across files.
// Strict must accept and apply.
func TestStrict_RenameCrossFileSucceedsOnResolved(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"go.mod": "module example.com/foo\n",
		"a.go":   "package foo\n\nfunc Compute(x int) int { return x * 2 }\n",
		"b.go":   "package foo\n\nfunc Use() int { return Compute(5) + Compute(7) }\n",
	})

	res, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.go:Compute"},
		map[string]any{"new_name": "Calculate", "cross_file": true, "strict": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rr := mustRename(t, res)
	if rr.Status != "applied" {
		t.Fatalf("status=%q want applied; result=%+v", rr.Status, rr)
	}
	bData, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(bData), "Calculate(5)") || strings.Contains(string(bData), "Compute(") {
		t.Errorf("b.go did not get cross-file rename applied: %s", bData)
	}
}

// TestStrict_RenameForceMutuallyExclusive: --strict and --force must
// not be combined. The dispatcher rejects the pairing immediately.
func TestStrict_RenameForceMutuallyExclusive(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"main.go": "package main\nfunc X() {}\n",
	})

	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"main.go:X"},
		map[string]any{"new_name": "Y", "strict": true, "force": true})
	if err == nil {
		t.Fatal("expected error combining --strict and --force")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error did not name the conflict: %v", err)
	}
}

// TestStrict_RefsToFiltersToResolved: --strict on refs-to suppresses
// non-Resolved entries. The Ruby method-collision repo above produces
// probable refs which strict should drop.
func TestStrict_RefsToFiltersToResolved(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.rb": "class A\n  def compute\n    1\n  end\nend\n\na = A.new\na.compute\n",
		"b.rb": "class B\n  def compute\n    2\n  end\nend\n\nb = B.new\nb.compute\n",
	})

	// Without strict — see what tiers the resolver returns.
	resOpen, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.rb:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch open: %v", err)
	}
	openCount, _ := mustMap(t, resOpen)["count"].(int)

	// With strict — must be <= the open count, and binding map must
	// contain only resolved (or be empty).
	resStrict, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.rb:compute"}, map[string]any{"strict": true})
	if err != nil {
		t.Fatalf("dispatch strict: %v", err)
	}
	mStrict := mustMap(t, resStrict)
	strictCount, _ := mStrict["count"].(int)
	if strictCount > openCount {
		t.Errorf("strict count %d > open count %d", strictCount, openCount)
	}
	if b, ok := mStrict["binding"].(map[string]any); ok {
		for k := range b {
			if k != "resolved" {
				t.Errorf("strict refs-to surfaced %q tier; expected only resolved", k)
			}
		}
	}
}

// TestNameMatch_CountOnly: refs-to --include-name-match should report a
// count for word-bounded text matches scope did not bind. The
// non-Resolved Ruby case above means there are name-match-only matches
// in b.rb (B.compute and the call) that scope didn't link to A's
// compute.
func TestNameMatch_CountOnly(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.rb": "class A\n  def compute\n    1\n  end\nend\n",
		"b.rb": "class B\n  def compute\n    2\n  end\nend\n\nb = B.new\nb.compute\n",
	})

	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.rb:compute"},
		map[string]any{"include_name_match": true, "strict": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := mustMap(t, res)
	extra, ok := m["name_match_extra"].(int)
	if !ok || extra <= 0 {
		t.Errorf("name_match_extra not > 0; result=%+v", m)
	}
	// Default form: no by_file map and no entries list.
	if _, ok := m["name_match_by_file"]; ok {
		t.Errorf("default form should not include name_match_by_file")
	}
	if _, ok := m["name_match_entries"]; ok {
		t.Errorf("default form should not include name_match_entries")
	}
}

// TestNameMatch_ByFile: --by-file groups name-match-only counts by
// file. The b.rb file should appear with > 0 hits.
func TestNameMatch_ByFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.rb": "class A\n  def compute\n    1\n  end\nend\n",
		"b.rb": "class B\n  def compute\n    2\n  end\nend\n\nb = B.new\nb.compute\n",
	})

	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.rb:compute"},
		map[string]any{"include_name_match": true, "by_file": true, "strict": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := mustMap(t, res)
	perFile, ok := m["name_match_by_file"].(map[string]int)
	if !ok || len(perFile) == 0 {
		t.Fatalf("name_match_by_file missing or empty; result=%+v", m)
	}
	// Should not include the entries list under by_file.
	if _, ok := m["name_match_entries"]; ok {
		t.Errorf("by_file should not include the per-line entries list")
	}
}

// TestNameMatch_List: --list emits per-occurrence entries; default
// budget caps at 50.
func TestNameMatch_List(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.rb": "class A\n  def compute\n    1\n  end\nend\n",
		"b.rb": "class B\n  def compute\n    2\n  end\nend\n\nb = B.new\nb.compute\n",
	})

	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.rb:compute"},
		map[string]any{"include_name_match": true, "list": true, "strict": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := mustMap(t, res)
	entries, ok := m["name_match_entries"].([]map[string]any)
	if !ok || len(entries) == 0 {
		t.Fatalf("name_match_entries missing or empty; result=%+v", m)
	}
}

func mustMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", v)
	}
	return m
}

func mustRename(t *testing.T, v any) *output.RenameResult {
	t.Helper()
	rr, ok := v.(*output.RenameResult)
	if !ok {
		t.Fatalf("unexpected result type %T (want *output.RenameResult)", v)
	}
	return rr
}
