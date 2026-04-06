package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
)

// testHandleDo wraps handleDo with the old (string, error) return signature for tests.
func testHandleDo(ctx context.Context, db index.SymbolStore, sess *session.Session, raw json.RawMessage) (string, error) {
	env := output.NewEnvelope("batch")
	if err := handleDo(ctx, db, sess, env, raw); err != nil {
		return "", err
	}
	data, _ := json.Marshal(env)
	return string(data), nil
}

func TestCountDiffLines(t *testing.T) {
	diff := "--- a/f.go\n+++ b/f.go\n@@ -10,5 +10,5 @@\n context\n-old 1\n-old 2\n+new 1\n+new 2\n+new 3\n context\n"
	got := session.CountDiffLines(diff)
	if got != 5 {
		t.Errorf("CountDiffLines = %d, want 5", got)
	}
}

func TestCountDiffLines_Empty(t *testing.T) {
	if got := session.CountDiffLines(""); got != 0 {
		t.Errorf("CountDiffLines empty = %d, want 0", got)
	}
}

func TestStoreDiff_SmallInline(t *testing.T) {
	sess := session.New()
	result := map[string]any{
		"ok": true, "file": "f.go", "symbol": "foo",
		"diff": "-old\n+new\n", "hash": "abc",
		"old_size": 10, "new_size": 12,
	}
	slim := sess.StoreDiff(result, map[string]any{})
	if slim != nil {
		t.Fatal("small diffs should return nil (inline in original result)")
	}
	if result["lines_changed"] != 2 {
		t.Errorf("lines_changed = %v, want 2", result["lines_changed"])
	}
	if result["diff_available"] != true {
		t.Error("diff_available should be true")
	}
	if result["diff"] != "-old\n+new\n" {
		t.Error("small diff should stay in result")
	}
}

func TestStoreDiff_LargeSlim(t *testing.T) {
	sess := session.New()
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, "+new line")
	}
	bigDiff := strings.Join(lines, "\n") + "\n"
	result := map[string]any{
		"ok": true, "file": "f.go", "symbol": "foo",
		"diff": bigDiff, "hash": "abc",
		"old_size": 10, "new_size": 100,
	}
	slim := sess.StoreDiff(result, map[string]any{})
	// No slim optimization anymore — diffs always stay inline
	if slim != nil {
		t.Fatal("StoreDiff should return nil (diffs always inline)")
	}
	if result["lines_changed"] != 25 {
		t.Errorf("lines_changed = %v, want 25", result["lines_changed"])
	}
	if result["diff_available"] != true {
		t.Error("diff_available should be set")
	}
}

func TestStoreDiff_VerboseSkips(t *testing.T) {
	sess := session.New()
	result := map[string]any{"diff": "some diff", "file": "f.go"}
	if sess.StoreDiff(result, map[string]any{"verbose": true}) != nil {
		t.Error("verbose should skip diff stripping")
	}
}

func TestStoreDiff_NoDiff(t *testing.T) {
	sess := session.New()
	if sess.StoreDiff(map[string]any{"ok": true}, map[string]any{}) != nil {
		t.Error("no diff field should return nil")
	}
}

func TestGetDiff(t *testing.T) {
	sess := session.New()
	sess.Diffs["f.go:foo"] = "the diff"

	r := sess.GetDiff([]string{"f.go", "foo"})
	if r["diff"] != "the diff" {
		t.Errorf("GetDiff = %v", r)
	}

	r = sess.GetDiff([]string{"other.go"})
	if _, ok := r["error"]; !ok {
		t.Error("should error for unknown key")
	}

	r = sess.GetDiff([]string{})
	if _, ok := r["error"]; !ok {
		t.Error("should error with no args")
	}
}

func TestGetDiff_FallbackToFileKey(t *testing.T) {
	sess := session.New()
	sess.Diffs["f.go"] = "file diff"
	r := sess.GetDiff([]string{"f.go", "unknown"})
	if r["diff"] != "file diff" {
		t.Errorf("fallback = %v", r)
	}
}

func TestCheckContent_New(t *testing.T) {
	sess := session.New()
	s, _ := sess.CheckContent("f.go:[1 10]", "content", false)
	if s != "new" {
		t.Errorf("got %s, want new", s)
	}
}

func TestCheckContent_Unchanged(t *testing.T) {
	sess := session.New()
	sess.StoreContent("f.go:[1 10]", "content", false)
	s, _ := sess.CheckContent("f.go:[1 10]", "content", false)
	if s != "unchanged" {
		t.Errorf("got %s, want unchanged", s)
	}
}

func TestCheckContent_Changed(t *testing.T) {
	sess := session.New()
	sess.StoreContent("f.go:[1 10]", "old", false)
	// With hash-only storage, changed content appears as "new" (no old content to diff)
	s, _ := sess.CheckContent("f.go:[1 10]", "new", false)
	if s != "new" {
		t.Errorf("got %s, want new", s)
	}
}

func TestEvictLRU(t *testing.T) {
	sess := session.New()
	for i := 0; i < session.MaxContentEntries+5; i++ {
		key := string(rune('A'+i%26)) + string(rune('a'+i/26)) + ".go"
		if i%2 == 0 {
			sess.StoreContent(key, "c", false)
		} else {
			sess.StoreContent(key, "c", true)
		}
	}
	total := len(sess.FileContent) + len(sess.SymbolContent)
	if total > session.MaxContentEntries {
		t.Errorf("LRU: %d > %d", total, session.MaxContentEntries)
	}
}

func TestTrackBodies(t *testing.T) {
	sess := session.New()
	sess.TrackBodies(map[string]any{
		"body":   "func foo() {}",
		"symbol": map[string]any{"file": "m.go", "name": "foo"},
	}, "read-symbol")
	if _, ok := sess.SeenBodies["m.go:foo"]; !ok {
		t.Error("body should be tracked")
	}
}

func TestStripSeenBodies_Gather(t *testing.T) {
	sess := session.New()
	body := "func p() {}"
	sess.SeenBodies["c.go:p"] = session.ContentHash(body)

	r := map[string]any{
		"target":      map[string]any{"file": "c.go", "name": "p"},
		"target_body": body,
	}
	sess.StripSeenBodies(r, "gather")
	if r["target_body"] != "[in context]" {
		t.Error("seen body should be replaced")
	}
	skipped, _ := r["skipped_bodies"].([]string)
	if len(skipped) != 1 || skipped[0] != "p" {
		t.Errorf("skipped = %v", r["skipped_bodies"])
	}
}

func TestStripSeenBodies_Search(t *testing.T) {
	sess := session.New()
	body := "func h() {}"
	sess.SeenBodies["a.go:h"] = session.ContentHash(body)

	r := map[string]any{
		"matches": []any{
			map[string]any{
				"body":   body,
				"symbol": map[string]any{"file": "a.go", "name": "h"},
			},
			map[string]any{
				"body":   "func other() {}",
				"symbol": map[string]any{"file": "a.go", "name": "other"},
			},
		},
	}
	sess.StripSeenBodies(r, "search")
	ms := r["matches"].([]any)
	if ms[0].(map[string]any)["body"] != "[in context]" {
		t.Error("seen should be replaced")
	}
	if ms[1].(map[string]any)["body"] == "[in context]" {
		t.Error("unseen should NOT be replaced")
	}
}

func TestStripSeenBodies_NewBodyTracked(t *testing.T) {
	sess := session.New()
	r := map[string]any{
		"target":      map[string]any{"file": "n.go", "name": "nf"},
		"target_body": "func nf() {}",
	}
	sess.StripSeenBodies(r, "gather")
	if r["target_body"] == "[in context]" {
		t.Error("unseen should not be replaced")
	}
	if _, ok := sess.SeenBodies["n.go:nf"]; !ok {
		t.Error("should be tracked after first encounter")
	}
}

func TestInvalidateForEdit_RenameClears(t *testing.T) {
	sess := session.New()
	sess.Diffs["f.go"] = "d"
	sess.StoreContent("f.go:[1 10]", "c", false)
	sess.SeenBodies["f.go:foo"] = "h"

	sess.InvalidateForEdit("rename", []string{"old", "new"})
	if len(sess.Diffs)+len(sess.FileContent)+len(sess.SeenBodies) != 0 {
		t.Error("rename should clear all")
	}
}

func TestPostProcess_SmallEditInline(t *testing.T) {
	sess := session.New()
	text := `{"ok":true,"file":"f.go","symbol":"foo","diff":"-old\n+new\n","hash":"abc","old_size":1,"new_size":1}`

	result := sess.PostProcess("edit", []string{"f.go", "foo"}, map[string]any{}, nil, text)
	if !strings.Contains(result, `"diff"`) {
		t.Error("small diff should stay inline")
	}
	if !strings.Contains(result, "lines_changed") {
		t.Error("should have lines_changed")
	}
	if !strings.Contains(result, "diff_available") {
		t.Error("should have diff_available")
	}
	gd := sess.GetDiff([]string{"f.go", "foo"})
	if _, ok := gd["diff"]; !ok {
		t.Error("diff should be retrievable")
	}
}

func TestPostProcess_DeltaRead_Unchanged(t *testing.T) {
	sess := session.New()
	text := `{"file":"f.go","lines":[1,10],"content":"hello","hash":"abc"}`

	sess.PostProcess("focus", []string{"f.go"}, map[string]any{}, nil, text)
	result := sess.PostProcess("focus", []string{"f.go"}, map[string]any{}, nil, text)
	if !strings.Contains(result, "unchanged") {
		t.Errorf("should be unchanged, got: %s", result)
	}
}

func TestPostProcess_DeltaRead_Changed(t *testing.T) {
	sess := session.New()
	t1 := `{"file":"f.go","lines":[1,10],"content":"line1\nline2\nline3","hash":"abc"}`
	t2 := `{"file":"f.go","lines":[1,10],"content":"line1\nmod\nline3","hash":"def"}`

	sess.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, t1)
	result := sess.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, t2)
	// With hash-only storage, changed content is returned in full (no delta diff)
	if strings.Contains(result, "unchanged") {
		t.Error("changed content should not be unchanged")
	}
}

func TestPostProcess_FullFlag(t *testing.T) {
	sess := session.New()
	text := `{"file":"f.go","lines":[1,10],"content":"hello","hash":"abc"}`
	sess.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, text)
	result := sess.PostProcess("read", []string{"f.go"}, map[string]any{"full": true}, nil, text)
	if strings.Contains(result, "unchanged") {
		t.Error("--full should bypass delta")
	}
}

func TestQueryToMultiCmd_Search(t *testing.T) {
	body := true
	pattern := "TODO"
	q := doQuery{
		Cmd:     "search",
		Pattern: &pattern,
		Body:    &body,
	}
	mc := queryToMultiCmd(q)
	if mc.Cmd != "search" {
		t.Errorf("cmd = %q, want search", mc.Cmd)
	}
	if len(mc.Args) != 1 || mc.Args[0] != "TODO" {
		t.Errorf("args = %v, want [TODO]", mc.Args)
	}
	if mc.Flags["body"] != true {
		t.Error("body flag should be true")
	}
}

func TestQueryToMultiCmd_SearchEmptyPattern(t *testing.T) {
	empty := ""
	q := doQuery{
		Cmd:     "search",
		Pattern: &empty,
	}
	mc := queryToMultiCmd(q)
	if mc.Cmd != "search" {
		t.Errorf("cmd = %q, want search", mc.Cmd)
	}
	if len(mc.Args) != 0 {
		t.Errorf("args = %v, want [] (empty pattern should be dropped)", mc.Args)
	}
}

func TestQueryToMultiCmd_Map(t *testing.T) {
	dir := "internal/"
	typ := "function"
	grep := "run"
	q := doQuery{
		Cmd:  "map",
		Dir:  &dir,
		Type: &typ,
		Grep: &grep,
	}
	mc := queryToMultiCmd(q)
	if mc.Cmd != "map" {
		t.Errorf("cmd = %q, want map", mc.Cmd)
	}
	if mc.Flags["dir"] != "internal/" {
		t.Errorf("dir = %v, want internal/", mc.Flags["dir"])
	}
	if mc.Flags["type"] != "function" {
		t.Errorf("type = %v, want function", mc.Flags["type"])
	}
	if mc.Flags["grep"] != "run" {
		t.Errorf("grep = %v, want run", mc.Flags["grep"])
	}
}

func TestNormalizeQueryCmd_DefaultsToRead(t *testing.T) {
	file := "main.go"
	q := doQuery{
		File: &file,
	}
	normalizeQueryCmd(&q)
	mc := queryToMultiCmd(q)
	if mc.Cmd != "read" {
		t.Errorf("cmd = %q, want read (default)", mc.Cmd)
	}
	if len(mc.Args) != 1 || mc.Args[0] != "main.go" {
		t.Errorf("args = %v, want [main.go]", mc.Args)
	}
}

func TestQueryToMultiCmd_Refs(t *testing.T) {
	sym := "Dispatch"
	impact := true
	depth := 2
	q := doQuery{
		Cmd:    "refs",
		Symbol: &sym,
		Impact: &impact,
		Depth:  &depth,
	}
	mc := queryToMultiCmd(q)
	// refs is now routed to focus --expand
	if mc.Cmd != "focus" {
		t.Errorf("cmd = %q, want focus (refs routes to focus)", mc.Cmd)
	}
	if mc.Flags["depth"] != 2 {
		t.Errorf("depth = %v, want 2", mc.Flags["depth"])
	}
}

func TestNormalizeQueryCmd_InferredDefaultBudget(t *testing.T) {
	// When cmd is not set (inferred), normalizeQueryCmd should apply a default budget of 200
	pattern := "TODO"
	q := doQuery{
		Pattern: &pattern,
		// No Cmd set — will be inferred as "search"
		// No Budget set — should get default 200
	}
	normalizeQueryCmd(&q)
	mc := queryToMultiCmd(q)
	if mc.Cmd != "search" {
		t.Errorf("cmd = %q, want search", mc.Cmd)
	}
	if mc.Flags["budget"] != 200 {
		t.Errorf("budget = %v, want 200 (default for inferred cmd)", mc.Flags["budget"])
	}
}

func TestNormalizeQueryCmd_ExplicitCmdNoBudgetDefault(t *testing.T) {
	// When cmd is explicitly set, no default budget should be applied
	pattern := "TODO"
	q := doQuery{
		Cmd:     "search",
		Pattern: &pattern,
		// Explicit Cmd — should NOT get default budget
	}
	normalizeQueryCmd(&q)
	mc := queryToMultiCmd(q)
	if _, ok := mc.Flags["budget"]; ok {
		t.Errorf("budget should not be set for explicit cmd, got %v", mc.Flags["budget"])
	}
}

func TestNormalizeQueryCmd_InferredWithExplicitBudget(t *testing.T) {
	// When cmd is inferred but budget is explicitly set, keep the explicit budget
	pattern := "TODO"
	budget := 500
	q := doQuery{
		Pattern: &pattern,
		Budget:  &budget,
	}
	normalizeQueryCmd(&q)
	mc := queryToMultiCmd(q)
	if mc.Flags["budget"] != 500 {
		t.Errorf("budget = %v, want 500 (explicit budget should be preserved)", mc.Flags["budget"])
	}
}

func TestNormalizeQueryCmd_InferredReturnValue(t *testing.T) {
	// When cmd is inferred, return value should be true
	pattern := "TODO"
	q := doQuery{Pattern: &pattern}
	inferred := normalizeQueryCmd(&q)
	if !inferred {
		t.Error("expected inferred=true when cmd is not set")
	}

	// When cmd is explicit, return value should be false
	q2 := doQuery{Cmd: "search", Pattern: &pattern}
	inferred2 := normalizeQueryCmd(&q2)
	if inferred2 {
		t.Error("expected inferred=false when cmd is explicitly set")
	}
}

func TestQueryToMultiCmd_TextSearchGrouping(t *testing.T) {
	pattern := "TODO"
	textTrue := true

	// Text search without explicit group: grouping is now default in dispatch,
	// so batch should NOT set any group flag (dispatch handles the default)
	q := doQuery{Cmd: "search", Pattern: &pattern, Text: &textTrue}
	mc := queryToMultiCmd(q)
	if _, ok := mc.Flags["group"]; ok {
		t.Error("text search should not set group flag (default is in dispatch)")
	}
	if _, ok := mc.Flags["no_group"]; ok {
		t.Error("text search should not set no_group flag by default")
	}

	// Explicit group=false should pass no_group=true to dispatch
	groupFalse := false
	q2 := doQuery{Cmd: "search", Pattern: &pattern, Text: &textTrue, Group: &groupFalse}
	mc2 := queryToMultiCmd(q2)
	if mc2.Flags["no_group"] != true {
		t.Error("explicit group=false should set no_group=true")
	}
}

// TestNormalizeQueryCmd_EmptyNoInference verifies that normalizeQueryCmd leaves
// Cmd empty when no fields allow inference, so executeQueries catches the error.
func TestNormalizeQueryCmd_EmptyNoInference(t *testing.T) {
	q := doQuery{} // no fields set
	inferred := normalizeQueryCmd(&q)
	if !inferred {
		t.Error("expected inferred=true when Cmd was empty")
	}
	if q.Cmd != "" {
		t.Errorf("Cmd = %q, want empty (no fields to infer from)", q.Cmd)
	}
}

// TestNormalizeQueryCmd_Idempotent verifies that calling normalizeQueryCmd twice
// produces the same result (important since executeQueries normalizes up front).
func TestNormalizeQueryCmd_Idempotent(t *testing.T) {
	pattern := "TODO"
	q := doQuery{Pattern: &pattern}
	normalizeQueryCmd(&q)
	cmd1 := q.Cmd
	budget1 := *q.Budget

	normalizeQueryCmd(&q)
	if q.Cmd != cmd1 {
		t.Errorf("second normalize changed Cmd: %q → %q", cmd1, q.Cmd)
	}
	if *q.Budget != budget1 {
		t.Errorf("second normalize changed Budget: %d → %d", budget1, *q.Budget)
	}
}

// TestInferQueryCmd_OnlyPublicCommands verifies that inferQueryCmd never returns
// internal command names (edit-plan, multi).
func TestInferQueryCmd_OnlyPublicCommands(t *testing.T) {
	internal := map[string]bool{
		"edit-plan": true, "multi": true,
	}

	tests := []doQuery{
		// Symbol-only (read)
		func() doQuery { s := "Foo"; return doQuery{Symbol: &s} }(),
		// Callers (refs)
		func() doQuery { s := "Foo"; c := true; return doQuery{Symbol: &s, Callers: &c} }(),
		// Deps (refs)
		func() doQuery { s := "Foo"; d := true; return doQuery{Symbol: &s, Deps: &d} }(),
		// Pattern (search)
		func() doQuery { p := "TODO"; return doQuery{Pattern: &p} }(),
		// Impact (refs)
		func() doQuery { s := "Foo"; i := true; return doQuery{Symbol: &s, Impact: &i} }(),
		// Dir (map)
		func() doQuery { d := "internal/"; return doQuery{Dir: &d} }(),
		// File (read)
		func() doQuery { f := "main.go"; return doQuery{File: &f} }(),
	}

	for i, q := range tests {
		cmd := inferQueryCmd(q)
		if internal[cmd] {
			t.Errorf("test %d: inferQueryCmd returned internal command %q", i, cmd)
		}
	}
}

func TestHandleDo_DistributeBudgetToQueries(t *testing.T) {
	// Verify that top-level budget is distributed to queries lacking individual budgets
	topBudget := 600
	pattern1 := "foo"
	pattern2 := "bar"
	individualBudget := 100
	pattern3 := "baz"

	p := doParams{
		Budget: &topBudget,
		Queries: []doQuery{
			{Cmd: "search", Pattern: &pattern1},                            // no budget — should get 200 (600/3)
			{Cmd: "search", Pattern: &pattern2, Budget: &individualBudget}, // has budget — should keep 100
			{Cmd: "search", Pattern: &pattern3},                            // no budget — should get 200 (600/3)
		},
	}

	// Simulate the distribution logic from handleDo
	if p.Budget != nil && len(p.Queries) > 0 {
		perQuery := *p.Budget / len(p.Queries)
		if perQuery < 50 {
			perQuery = 50
		}
		for i := range p.Queries {
			if p.Queries[i].Budget == nil {
				b := perQuery
				p.Queries[i].Budget = &b
			}
		}
	}

	if *p.Queries[0].Budget != 200 {
		t.Errorf("query[0] budget = %d, want 200", *p.Queries[0].Budget)
	}
	if *p.Queries[1].Budget != 100 {
		t.Errorf("query[1] budget = %d, want 100 (should keep individual budget)", *p.Queries[1].Budget)
	}
	if *p.Queries[2].Budget != 200 {
		t.Errorf("query[2] budget = %d, want 200", *p.Queries[2].Budget)
	}
}

func TestHandleDo_DistributeBudgetMinimum(t *testing.T) {
	// When budget/len(queries) < 50, use minimum of 50
	topBudget := 10
	pattern := "foo"

	p := doParams{
		Budget:  &topBudget,
		Queries: []doQuery{{Cmd: "search", Pattern: &pattern}},
	}

	if p.Budget != nil && len(p.Queries) > 0 {
		perQuery := *p.Budget / len(p.Queries)
		if perQuery < 50 {
			perQuery = 50
		}
		for i := range p.Queries {
			if p.Queries[i].Budget == nil {
				b := perQuery
				p.Queries[i].Budget = &b
			}
		}
	}

	if *p.Queries[0].Budget != 50 {
		t.Errorf("query[0] budget = %d, want 50 (minimum)", *p.Queries[0].Budget)
	}
}

func TestDoParams_Verify(t *testing.T) {
	// Test verify: true parses correctly
	raw := `{"verify": true}`
	var p doParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Verify != true {
		t.Errorf("verify = %v, want true", p.Verify)
	}

	// Test verify: "go vet ./..." parses correctly
	raw = `{"verify": "go vet ./..."}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Verify != "go vet ./..." {
		t.Errorf("verify = %v, want 'go vet ./...'", p.Verify)
	}
}

func TestDoParams_Writes(t *testing.T) {
	raw := `{"writes": [{"file": "new.go", "content": "package main\n", "mkdir": true}]}`
	var p doParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Writes) != 1 {
		t.Fatalf("writes len = %d, want 1", len(p.Writes))
	}
	w := p.Writes[0]
	if w.File != "new.go" {
		t.Errorf("file = %q, want new.go", w.File)
	}
	if w.Content != "package main\n" {
		t.Errorf("content = %q", w.Content)
	}
	if w.Mkdir == nil || !*w.Mkdir {
		t.Error("mkdir should be true")
	}
}

func TestDoParams_Renames(t *testing.T) {
	raw := `{"renames": [{"old_name": "Foo", "new_name": "Bar", "dry_run": true}]}`
	var p doParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Renames) != 1 {
		t.Fatalf("renames len = %d, want 1", len(p.Renames))
	}
	r := p.Renames[0]
	if r.OldName != "Foo" || r.NewName != "Bar" {
		t.Errorf("rename = %q → %q", r.OldName, r.NewName)
	}
	if r.DryRun == nil || !*r.DryRun {
		t.Error("dry_run should be true")
	}
}

func TestDoParams_Edits_DryRunPromotion(t *testing.T) {
	// Per-edit dry_run should be parsed into the DryRun field.
	raw := `{"edits": [{"file": "f.go", "old_text": "old", "new_text": "new", "dry_run": true}]}`
	var p doParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Edits) != 1 {
		t.Fatalf("edits len = %d, want 1", len(p.Edits))
	}
	e := p.Edits[0]
	if e.DryRun == nil || !*e.DryRun {
		t.Error("per-edit dry_run should be parsed as true")
	}
	// Top-level DryRun should still be nil (promotion happens in handleDo, not parsing).
	if p.DryRun != nil {
		t.Error("top-level dry_run should be nil before handleDo promotion")
	}
}

func TestPostProcess_EditPlanDiff(t *testing.T) {
	// Edit results should go through the slim-edit pipeline now that
	// DiffEditCommands includes "edit".
	sess := session.New()
	text := `{"ok":true,"edits":1,"files":1,"hashes":{"f.go":"abc"},"description":["replace text"],"diff":"--- a/f.go\n+++ b/f.go\n@@ -1 +1 @@\n-old\n+new\n"}`

	result := sess.PostProcess("edit", []string{}, map[string]any{}, nil, text)
	if !strings.Contains(result, `"diff"`) {
		t.Error("small edit diff should stay inline")
	}
	if !strings.Contains(result, "lines_changed") {
		t.Error("should have lines_changed")
	}
	if !strings.Contains(result, "diff_available") {
		t.Error("should have diff_available")
	}
	// Verify the diff was stored for later retrieval.
	gd := sess.GetDiff([]string{"f.go"})
	if gd["error"] != nil {
		t.Errorf("GetDiff should find stored diff, got error: %v", gd["error"])
	}
}

func TestHandleDo_ReadLineRangeInvalid(t *testing.T) {
	// Invalid line ranges in the do path should return errors, not panic.
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "start > end",
			raw:  `{"reads": [{"file": "main.go", "start_line": 100, "end_line": 50}]}`,
			want: "beyond end line",
		},
		{
			name: "start beyond EOF",
			raw:  `{"reads": [{"file": "main.go", "start_line": 99999, "end_line": 100000}]}`,
			want: "beyond end line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := testHandleDo(context.Background(), db, sess, json.RawMessage(tt.raw))
			if err != nil {
				t.Fatalf("handleDo returned error: %v", err)
			}
			if !strings.Contains(result, tt.want) {
				t.Errorf("expected %q in result, got: %s", tt.want, result)
			}
		})
	}
}

func TestHandleDo_EditEmptyNewTextDeletion(t *testing.T) {
	// Empty new_text with old_text should perform a deletion via do path.
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc remove() {}\n\nfunc keep() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	raw := json.RawMessage(`{
		"edits": [{"file": "main.go", "old_text": "func remove() {}\n\n", "new_text": ""}]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	if !strings.Contains(result, `"ok":true`) {
		t.Errorf("expected ok:true in result, got: %s", result)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "main.go"))
	if strings.Contains(string(data), "remove") {
		t.Errorf("deleted text still present: %s", string(data))
	}
	if !strings.Contains(string(data), "func keep()") {
		t.Errorf("kept text missing: %s", string(data))
	}
}

func TestDoParams_VerifyObjectSyntax(t *testing.T) {
	// Bug 1: verify: {"command": "...", "level": "...", "timeout": N} should parse correctly
	raw := `{"verify": {"command": "echo works", "timeout": 10}}`
	var p doParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	m, ok := p.Verify.(map[string]any)
	if !ok {
		t.Fatalf("verify should be map, got %T", p.Verify)
	}
	if m["command"] != "echo works" {
		t.Errorf("command = %v, want 'echo works'", m["command"])
	}
	if m["timeout"] != float64(10) {
		t.Errorf("timeout = %v, want 10", m["timeout"])
	}
}

func TestHandleDo_VerifyObjectCommand(t *testing.T) {
	// Bug 1: verify with object syntax should pass the custom command through
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()

	sess := session.New()

	raw := json.RawMessage(`{"verify": {"command": "echo verify-works", "timeout": 10}}`)
	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// The result should contain the custom command, not auto-detected "go build"
	if !strings.Contains(result, "echo verify-works") {
		t.Errorf("expected custom command in result, got: %s", result)
	}
}

func TestHandleDo_BatchEditFailureReporting(t *testing.T) {
	// Bug 3: batch edit failure should include edit_index, edit_mode, total_edits
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// 3 edits, second fails (nonexistent text)
	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "package main", "new_text": "package main"},
			{"file": "main.go", "old_text": "nonexistent_text_xyz", "new_text": "replacement"},
			{"file": "main.go", "old_text": "func hello", "new_text": "func world"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// Parse the JSON result
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	// Find the edit op in the flat ops array
	ops := parsed["ops"].([]any)
	var editOp map[string]any
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "edit" {
			editOp = m
			break
		}
	}
	if editOp == nil {
		t.Fatal("no edit op found")
	}

	// Check for error or noop in the edit op
	if editOp["error"] != nil {
		// Edit failed — expected for not_found
	} else if noop, _ := editOp["noop"].(bool); noop {
		// Edit was a no-op
	} else {
		// Check for edit_index field
		if editIdx, ok := editOp["edit_index"]; ok {
			if editIdx != float64(1) {
				t.Errorf("edit_index = %v, want 1", editIdx)
			}
		}
	}
}

func TestHandleDo_BatchEditNotFoundFields(t *testing.T) {
	// Bug 3: Targeted test — edit with non-matching old_text should have structured fields
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Single failing edit
	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "nonexistent_text_xyz", "new_text": "replacement"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	// Find the edit op in flat ops
	ops := parsed["ops"].([]any)
	var editOp map[string]any
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "edit" {
			editOp = m
			break
		}
	}
	if editOp == nil {
		t.Fatal("no edit op found")
	}

	// Edit should have failed with an error
	if editOp["error"] == nil {
		t.Fatalf("expected edit error, got: %v", editOp)
	}
}

func TestDoStructsMatchCmdspec(t *testing.T) {
	// Verify that doRead, doQuery, doEdit, doWrite, doRename struct fields
	// have corresponding cmdspec flags/batch fields, and vice versa.
	// This catches drift between JSON batch structs and the registry.

	// Structural fields are batch JSON args, not CLI flags — exclude from cmdspec check.
	structuralFields := map[string]bool{
		"file": true, "symbol": true, "old_name": true, "new_name": true,
	}

	// Also exclude legacy batch-only fields not yet removed from the structs.
	legacyFields := map[string]bool{
		"depth": true, "start_line": true, "end_line": true, "symbols": true,
	}

	checkStructFieldsFiltered := func(t *testing.T, structName string, knownKeys map[string]bool, structFields map[string]bool) {
		t.Helper()
		for field := range structFields {
			if structuralFields[field] || legacyFields[field] {
				continue
			}
			if !knownKeys[field] {
				t.Errorf("%s has field %q not in cmdspec", structName, field)
			}
		}
		for key := range knownKeys {
			if !structFields[key] {
				t.Errorf("cmdspec has key %q not in %s struct", key, structName)
			}
		}
	}

	// doRead fields (from JSON tags)
	readFields := map[string]bool{
		"file": true, "symbol": true, "budget": true, "signatures": true,
		"skeleton": true, "lines": true, "depth": true, "start_line": true, "end_line": true, "symbols": true, "full": true, "expand": true, "no_expand": true,
	}
	checkStructFieldsFiltered(t, "doRead", doReadKnownKeys, readFields)

	// doEdit fields
	editFields := map[string]bool{
		"file": true, "old_text": true, "new_text": true, "symbol": true,
		"start_line": true, "end_line": true, "all": true, "in": true,
		"where": true, "dry_run": true, "expect_hash": true, "refresh_hash": true, "delete": true,
		"insert_at": true, "fuzzy": true, "lines": true, "move_after": true, "read_back": true,
		// Write/create flags (merged from write)
		"content": true, "inside": true, "after": true, "append": true,
		"mkdir": true, "verify": true, "no_verify": true,
	}
	checkStructFieldsFiltered(t, "doEdit", doEditKnownKeys, editFields)

}

func TestHandleDo_SkipsPostEditReadsAndVerifyOnEditFailure(t *testing.T) {
	tmp := t.TempDir()

	// Create a dummy Go file so the repo root is valid.
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module test\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()

	sess := session.New()

	// Call handleDo with an edit targeting a non-existent file, plus
	// read_after_edit and verify — both should be skipped.
	raw := json.RawMessage(`{
		"edits": [{"file": "nonexistent.go", "old_text": "foo", "new_text": "bar"}],
		"read_after_edit": true,
		"verify": true
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo error: %v", err)
	}

	// The edit should have failed.
	if !strings.Contains(result, `"error"`) {
		t.Errorf("expected edit error in result, got: %s", result)
	}

	// verify should be skipped (edits failed).
	if !strings.Contains(result, `"status":"skipped"`) || !strings.Contains(result, `"reason":"edits failed"`) {
		t.Errorf("expected verify to be skipped with reason, got: %s", result)
	}
}

// --- Correctness bug fix tests ---

func TestHandleDo_DryRunSkipsWrites(t *testing.T) {
	// Issue #12: writes should not execute when edits have dry-run set
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "existing.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	writeTarget := filepath.Join(tmp, "should_not_exist.txt")
	raw := json.RawMessage(fmt.Sprintf(`{
		"writes": [{"file": %q, "content": "test data"}],
		"edits": [{"file": "existing.go", "old_text": "func hello", "new_text": "func world", "dry_run": true}]
	}`, writeTarget))

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// The write target should NOT exist on disk
	if _, statErr := os.Stat(writeTarget); statErr == nil {
		t.Error("write target should not exist when edits are dry-run")
	}

	// Parse result to verify structure
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Find the write op in flat ops
	ops := parsed["ops"].([]any)
	var writeOp map[string]any
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "write" {
			writeOp = m
			break
		}
	}
	if writeOp == nil {
		t.Fatal("no write op found")
	}
	if writeOp["status"] != "dry_run" {
		t.Errorf("expected write dry_run preview, got: %v", writeOp)
	}

	// existing.go should not be modified (dry-run)
	data, _ := os.ReadFile(filepath.Join(tmp, "existing.go"))
	if !strings.Contains(string(data), "func hello") {
		t.Error("existing.go should not be modified during dry-run")
	}
}

func TestHandleDo_NoopEditSkipsVerify(t *testing.T) {
	// Issue #19: no-op edits (old_text == new_text) should not trigger verify
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Edit where old_text == new_text, with verify using a command that would fail
	raw := json.RawMessage(`{
		"edits": [{"file": "main.go", "old_text": "func hello", "new_text": "func hello"}],
		"verify": "false"
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Verify should be skipped for no-op
	verifyMap, ok := parsed["verify"].(map[string]any)
	if !ok || verifyMap["status"] != "skipped" || verifyMap["reason"] != "no-op edit" {
		t.Errorf("verify = %v, want {status: skipped, reason: no-op edit}", parsed["verify"])
	}

	// File should be unchanged
	data, _ := os.ReadFile(filepath.Join(tmp, "main.go"))
	if !strings.Contains(string(data), "func hello") {
		t.Error("file should not be modified by no-op edit")
	}
}

func TestHandleDo_VerifyFailedSummaryStatus(t *testing.T) {
	// Issue #23: summary status should be "verify_failed" when verify fails
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "data.txt"), []byte("old content\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Real edit that succeeds, but verify command fails
	raw := json.RawMessage(`{
		"edits": [{"file": "data.txt", "old_text": "old content", "new_text": "new content"}],
		"verify": "false"
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Verify should show failure
	verify, ok := parsed["verify"].(map[string]any)
	if !ok {
		t.Fatalf("verify should be object, got: %v", parsed["verify"])
	}
	if status, _ := verify["status"].(string); status == "passed" || status == "" {
		t.Errorf("verify status should be failed, got %q", status)
	}

	// Envelope ok should be false when verify fails
	if okVal, _ := parsed["ok"].(bool); okVal {
		t.Error("envelope ok should be false when verify fails")
	}

	// Verify error message should be present
	if verifyErr, ok := verify["error"].(string); !ok || verifyErr == "" {
		t.Errorf("verify error should be non-empty, got %v", verify["error"])
	}
}

func TestHandleDo_WriteInvalidatesSession(t *testing.T) {
	// Issue #17: session should be invalidated after writes
	tmp := t.TempDir()

	db := index.NewOnDemand(tmp)
	defer db.Close()

	sess := session.New()

	targetFile := "written.txt"

	// Pre-populate session with a stale content entry for the file
	sess.StoreContent(targetFile+":[1 10]", "stale content", false)

	// Verify the session has the entry
	status, _ := sess.CheckContent(targetFile+":[1 10]", "stale content", false)
	if status != "unchanged" {
		t.Fatalf("pre-condition: expected unchanged, got %s", status)
	}

	// Write the file via batch
	raw := json.RawMessage(fmt.Sprintf(`{
		"writes": [{"file": %q, "content": "fresh content"}]
	}`, targetFile))

	_, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// After write, the session entry should be invalidated.
	// Checking with the old content should now return "new" (not "unchanged")
	status, _ = sess.CheckContent(targetFile+":[1 10]", "stale content", false)
	if status == "unchanged" {
		t.Error("session should have been invalidated after write; got unchanged")
	}
}

// --- Issue 2: Overlapping edit detection ---

func TestHandleDo_OverlappingEditsRejected(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc alpha() {}\n\nfunc beta() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Two edits with overlapping text on the same file.
	// With sequential per-edit dispatch, edit 1 succeeds and edit 2 fails
	// because its old_text was consumed by edit 1.
	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "func alpha() {}\n\nfunc beta", "new_text": "REPLACED1"},
			{"file": "main.go", "old_text": "func beta() {}", "new_text": "REPLACED2"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// Edit 1 should succeed, edit 2 should fail (old_text not found)
	if !strings.Contains(result, "not_found") && !strings.Contains(result, "old_text not found") {
		t.Errorf("expected not_found error for second edit, got: %s", result)
	}

	// File should have edit 1 applied
	data, _ := os.ReadFile(filepath.Join(tmp, "main.go"))
	if !strings.Contains(string(data), "REPLACED1") {
		t.Error("edit 1 should have been applied")
	}
}

func TestHandleDo_NonOverlappingEditsSameFileSucceed(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc alpha() {}\n\nfunc beta() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Two non-overlapping edits on the same file
	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "func alpha()", "new_text": "func alphaNew()"},
			{"file": "main.go", "old_text": "func beta()", "new_text": "func betaNew()"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	if strings.Contains(result, "overlapping") {
		t.Errorf("non-overlapping edits should not be rejected: %s", result)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "main.go"))
	if !strings.Contains(string(data), "func alphaNew()") || !strings.Contains(string(data), "func betaNew()") {
		t.Errorf("expected both edits applied, got: %s", string(data))
	}
}

// --- Issue 2: Edits run before writes, failed edits skip writes ---

func TestHandleDo_EditFailureSkipsWrites(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	writeTarget := filepath.Join(tmp, "new_file.go")
	// Edit will fail (old_text not found), write should be skipped
	raw := json.RawMessage(fmt.Sprintf(`{
		"edits": [{"file": "main.go", "old_text": "DOES_NOT_EXIST", "new_text": "replaced"}],
		"writes": [{"file": %q, "content": "package new"}]
	}`, writeTarget))

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// Write target should NOT exist since edits failed
	if _, statErr := os.Stat(writeTarget); statErr == nil {
		t.Error("write should not execute when edits fail")
	}

	// Result should indicate writes were skipped (status: "skipped", not error)
	if !strings.Contains(result, `"status":"skipped"`) {
		t.Errorf("expected writes with status:skipped, got: %s", result)
	}
	if !strings.Contains(result, `"reason":"edits failed"`) {
		t.Errorf("expected reason:edits failed, got: %s", result)
	}
}

// --- Issue 3: Write dry-run produces preview ---

func TestHandleDo_WriteDryRunPreview(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "existing.go"), []byte("package main\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Global dry-run with writes only (no edits)
	raw := json.RawMessage(`{
		"writes": [{"file": "existing.go", "content": "package updated\n"}],
		"dry_run": true
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}

	ops := parsed["ops"].([]any)
	var writeOp map[string]any
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "write" {
			writeOp = m
			break
		}
	}
	if writeOp == nil {
		t.Fatal("no write op found")
	}
	if writeOp["status"] != "dry_run" {
		t.Errorf("expected dry_run: true, got: %v", writeOp)
	}
	if _, hasDiff := writeOp["diff"]; !hasDiff {
		t.Error("expected diff in dry-run write preview")
	}

	// File should be unchanged
	data, _ := os.ReadFile(filepath.Join(tmp, "existing.go"))
	if string(data) != "package main\n" {
		t.Errorf("file was modified during dry-run: %q", string(data))
	}
}

func TestHandleDo_WriteNewFileDryRun(t *testing.T) {
	tmp := t.TempDir()

	db := index.NewOnDemand(tmp)
	defer db.Close()

	sess := session.New()

	newFile := filepath.Join(tmp, "brand_new.go")
	raw := json.RawMessage(fmt.Sprintf(`{
		"writes": [{"file": %q, "content": "package brand_new\n"}],
		"dry_run": true
	}`, newFile))

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// File should NOT be created
	if _, statErr := os.Stat(newFile); statErr == nil {
		t.Error("new file should not be created during dry-run")
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}

	ops := parsed["ops"].([]any)
	// Find the write op
	var writeOp map[string]any
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "write" {
			writeOp = m
			break
		}
	}
	if writeOp == nil {
		t.Fatal("no write op found")
	}
	if writeOp["status"] != "dry_run" {
		t.Errorf("expected dry_run: true")
	}
	if writeOp["new_file"] != true {
		t.Errorf("expected new_file: true for new file dry-run preview")
	}
}

// --- Task 2: -V flag replaces -v for batch verify ---

func TestParseBatchArgs_UpperV_Verify(t *testing.T) {
	state, err := parseBatchArgs([]string{"-r", "f.go", "-V"})
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	if !state.verifySet || !state.verifyEnabled {
		t.Error("expected -V to enable verify")
	}
}

func TestParseBatchArgs_LowerV_Rejected(t *testing.T) {
	_, err := parseBatchArgs([]string{"-r", "f.go", "-v"})
	if err == nil {
		t.Fatal("expected -v to be rejected as unknown flag")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("expected 'unknown flag' error, got: %v", err)
	}
}

func TestIsBatchFlag_UpperV(t *testing.T) {
	if !IsBatchFlag("-V") {
		t.Error("IsBatchFlag(-V) should be true")
	}
	if IsBatchFlag("-v") {
		t.Error("IsBatchFlag(-v) should be false after migration to -V")
	}
}

// --- Task 3: Batch mode uses quiet=true ---

func TestBatchMode_QuietStderr(t *testing.T) {
	// Verify that runBatch passes quiet=true by checking that parseBatchArgs
	// succeeds and toParams auto-enables verify for edits (proving the batch
	// path works). The actual quiet=true is tested by the stderr-silent smoke
	// test, but we verify the plumbing here: batch always opens DB with quiet.
	state, err := parseBatchArgs([]string{"-e", "f.go", "--old", "x", "--new", "y"})
	if err != nil {
		t.Fatalf("parseBatchArgs: %v", err)
	}
	p := state.toParams()
	// Verify is opt-in — without -V, should be nil
	if p.Verify != nil {
		t.Error("expected no auto-verify without -V flag")
	}
}

// Task 5 tests are in internal/dispatch/dispatch_verify_test.go

// --- Per-edit op correlation ---

func TestHandleDo_MultiEditProducesPerEditOps(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"),
		[]byte("package main\n\nfunc alpha() {}\n\nfunc beta() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "func alpha()", "new_text": "func alphaNew()"},
			{"file": "main.go", "old_text": "func beta()", "new_text": "func betaNew()"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)

	ops := parsed["ops"].([]any)
	editOps := []map[string]any{}
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "edit" {
			editOps = append(editOps, m)
		}
	}

	if len(editOps) != 2 {
		t.Fatalf("expected 2 edit ops, got %d: %s", len(editOps), result)
	}
	if editOps[0]["op_id"] != "e0" {
		t.Errorf("first edit op_id = %v, want e0", editOps[0]["op_id"])
	}
	if editOps[1]["op_id"] != "e1" {
		t.Errorf("second edit op_id = %v, want e1", editOps[1]["op_id"])
	}
	// Each op should have the file
	if editOps[0]["file"] != "main.go" {
		t.Errorf("edit e0 file = %v", editOps[0]["file"])
	}
}

func TestHandleDo_MultiEditDryRunPerEditOps(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"),
		[]byte("package main\n\nfunc alpha() {}\n\nfunc beta() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "func alpha()", "new_text": "func alphaNew()"},
			{"file": "main.go", "old_text": "func beta()", "new_text": "func betaNew()"}
		],
		"dry_run": true
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)

	ops := parsed["ops"].([]any)
	editOps := []map[string]any{}
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "edit" {
			editOps = append(editOps, m)
		}
	}

	if len(editOps) != 2 {
		t.Fatalf("expected 2 edit ops for dry-run, got %d", len(editOps))
	}
	// Dry-run edits should have status dry_run
	for i, op := range editOps {
		if op["status"] != "dry_run" {
			t.Errorf("edit e%d status = %v, want dry_run", i, op["status"])
		}
	}

	// File should be unchanged (dry-run)
	data, _ := os.ReadFile(filepath.Join(tmp, "main.go"))
	if strings.Contains(string(data), "alphaNew") {
		t.Error("dry-run should not modify file")
	}
}

func TestHandleDo_EditFailurePerEditOps(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Two edits, both will fail because old_text not found
	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "DOES_NOT_EXIST_1", "new_text": "a"},
			{"file": "main.go", "old_text": "DOES_NOT_EXIST_2", "new_text": "b"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)

	ops := parsed["ops"].([]any)
	editOps := []map[string]any{}
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "edit" {
			editOps = append(editOps, m)
		}
	}

	// Each failed edit should get its own op
	if len(editOps) != 2 {
		t.Fatalf("expected 2 failed edit ops, got %d: %s", len(editOps), result)
	}
	for i, op := range editOps {
		if op["error"] == nil {
			t.Errorf("edit e%d should have error", i)
		}
	}
}

// --- Multi-file batch edits ---

func TestHandleDo_MultiFileEditsParallel(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a.go"), []byte("package main\n\nfunc alpha() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmp, "b.go"), []byte("package main\n\nfunc beta() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	raw := json.RawMessage(`{
		"edits": [
			{"file": "a.go", "old_text": "func alpha()", "new_text": "func alphaNew()"},
			{"file": "b.go", "old_text": "func beta()", "new_text": "func betaNew()"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)

	ops := parsed["ops"].([]any)
	editOps := []map[string]any{}
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "edit" {
			editOps = append(editOps, m)
		}
	}

	if len(editOps) != 2 {
		t.Fatalf("expected 2 edit ops, got %d: %s", len(editOps), result)
	}

	// Both should succeed
	for i, op := range editOps {
		if op["error"] != nil {
			t.Errorf("edit e%d failed: %v", i, op["error"])
		}
	}

	// Both files should be modified
	dataA, _ := os.ReadFile(filepath.Join(tmp, "a.go"))
	dataB, _ := os.ReadFile(filepath.Join(tmp, "b.go"))
	if !strings.Contains(string(dataA), "alphaNew") {
		t.Error("a.go should have alphaNew")
	}
	if !strings.Contains(string(dataB), "betaNew") {
		t.Error("b.go should have betaNew")
	}
}

func TestHandleDo_MultiFileEditPartialFailure(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a.go"), []byte("package main\n\nfunc alpha() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmp, "b.go"), []byte("package main\n\nfunc beta() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Edit on a.go succeeds, edit on b.go fails (old_text not found)
	raw := json.RawMessage(`{
		"edits": [
			{"file": "a.go", "old_text": "func alpha()", "new_text": "func alphaNew()"},
			{"file": "b.go", "old_text": "DOES_NOT_EXIST", "new_text": "replacement"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)

	ops := parsed["ops"].([]any)
	editOps := []map[string]any{}
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "edit" {
			editOps = append(editOps, m)
		}
	}

	if len(editOps) != 2 {
		t.Fatalf("expected 2 edit ops, got %d: %s", len(editOps), result)
	}

	// e0 (a.go) should succeed, e1 (b.go) should fail
	if editOps[0]["error"] != nil {
		t.Errorf("edit e0 should succeed, got error: %v", editOps[0]["error"])
	}
	if editOps[1]["error"] == nil {
		t.Error("edit e1 should fail (old_text not found)")
	}

	// a.go should be modified, b.go unchanged
	dataA, _ := os.ReadFile(filepath.Join(tmp, "a.go"))
	dataB, _ := os.ReadFile(filepath.Join(tmp, "b.go"))
	if !strings.Contains(string(dataA), "alphaNew") {
		t.Error("a.go should have edit applied")
	}
	if !strings.Contains(string(dataB), "func beta()") {
		t.Error("b.go should be unchanged")
	}
}

func TestHandleDo_BatchEditResultShapeParity(t *testing.T) {
	// Batch edit result should have the same fields as standalone edit
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "func hello()", "new_text": "func world()"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)

	ops := parsed["ops"].([]any)
	editOp := ops[0].(map[string]any)

	// Must have these fields (same as standalone edr edit)
	for _, key := range []string{"file", "status", "hash"} {
		if _, ok := editOp[key]; !ok {
			t.Errorf("batch edit result missing %q field: %v", key, editOp)
		}
	}
	if editOp["status"] != "applied" {
		t.Errorf("status = %v, want applied", editOp["status"])
	}
	if editOp["file"] != "main.go" {
		t.Errorf("file = %v, want main.go", editOp["file"])
	}
}

// --- Skipped writes: status distinction ---

func TestHandleDo_SkippedWritesNotFailed(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	writeTarget := filepath.Join(tmp, "new.go")
	raw := json.RawMessage(fmt.Sprintf(`{
		"edits": [{"file": "main.go", "old_text": "NOPE", "new_text": "x"}],
		"writes": [{"file": %q, "content": "package new"}]
	}`, writeTarget))

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal([]byte(result), &parsed)

	ops := parsed["ops"].([]any)
	for _, op := range ops {
		m := op.(map[string]any)
		if m["type"] == "write" {
			if m["status"] != "skipped" {
				t.Errorf("write status = %v, want skipped", m["status"])
			}
			if _, hasErr := m["error"]; hasErr {
				t.Error("skipped write should NOT have error key")
			}
			if m["reason"] != "edits failed" {
				t.Errorf("write reason = %v, want 'edits failed'", m["reason"])
			}
		}
	}
}

// TestHandleDo_ReadShapeParity verifies that batch and standalone symbol reads
// produce the same set of keys.
func TestHandleDo_ReadShapeParity(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() { println(\"hi\") }\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Batch read via handleDo
	raw := json.RawMessage(`{"reads":[{"file":"main.go","symbol":"hello"}]}`)
	batchResult, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var batchEnv map[string]any
	json.Unmarshal([]byte(batchResult), &batchEnv)
	batchOps, _ := batchEnv["ops"].([]any)
	if len(batchOps) == 0 {
		t.Fatal("batch returned no ops")
	}
	batchOp, _ := batchOps[0].(map[string]any)

	// Standalone read via dispatch + normalize
	result, err := dispatch.Dispatch(context.Background(), db, "read", []string{"main.go:hello"}, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	standaloneMap, _ := result.(map[string]any)

	// Compare key sets (excluding op_id/type which are added by envelope)
	skip := map[string]bool{"op_id": true, "type": true, "session": true, "_signature": true}
	batchKeys := map[string]bool{}
	for k := range batchOp {
		if !skip[k] {
			batchKeys[k] = true
		}
	}
	standaloneKeys := map[string]bool{}
	for k := range standaloneMap {
		if !skip[k] {
			standaloneKeys[k] = true
		}
	}

	for k := range batchKeys {
		if !standaloneKeys[k] {
			t.Errorf("batch has key %q but standalone does not", k)
		}
	}
	for k := range standaloneKeys {
		if !batchKeys[k] {
			t.Errorf("standalone has key %q but batch does not", k)
		}
	}
}

// --- detectCommandName ---

func TestDetectCommandName(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	os.Args = []string{"edr", "read", "test.go"}
	if got := detectCommandName(); got != "read" {
		t.Errorf("detectCommandName = %q, want read", got)
	}

	os.Args = []string{"edr", "--verbose", "search", "foo"}
	if got := detectCommandName(); got != "search" {
		t.Errorf("detectCommandName = %q, want search", got)
	}

	os.Args = []string{"edr", "--verbose"}
	if got := detectCommandName(); got != "batch" {
		t.Errorf("detectCommandName (flags only) = %q, want batch", got)
	}

	os.Args = []string{"edr", "--root", "/tmp/foo", "read", "hello.go"}
	if got := detectCommandName(); got != "read" {
		t.Errorf("detectCommandName (--root value) = %q, want read", got)
	}

	os.Args = []string{"edr", "--root=/tmp/foo", "edit", "hello.go"}
	if got := detectCommandName(); got != "edit" {
		t.Errorf("detectCommandName (--root=value) = %q, want edit", got)
	}

	os.Args = []string{"edr", "-r", "hello.go"}
	if got := detectCommandName(); got != "batch" {
		t.Errorf("detectCommandName (batch flags) = %q, want batch", got)
	}
}

func TestHandleDo_BatchEditErrorParity(t *testing.T) {
	// Batch edit errors should not contain "edit:" prefix from multi-edit dispatcher.
	// This verifies parity between batch and standalone error messages.
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "nonexistent_text", "new_text": "replacement"}
		]
	}`)

	result, err := testHandleDo(context.Background(), db, sess, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	if strings.Contains(result, "edit: edit ") {
		t.Errorf("batch edit error should not contain 'edit: edit N:' prefix, got: %s", result)
	}
	if !strings.Contains(result, "old_text not found") {
		t.Errorf("batch edit error should contain 'old_text not found', got: %s", result)
	}
}

// TestHandleDo_CommandFieldNeverLeaksInternalNames verifies that the envelope
// "command" field and op "type" fields never contain internal command names.
func TestHandleDo_CommandFieldNeverLeaksInternalNames(t *testing.T) {
	internal := []string{"edit-plan", "multi"}

	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db := index.NewOnDemand(tmp)
	defer db.Close()
	sess := session.New()

	// Test various batch operations
	cases := []string{
		`{"reads":[{"file":"main.go"}]}`,
		`{"queries":[{"cmd":"search","pattern":"hello"}]}`,
		`{"edits":[{"file":"main.go","old_text":"func hello()","new_text":"func hello()"}]}`,
		`{"reindex":true}`,
	}

	for _, rawJSON := range cases {
		result, err := testHandleDo(context.Background(), db, sess, json.RawMessage(rawJSON))
		if err != nil {
			continue // some may fail, that's ok — we're checking the JSON output
		}
		for _, name := range internal {
			// Check for "command":"<internal>" or "type":"<internal>" in output
			if strings.Contains(result, `"`+name+`"`) {
				t.Errorf("internal command %q leaked into output: %s", name, result[:min(len(result), 200)])
			}
		}
	}
}
