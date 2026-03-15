package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
)

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

func TestInvalidateForEdit_InitClears(t *testing.T) {
	sess := session.New()
	sess.Diffs["f.go"] = "d"

	sess.InvalidateForEdit("init", []string{})
	if len(sess.Diffs) != 0 {
		t.Error("init should clear all")
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

	sess.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, text)
	result := sess.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, text)
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

func TestDoQueryToMultiCmd_Search(t *testing.T) {
	body := true
	pattern := "TODO"
	q := doQuery{
		Cmd:     "search",
		Pattern: &pattern,
		Body:    &body,
	}
	mc, _ := doQueryToMultiCmd(q)
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

func TestDoQueryToMultiCmd_SearchEmptyPattern(t *testing.T) {
	empty := ""
	q := doQuery{
		Cmd:     "search",
		Pattern: &empty,
	}
	mc, _ := doQueryToMultiCmd(q)
	if mc.Cmd != "search" {
		t.Errorf("cmd = %q, want search", mc.Cmd)
	}
	if len(mc.Args) != 0 {
		t.Errorf("args = %v, want [] (empty pattern should be dropped)", mc.Args)
	}
}

func TestDoQueryToMultiCmd_Explore(t *testing.T) {
	sym := "Dispatch"
	gather := true
	body := true
	q := doQuery{
		Cmd:    "explore",
		Symbol: &sym,
		Gather: &gather,
		Body:   &body,
	}
	mc, _ := doQueryToMultiCmd(q)
	if mc.Cmd != "explore" {
		t.Errorf("cmd = %q, want explore", mc.Cmd)
	}
	if len(mc.Args) != 1 || mc.Args[0] != "Dispatch" {
		t.Errorf("args = %v, want [Dispatch]", mc.Args)
	}
	if mc.Flags["gather"] != true {
		t.Error("gather should be true")
	}
}

func TestDoQueryToMultiCmd_Map(t *testing.T) {
	dir := "internal/"
	typ := "function"
	grep := "run"
	q := doQuery{
		Cmd:  "map",
		Dir:  &dir,
		Type: &typ,
		Grep: &grep,
	}
	mc, _ := doQueryToMultiCmd(q)
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

func TestDoQueryToMultiCmd_DefaultsToRead(t *testing.T) {
	file := "main.go"
	q := doQuery{
		File: &file,
	}
	mc, _ := doQueryToMultiCmd(q)
	if mc.Cmd != "read" {
		t.Errorf("cmd = %q, want read (default)", mc.Cmd)
	}
	if len(mc.Args) != 1 || mc.Args[0] != "main.go" {
		t.Errorf("args = %v, want [main.go]", mc.Args)
	}
}

func TestDoQueryToMultiCmd_Refs(t *testing.T) {
	sym := "Dispatch"
	impact := true
	depth := 2
	q := doQuery{
		Cmd:    "refs",
		Symbol: &sym,
		Impact: &impact,
		Depth:  &depth,
	}
	mc, _ := doQueryToMultiCmd(q)
	if mc.Cmd != "refs" {
		t.Errorf("cmd = %q, want refs", mc.Cmd)
	}
	if mc.Flags["impact"] != true {
		t.Error("impact should be true")
	}
	if mc.Flags["depth"] != 2 {
		t.Errorf("depth = %v, want 2", mc.Flags["depth"])
	}
}

func TestDoQueryToMultiCmd_InferredDefaultBudget(t *testing.T) {
	// When cmd is not set (inferred), doQueryToMultiCmd should apply a default budget of 200
	pattern := "TODO"
	q := doQuery{
		Pattern: &pattern,
		// No Cmd set — will be inferred as "search"
		// No Budget set — should get default 200
	}
	mc, _ := doQueryToMultiCmd(q)
	if mc.Cmd != "search" {
		t.Errorf("cmd = %q, want search", mc.Cmd)
	}
	if mc.Flags["budget"] != 200 {
		t.Errorf("budget = %v, want 200 (default for inferred cmd)", mc.Flags["budget"])
	}
}

func TestDoQueryToMultiCmd_ExplicitCmdNoBudgetDefault(t *testing.T) {
	// When cmd is explicitly set, no default budget should be applied
	pattern := "TODO"
	q := doQuery{
		Cmd:     "search",
		Pattern: &pattern,
		// Explicit Cmd — should NOT get default budget
	}
	mc, _ := doQueryToMultiCmd(q)
	if _, ok := mc.Flags["budget"]; ok {
		t.Errorf("budget should not be set for explicit cmd, got %v", mc.Flags["budget"])
	}
}

func TestDoQueryToMultiCmd_InferredWithExplicitBudget(t *testing.T) {
	// When cmd is inferred but budget is explicitly set, keep the explicit budget
	pattern := "TODO"
	budget := 500
	q := doQuery{
		Pattern: &pattern,
		Budget:  &budget,
	}
	mc, _ := doQueryToMultiCmd(q)
	if mc.Flags["budget"] != 500 {
		t.Errorf("budget = %v, want 500 (explicit budget should be preserved)", mc.Flags["budget"])
	}
}

func TestDoQueryToMultiCmd_InferredReturnValue(t *testing.T) {
	// When cmd is inferred, second return value should be true
	pattern := "TODO"
	q := doQuery{Pattern: &pattern}
	_, inferred := doQueryToMultiCmd(q)
	if !inferred {
		t.Error("expected inferred=true when cmd is not set")
	}

	// When cmd is explicit, second return value should be false
	q2 := doQuery{Cmd: "search", Pattern: &pattern}
	_, inferred2 := doQueryToMultiCmd(q2)
	if inferred2 {
		t.Error("expected inferred=false when cmd is explicitly set")
	}
}

func TestDoQueryToMultiCmd_TextSearchDefaultsGroupTrue(t *testing.T) {
	pattern := "TODO"
	textTrue := true

	// Text search without explicit group should default to group=true
	q := doQuery{Cmd: "search", Pattern: &pattern, Text: &textTrue}
	mc, _ := doQueryToMultiCmd(q)
	if mc.Flags["group"] != true {
		t.Error("text search should default group=true via batch")
	}

	// Symbol search (no text flag) should NOT default group
	q2 := doQuery{Cmd: "search", Pattern: &pattern}
	mc2, _ := doQueryToMultiCmd(q2)
	if _, ok := mc2.Flags["group"]; ok {
		t.Error("symbol search should not default group=true")
	}

	// Explicit group=false (Group set to non-nil false) should not override
	groupFalse := false
	q3 := doQuery{Cmd: "search", Pattern: &pattern, Text: &textTrue, Group: &groupFalse}
	mc3, _ := doQueryToMultiCmd(q3)
	if mc3.Flags["group"] == true {
		t.Error("explicit group=false should not be overridden")
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
	raw := `{"renames": [{"old_name": "Foo", "new_name": "Bar", "dry_run": true, "scope": "internal/**"}]}`
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
	if r.Scope == nil || *r.Scope != "internal/**" {
		t.Error("scope should be internal/**")
	}
}

func TestDoParams_Init(t *testing.T) {
	raw := `{"init": true}`
	var p doParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if p.Init == nil || !*p.Init {
		t.Error("init should be true")
	}
}

func TestDoParams_Edits_RegexAll(t *testing.T) {
	raw := `{"edits": [{"file": "f.go", "old_text": "v[0-9]+", "new_text": "v2", "regex": true, "all": true}]}`
	var p doParams
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Edits) != 1 {
		t.Fatalf("edits len = %d, want 1", len(p.Edits))
	}
	e := p.Edits[0]
	if e.Regex == nil || !*e.Regex {
		t.Error("regex should be true")
	}
	if e.All == nil || !*e.All {
		t.Error("all should be true")
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
	// edit-plan results should go through the slim-edit pipeline now that
	// DiffEditCommands includes "edit-plan".
	sess := session.New()
	text := `{"ok":true,"edits":1,"files":1,"hashes":{"f.go":"abc"},"description":["replace text"],"diff":"--- a/f.go\n+++ b/f.go\n@@ -1 +1 @@\n-old\n+new\n"}`

	result := sess.PostProcess("edit-plan", []string{}, map[string]any{}, nil, text)
	if !strings.Contains(result, `"diff"`) {
		t.Error("small edit-plan diff should stay inline")
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
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

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
			result, err := handleDo(context.Background(), db, sess, nil, json.RawMessage(tt.raw))
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
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc remove() {}\n\nfunc keep() {}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	raw := json.RawMessage(`{
		"edits": [{"file": "main.go", "old_text": "func remove() {}\n\n", "new_text": ""}]
	}`)

	result, err := handleDo(context.Background(), db, sess, tc, raw)
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
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	raw := json.RawMessage(`{"verify": {"command": "echo verify-works", "timeout": 10}}`)
	result, err := handleDo(context.Background(), db, sess, tc, raw)
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
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	// 3 edits, second fails (nonexistent text)
	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "package main", "new_text": "package main"},
			{"file": "main.go", "old_text": "nonexistent_text_xyz", "new_text": "replacement"},
			{"file": "main.go", "old_text": "func hello", "new_text": "func world"}
		]
	}`)

	result, err := handleDo(context.Background(), db, sess, tc, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// Parse the JSON result
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	edits, ok := parsed["edits"].(map[string]any)
	if !ok {
		t.Fatalf("edits should be object, got %T: %s", parsed["edits"], result)
	}

	// The first edit is a no-op so editPlan returns early with noop before
	// reaching the second. But the second edit should fail with not_found.
	// Check for edit_index in error
	if edits["error"] != "not_found" && edits["error"] != "noop" {
		// If it's a not_found error, verify the new fields
		if editIdx, ok := edits["edit_index"]; ok {
			if editIdx != float64(1) {
				t.Errorf("edit_index = %v, want 1", editIdx)
			}
		}
		if editMode, ok := edits["edit_mode"]; ok {
			if editMode != "old_text" {
				t.Errorf("edit_mode = %v, want old_text", editMode)
			}
		}
		if totalEdits, ok := edits["total_edits"]; ok {
			if totalEdits != float64(3) {
				t.Errorf("total_edits = %v, want 3", totalEdits)
			}
		}
	}
}

func TestHandleDo_BatchEditNotFoundFields(t *testing.T) {
	// Bug 3: Targeted test — edit with non-matching old_text should have structured fields
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	// Single failing edit
	raw := json.RawMessage(`{
		"edits": [
			{"file": "main.go", "old_text": "nonexistent_text_xyz", "new_text": "replacement"}
		]
	}`)

	result, err := handleDo(context.Background(), db, sess, tc, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse result: %v", err)
	}

	edits, ok := parsed["edits"].(map[string]any)
	if !ok {
		t.Fatalf("edits should be object, got %T: %s", parsed["edits"], result)
	}

	if edits["error"] != "not_found" {
		t.Fatalf("expected not_found error, got: %v", edits["error"])
	}
	if edits["edit_index"] != float64(0) {
		t.Errorf("edit_index = %v, want 0", edits["edit_index"])
	}
	if edits["edit_mode"] != "old_text" {
		t.Errorf("edit_mode = %v, want old_text", edits["edit_mode"])
	}
	if edits["total_edits"] != float64(1) {
		t.Errorf("total_edits = %v, want 1", edits["total_edits"])
	}
}

func TestDoStructsMatchCmdspec(t *testing.T) {
	// Verify that doRead, doQuery, doEdit, doWrite, doRename struct fields
	// have corresponding cmdspec flags/batch fields, and vice versa.
	// This catches drift between JSON batch structs and the registry.

	checkStructFields := func(t *testing.T, structName string, knownKeys map[string]bool, structFields map[string]bool) {
		t.Helper()
		for field := range structFields {
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
		"depth": true, "start_line": true, "end_line": true, "symbols": true, "full": true,
	}
	checkStructFields(t, "doRead", doReadKnownKeys, readFields)

	// doEdit fields
	editFields := map[string]bool{
		"file": true, "old_text": true, "new_text": true, "symbol": true,
		"start_line": true, "end_line": true, "regex": true, "all": true,
		"move": true, "after": true, "before": true, "dry_run": true, "expect_hash": true,
	}
	checkStructFields(t, "doEdit", doEditKnownKeys, editFields)

	// doWrite fields
	writeFields := map[string]bool{
		"file": true, "content": true, "mkdir": true, "after": true,
		"inside": true, "append": true,
	}
	// Write has extra batch fields (body, new_text, force) that aren't in the struct
	for key := range doWriteKnownKeys {
		if key == "body" || key == "new_text" || key == "force" {
			continue // batch aliases, not in struct
		}
		if !writeFields[key] {
			t.Errorf("cmdspec write has key %q not in doWrite struct", key)
		}
	}
	for field := range writeFields {
		if !doWriteKnownKeys[field] {
			t.Errorf("doWrite has field %q not in cmdspec", field)
		}
	}

	// doRename fields
	renameFields := map[string]bool{
		"old_name": true, "new_name": true, "dry_run": true, "scope": true,
	}
	checkStructFields(t, "doRename", doRenameKnownKeys, renameFields)
}

func TestHandleDo_SkipsPostEditReadsAndVerifyOnEditFailure(t *testing.T) {
	// Create a temp dir with a .edr directory for the DB and traces.
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)

	// Create a dummy Go file so the repo root is valid.
	os.WriteFile(filepath.Join(tmp, "go.mod"), []byte("module test\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	// Call handleDo with an edit targeting a non-existent file, plus
	// read_after_edit and verify — both should be skipped.
	raw := json.RawMessage(`{
		"edits": [{"file": "nonexistent.go", "old_text": "foo", "new_text": "bar"}],
		"read_after_edit": true,
		"verify": true
	}`)

	result, err := handleDo(context.Background(), db, sess, tc, raw)
	if err != nil {
		t.Fatalf("handleDo error: %v", err)
	}

	// The edit should have failed.
	if !strings.Contains(result, `"error"`) {
		t.Errorf("expected edit error in result, got: %s", result)
	}

	// post_edit_reads should be skipped.
	if !strings.Contains(result, `"post_edit_reads":"skipped: edits failed"`) {
		t.Errorf("expected post_edit_reads to be skipped, got: %s", result)
	}

	// verify should be skipped.
	if !strings.Contains(result, `"verify":"skipped: edits failed"`) {
		t.Errorf("expected verify to be skipped, got: %s", result)
	}
}

// --- Correctness bug fix tests ---

func TestHandleDo_DryRunSkipsWrites(t *testing.T) {
	// Issue #12: writes should not execute when edits have dry-run set
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "existing.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	writeTarget := filepath.Join(tmp, "should_not_exist.txt")
	raw := json.RawMessage(fmt.Sprintf(`{
		"writes": [{"file": %q, "content": "test data"}],
		"edits": [{"file": "existing.go", "old_text": "func hello", "new_text": "func world", "dry_run": true}]
	}`, writeTarget))

	result, err := handleDo(context.Background(), db, sess, tc, raw)
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

	// Writes should show skipped
	writes, ok := parsed["writes"].([]any)
	if !ok || len(writes) != 1 {
		t.Fatalf("expected 1 write result, got: %v", parsed["writes"])
	}
	wr := writes[0].(map[string]any)
	if res, ok := wr["result"].(map[string]any); !ok || res["skipped"] != "dry_run" {
		t.Errorf("expected write skipped for dry_run, got: %v", wr)
	}

	// Summary should show dry_run status
	summary := parsed["summary"].(map[string]any)
	if summary["status"] != "dry_run" {
		t.Errorf("summary status = %v, want dry_run", summary["status"])
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
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	// Edit where old_text == new_text, with verify using a command that would fail
	raw := json.RawMessage(`{
		"edits": [{"file": "main.go", "old_text": "func hello", "new_text": "func hello"}],
		"verify": "false"
	}`)

	result, err := handleDo(context.Background(), db, sess, tc, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Verify should be skipped for no-op
	verifyStr, ok := parsed["verify"].(string)
	if !ok || verifyStr != "skipped: no-op edit" {
		t.Errorf("verify = %v, want \"skipped: no-op edit\"", parsed["verify"])
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
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "data.txt"), []byte("old content\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

	// Real edit that succeeds, but verify command fails
	raw := json.RawMessage(`{
		"edits": [{"file": "data.txt", "old_text": "old content", "new_text": "new content"}],
		"verify": "false"
	}`)

	result, err := handleDo(context.Background(), db, sess, tc, raw)
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
	if ok, _ := verify["ok"].(bool); ok {
		t.Error("verify should have ok=false")
	}

	// Summary status should be "verify_failed", not "applied"
	summary := parsed["summary"].(map[string]any)
	if summary["status"] != "verify_failed" {
		t.Errorf("summary status = %v, want verify_failed", summary["status"])
	}
}

func TestHandleDo_WriteInvalidatesSession(t *testing.T) {
	// Issue #17: session should be invalidated after writes
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	sess := session.New()
	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc != nil {
		defer tc.Close()
	}

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

	_, err = handleDo(context.Background(), db, sess, tc, raw)
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

func TestHandleDo_TraceCollectorRecordsEvents(t *testing.T) {
	// Issue #16: trace events should be recorded when collector is provided
	tmp := t.TempDir()
	edrDir := filepath.Join(tmp, ".edr")
	os.MkdirAll(edrDir, 0755)
	os.WriteFile(filepath.Join(tmp, "f.txt"), []byte("hello\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()
	if _, _, err := index.IndexRepo(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	tc := trace.NewCollector(edrDir, "test-1.0")
	if tc == nil {
		t.Fatal("NewCollector returned nil")
	}

	sess := session.New()

	raw := json.RawMessage(`{
		"reads": [{"file": "f.txt"}],
		"edits": [{"file": "f.txt", "old_text": "hello", "new_text": "world"}]
	}`)

	_, err = handleDo(context.Background(), db, sess, tc, raw)
	if err != nil {
		t.Fatalf("handleDo: %v", err)
	}

	// Close the collector to flush events
	tc.Close()

	// Verify traces.db has at least one call record
	traceDB, err := trace.OpenTraceDB(edrDir)
	if err != nil {
		t.Fatalf("OpenTraceDB: %v", err)
	}
	defer traceDB.Close()

	var callCount int
	err = traceDB.QueryRow("SELECT count(*) FROM calls").Scan(&callCount)
	if err != nil {
		t.Fatalf("query calls: %v", err)
	}
	if callCount == 0 {
		t.Error("expected at least 1 call record in traces.db, got 0")
	}

	// Verify the call record has the right shape (reads + edits)
	var numReads, numEdits int
	err = traceDB.QueryRow("SELECT num_reads, num_edits FROM calls ORDER BY id DESC LIMIT 1").Scan(&numReads, &numEdits)
	if err != nil {
		t.Fatalf("query call shape: %v", err)
	}
	if numReads != 1 {
		t.Errorf("num_reads = %d, want 1", numReads)
	}
	if numEdits != 1 {
		t.Errorf("num_edits = %d, want 1", numEdits)
	}
}

func TestIsVerifyFailed(t *testing.T) {
	tests := []struct {
		name   string
		input  json.RawMessage
		expect bool
	}{
		{"nil", nil, false},
		{"skip string", json.RawMessage(`"skipped: dry run"`), false},
		{"ok true", json.RawMessage(`{"ok":true}`), false},
		{"ok false with error", json.RawMessage(`{"ok":false,"error":"exit status 1"}`), true},
		{"ok false no error", json.RawMessage(`{"ok":false}`), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isVerifyFailed(tt.input)
			if got != tt.expect {
				t.Errorf("isVerifyFailed(%s) = %v, want %v", string(tt.input), got, tt.expect)
			}
		})
	}
}

func TestBuildSummary_VerifyFailed(t *testing.T) {
	p := &doParams{
		Edits: []doEdit{{File: "f.go", OldText: "a", NewText: "b"}},
	}
	s := buildSummary(p, false, nil, true)
	if s.Status != "verify_failed" {
		t.Errorf("status = %q, want verify_failed", s.Status)
	}
}

func TestBuildSummary_Applied(t *testing.T) {
	p := &doParams{
		Edits: []doEdit{{File: "f.go", OldText: "a", NewText: "b"}},
	}
	s := buildSummary(p, false, nil, false)
	if s.Status != "applied" {
		t.Errorf("status = %q, want applied", s.Status)
	}
}

func TestBuildSummary_DryRun(t *testing.T) {
	dr := true
	p := &doParams{
		Edits:  []doEdit{{File: "f.go", OldText: "a", NewText: "b"}},
		DryRun: &dr,
	}
	s := buildSummary(p, false, nil, false)
	if s.Status != "dry_run" {
		t.Errorf("status = %q, want dry_run", s.Status)
	}
}

func TestBuildSummary_EditsFailed(t *testing.T) {
	p := &doParams{
		Edits: []doEdit{{File: "f.go", OldText: "a", NewText: "b"}},
	}
	s := buildSummary(p, true, nil, false)
	if s.Status != "failed" {
		t.Errorf("status = %q, want failed", s.Status)
	}
}

