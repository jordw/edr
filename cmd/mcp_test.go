package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/session"
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
	if slim == nil {
		t.Fatal("large diffs should return slim map")
	}
	if _, ok := slim["diff"]; ok {
		t.Error("slim should not contain diff")
	}
	if _, ok := slim["old_size"]; ok {
		t.Error("slim should not contain old_size")
	}
	if slim["lines_changed"] != 25 {
		t.Errorf("lines_changed = %v, want 25", slim["lines_changed"])
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
	s, _, _ := sess.CheckContent("f.go:[1 10]", "content", false)
	if s != "new" {
		t.Errorf("got %s, want new", s)
	}
}

func TestCheckContent_Unchanged(t *testing.T) {
	sess := session.New()
	sess.StoreContent("f.go:[1 10]", "content", false)
	s, _, _ := sess.CheckContent("f.go:[1 10]", "content", false)
	if s != "unchanged" {
		t.Errorf("got %s, want unchanged", s)
	}
}

func TestCheckContent_Changed(t *testing.T) {
	sess := session.New()
	sess.StoreContent("f.go:[1 10]", "old", false)
	s, old, _ := sess.CheckContent("f.go:[1 10]", "new", false)
	if s != "changed" {
		t.Errorf("got %s, want changed", s)
	}
	if old != "old" {
		t.Errorf("old = %s", old)
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

func TestComputeTextDiff(t *testing.T) {
	old := "line1\nline2\nline3\nline4"
	new_ := "line1\nmodified\nline3\nline4"
	d := session.ComputeTextDiff(old, new_, "test.go")
	if d == "" {
		t.Fatal("expected non-empty diff")
	}
	if !strings.Contains(d, "-line2") {
		t.Error("should contain removed line")
	}
	if !strings.Contains(d, "+modified") {
		t.Error("should contain added line")
	}
	if !strings.Contains(d, "--- a/test.go") {
		t.Error("should have file header")
	}
}

func TestComputeTextDiff_Identical(t *testing.T) {
	if session.ComputeTextDiff("same", "same", "t.go") != "" {
		t.Error("identical should be empty")
	}
}

func TestComputeTextDiff_Large(t *testing.T) {
	lines := make([]string, 2500)
	for i := range lines {
		lines[i] = "line"
	}
	old := strings.Join(lines, "\n")
	lines[100] = "changed"
	new_ := strings.Join(lines, "\n")
	if session.ComputeTextDiff(old, new_, "big.go") != "" {
		t.Error("large input should bail")
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
	sess.Responses["k"] = "v"
	sess.Diffs["f.go"] = "d"
	sess.StoreContent("f.go:[1 10]", "c", false)
	sess.SeenBodies["f.go:foo"] = "h"

	sess.InvalidateForEdit("rename", []string{"old", "new"})
	if len(sess.Responses)+len(sess.Diffs)+len(sess.FileContent)+len(sess.SeenBodies) != 0 {
		t.Error("rename should clear all")
	}
}

func TestInvalidateForEdit_InitClears(t *testing.T) {
	sess := session.New()
	sess.Responses["k"] = "v"
	sess.Diffs["f.go"] = "d"

	sess.InvalidateForEdit("init", []string{})
	if len(sess.Responses)+len(sess.Diffs) != 0 {
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
	if !strings.Contains(result, "delta") {
		t.Errorf("should be delta, got: %s", result)
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

func TestRouteTool_Edr(t *testing.T) {
	raw := json.RawMessage(`{"reads":[{"file":"f.go"}]}`)
	cmd, _, _, err := routeTool("edr", raw)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != "do" {
		t.Errorf("cmd=%s, want do", cmd)
	}
}

func TestRouteTool_Unknown(t *testing.T) {
	_, _, _, err := routeTool("edr_nonexistent", json.RawMessage(`{}`))
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func TestMcpTools_Count(t *testing.T) {
	tools := mcpTools()
	if len(tools) != 1 {
		t.Errorf("expected 1 tool, got %d", len(tools))
	}
	if tools[0].Name != "edr" {
		t.Errorf("tool name = %q, want edr", tools[0].Name)
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
	mc := doQueryToMultiCmd(q)
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
	mc := doQueryToMultiCmd(q)
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
	mc := doQueryToMultiCmd(q)
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
	mc := doQueryToMultiCmd(q)
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
	mc := doQueryToMultiCmd(q)
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
	mc := doQueryToMultiCmd(q)
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

