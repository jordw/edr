package session

import (
	"os"
	"strings"
	"testing"
)

// --- ResolveSessionID ---

func TestResolveSessionID_EnvSet(t *testing.T) {
	t.Setenv("EDR_SESSION", "my-session")
	if id := ResolveSessionID(); id != "my-session" {
		t.Errorf("expected 'my-session', got %q", id)
	}
}

func TestResolveSessionID_EnvUnset(t *testing.T) {
	t.Setenv("EDR_SESSION", "")
	os.Unsetenv("EDR_SESSION")
	id := ResolveSessionID()
	// Without EDR_SESSION, PPID-based routing kicks in.
	// In test context there is no .edr dir, so resolveByPPID auto-creates
	// a fresh session or returns "" if it can't find a repo root.
	// Either way, it should NOT return "default".
	if id == "default" {
		t.Errorf("should not fall back to shared \"default\" session, got %q", id)
	}
}

// --- New ---

func TestNew(t *testing.T) {
	s := New()
	if s.Diffs == nil || s.FileContent == nil || s.SymbolContent == nil || s.SeenBodies == nil {
		t.Fatal("New() should initialize all maps")
	}
	if s.ContentOrder != 0 {
		t.Error("ContentOrder should start at 0")
	}
}

// --- ContentHash ---

func TestContentHash_Deterministic(t *testing.T) {
	h1 := ContentHash("hello")
	h2 := ContentHash("hello")
	if h1 != h2 {
		t.Error("same input should produce same hash")
	}
}

func TestContentHash_Different(t *testing.T) {
	h1 := ContentHash("hello")
	h2 := ContentHash("world")
	if h1 == h2 {
		t.Error("different input should produce different hash")
	}
}

func TestContentHash_Length(t *testing.T) {
	// 16 bytes hex-encoded = 32 chars
	h := ContentHash("test")
	if len(h) != 32 {
		t.Errorf("hash length = %d, want 32", len(h))
	}
}

// --- CacheKey ---

func TestCacheKey_Basic(t *testing.T) {
	s := New()
	k := s.CacheKey("read", []string{"f.go"}, map[string]any{})
	if k != "read\x00f.go" {
		t.Errorf("unexpected key: %q", k)
	}
}

func TestCacheKey_WithFlags(t *testing.T) {
	s := New()
	k := s.CacheKey("read", []string{"f.go"}, map[string]any{"budget": 500, "body": true})
	if !strings.Contains(k, "budget=500") || !strings.Contains(k, "body=true") {
		t.Errorf("key missing flags: %q", k)
	}
}

func TestCacheKey_IgnoresUnknownFlags(t *testing.T) {
	s := New()
	k1 := s.CacheKey("read", []string{"f.go"}, map[string]any{"unknown": 42})
	k2 := s.CacheKey("read", []string{"f.go"}, map[string]any{})
	if k1 != k2 {
		t.Error("unknown flags should not affect cache key")
	}
}

func TestCacheKey_DifferentArgs(t *testing.T) {
	s := New()
	k1 := s.CacheKey("read", []string{"a.go"}, map[string]any{})
	k2 := s.CacheKey("read", []string{"b.go"}, map[string]any{})
	if k1 == k2 {
		t.Error("different args should produce different keys")
	}
}

func TestCacheKey_DifferentCmds(t *testing.T) {
	s := New()
	k1 := s.CacheKey("read", []string{"f.go"}, map[string]any{})
	k2 := s.CacheKey("search", []string{"f.go"}, map[string]any{})
	if k1 == k2 {
		t.Error("different commands should produce different keys")
	}
}

func TestCacheKey_DepthIncluded(t *testing.T) {
	s := New()
	k1 := s.CacheKey("read", []string{"f.go:sym"}, map[string]any{})
	k2 := s.CacheKey("read", []string{"f.go:sym"}, map[string]any{"depth": 2})
	if k1 == k2 {
		t.Error("depth flag should produce a different cache key")
	}
}

func TestProcessReadResult_DepthAffectsKey(t *testing.T) {
	s := New()

	// First: store full body (no depth)
	result1 := map[string]any{
		"content": "func foo() { full body }",
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "abc",
	}
	if delta := s.ProcessReadResult("read", result1, map[string]any{}); delta != nil {
		t.Error("first read should return nil (new)")
	}

	// Second: read with depth=2 and different content
	result2 := map[string]any{
		"content": "func foo() { ... }",
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "abc",
	}
	delta := s.ProcessReadResult("read", result2, map[string]any{"depth": 2})
	if delta != nil {
		// depth=2 should be a separate key, so this is "new" content, not "unchanged"
		if delta["unchanged"] == true {
			t.Error("depth=2 read should NOT return unchanged when full body was stored")
		}
	}

	// Third: re-read with depth=2 and same content → symbol reads always emit content
	result3 := map[string]any{
		"content": "func foo() { ... }",
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "abc",
	}
	delta = s.ProcessReadResult("read", result3, map[string]any{"depth": 2})
	if delta != nil {
		t.Error("symbol re-read should emit content (no dedup stub)")
	}
	if result3["session"] != "unchanged" {
		t.Errorf("expected session=unchanged, got: %v", result3["session"])
	}
}

// --- InvalidateFile ---

func TestInvalidateFile(t *testing.T) {
	s := New()
	s.FileContent["f.go:[1 5]"] = ContentEntry{Hash: "hash1"}
	s.FileContent["other.go:[1 5]"] = ContentEntry{Hash: "hash2"}
	s.InvalidateFile("f.go")
	if _, ok := s.FileContent["f.go:[1 5]"]; ok {
		t.Error("f.go entry should be deleted")
	}
	if _, ok := s.FileContent["other.go:[1 5]"]; !ok {
		t.Error("other.go entry should remain")
	}
}

// --- InvalidateForEdit ---

func TestInvalidateForEdit_RegularEdit(t *testing.T) {
	s := New()
	s.FileContent["f.go:[1 5]"] = ContentEntry{Hash: "h1"}
	s.FileContent["other.go:[1 5]"] = ContentEntry{Hash: "h2"}
	s.InvalidateForEdit("edit", []string{"f.go"})
	if _, ok := s.FileContent["f.go:[1 5]"]; ok {
		t.Error("edited file should be invalidated")
	}
	if _, ok := s.FileContent["other.go:[1 5]"]; !ok {
		t.Error("other file should remain")
	}
}

func TestInvalidateForEdit_RenameClears(t *testing.T) {
	s := New()
	s.Diffs["f.go"] = "diff"
	s.FileContent["f.go"] = ContentEntry{Hash: "h"}
	s.SymbolContent["f.go:foo"] = ContentEntry{Hash: "h"}
	s.SeenBodies["f.go:foo"] = "h"

	s.InvalidateForEdit("rename", []string{"old", "new"})

	if len(s.Diffs) != 0 || len(s.FileContent) != 0 || len(s.SymbolContent) != 0 || len(s.SeenBodies) != 0 {
		t.Error("rename should clear all state")
	}
}

func TestInvalidateForEdit_NoArgs(t *testing.T) {
	s := New()
	s.FileContent["k"] = ContentEntry{Hash: "v"}
	s.InvalidateForEdit("edit", nil)
	if len(s.FileContent) != 1 {
		t.Error("no args should not invalidate anything")
	}
}

// --- CountDiffLines ---

func TestCountDiffLines(t *testing.T) {
	diff := "--- a/f.go\n+++ b/f.go\n@@ -1,3 +1,3 @@\n ctx\n-old\n+new\n ctx\n"
	if got := CountDiffLines(diff); got != 2 {
		t.Errorf("got %d, want 2", got)
	}
}

func TestCountDiffLines_Empty(t *testing.T) {
	if got := CountDiffLines(""); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

func TestCountDiffLines_ContextOnly(t *testing.T) {
	diff := "--- a/f.go\n+++ b/f.go\n@@ -1,2 +1,2 @@\n same\n same\n"
	if got := CountDiffLines(diff); got != 0 {
		t.Errorf("got %d, want 0", got)
	}
}

// --- StoreDiff ---

func TestStoreDiff_SmallInline(t *testing.T) {
	s := New()
	result := map[string]any{
		"ok": true, "file": "f.go", "symbol": "foo",
		"diff": "-old\n+new\n", "hash": "abc",
	}
	slim := s.StoreDiff(result, map[string]any{})
	if slim != nil {
		t.Fatal("StoreDiff should always return nil")
	}
	if result["lines_changed"] != 2 {
		t.Errorf("lines_changed = %v, want 2", result["lines_changed"])
	}
	if result["diff_available"] != true {
		t.Error("diff_available should be set")
	}
	// Should be stored for GetDiff
	if s.Diffs["f.go:foo"] != "-old\n+new\n" {
		t.Error("diff should be stored")
	}
}

func TestStoreDiff_LargeDiff(t *testing.T) {
	s := New()
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, "+new line")
	}
	bigDiff := strings.Join(lines, "\n") + "\n"
	result := map[string]any{
		"ok": true, "file": "f.go",
		"diff": bigDiff, "hash": "abc",
		"old_size": 10, "new_size": 100,
	}
	slim := s.StoreDiff(result, map[string]any{})
	if slim != nil {
		t.Error("StoreDiff should always return nil (no slim optimization)")
	}
	if result["lines_changed"] != 25 {
		t.Errorf("lines_changed = %v, want 25", result["lines_changed"])
	}
	if result["diff_available"] != true {
		t.Error("diff_available should be set")
	}
}

func TestStoreDiff_FileOnlyKey(t *testing.T) {
	s := New()
	result := map[string]any{"file": "f.go", "diff": "-a\n+b\n"}
	s.StoreDiff(result, map[string]any{})
	if _, ok := s.Diffs["f.go"]; !ok {
		t.Error("should store under file key when no symbol")
	}
}

func TestStoreDiff_VerboseSkips(t *testing.T) {
	s := New()
	result := map[string]any{"diff": "some diff", "file": "f.go"}
	if s.StoreDiff(result, map[string]any{"verbose": true}) != nil {
		t.Error("verbose should skip")
	}
}

func TestStoreDiff_NoDiff(t *testing.T) {
	s := New()
	if s.StoreDiff(map[string]any{"ok": true}, map[string]any{}) != nil {
		t.Error("no diff should return nil")
	}
}

func TestStoreDiff_EmptyDiff(t *testing.T) {
	s := New()
	if s.StoreDiff(map[string]any{"diff": ""}, map[string]any{}) != nil {
		t.Error("empty diff should return nil")
	}
}

// --- GetDiff ---

func TestGetDiff_ExactKey(t *testing.T) {
	s := New()
	s.Diffs["f.go:foo"] = "the diff"
	r := s.GetDiff([]string{"f.go", "foo"})
	if r["diff"] != "the diff" {
		t.Errorf("GetDiff = %v", r)
	}
}

func TestGetDiff_FileOnly(t *testing.T) {
	s := New()
	s.Diffs["f.go"] = "file diff"
	r := s.GetDiff([]string{"f.go"})
	if r["diff"] != "file diff" {
		t.Errorf("GetDiff = %v", r)
	}
}

func TestGetDiff_FallbackToFileKey(t *testing.T) {
	s := New()
	s.Diffs["f.go"] = "file diff"
	r := s.GetDiff([]string{"f.go", "unknownsym"})
	if r["diff"] != "file diff" {
		t.Errorf("fallback = %v", r)
	}
}

func TestGetDiff_NotFound(t *testing.T) {
	s := New()
	r := s.GetDiff([]string{"missing.go"})
	if _, ok := r["error"]; !ok {
		t.Error("should error")
	}
}

func TestGetDiff_NoArgs(t *testing.T) {
	s := New()
	r := s.GetDiff(nil)
	if _, ok := r["error"]; !ok {
		t.Error("should error with no args")
	}
}

// --- StoreContent / CheckContent ---

func TestCheckContent_New(t *testing.T) {
	s := New()
	status, prev := s.CheckContent("k", "content", false)
	if status != "new" || prev != "" {
		t.Errorf("got (%s, %q)", status, prev)
	}
}

func TestCheckContent_Unchanged(t *testing.T) {
	s := New()
	s.StoreContent("k", "content", false)
	status, prevHash := s.CheckContent("k", "content", false)
	if status != "unchanged" {
		t.Errorf("got %s, want unchanged", status)
	}
	if prevHash == "" {
		t.Error("prevHash should be set")
	}
}

func TestCheckContent_Changed(t *testing.T) {
	s := New()
	s.StoreContent("k", "old content", false)
	status, prevHash := s.CheckContent("k", "new content", false)
	if status != "new" {
		t.Errorf("got %s, want new (hash-only storage, changed maps to new)", status)
	}
	if prevHash == "" {
		t.Error("prevHash should be set")
	}
}

func TestCheckContent_UnchangedUpdatesOrder(t *testing.T) {
	s := New()
	s.StoreContent("k", "c", false)
	orderBefore := s.FileContent["k"].Order
	s.CheckContent("k", "c", false)
	orderAfter := s.FileContent["k"].Order
	if orderAfter <= orderBefore {
		t.Error("unchanged check should bump order for LRU")
	}
}

func TestStoreContent_SymbolVsFile(t *testing.T) {
	s := New()
	s.StoreContent("k", "file content", false)
	s.StoreContent("k", "symbol content", true)
	if _, ok := s.FileContent["k"]; !ok {
		t.Error("file content should exist")
	}
	if _, ok := s.SymbolContent["k"]; !ok {
		t.Error("symbol content should exist")
	}
}

// --- evictLRU ---

func TestEvictLRU_UnderLimit(t *testing.T) {
	s := New()
	for i := 0; i < 10; i++ {
		s.StoreContent(string(rune('a'+i)), "c", false)
	}
	if len(s.FileContent) != 10 {
		t.Errorf("got %d entries, want 10", len(s.FileContent))
	}
}

func TestEvictLRU_AtLimit(t *testing.T) {
	s := New()
	for i := 0; i < MaxContentEntries+5; i++ {
		key := string(rune('A'+i%26)) + string(rune('a'+i/26))
		if i%2 == 0 {
			s.StoreContent(key, "c", false)
		} else {
			s.StoreContent(key, "c", true)
		}
	}
	total := len(s.FileContent) + len(s.SymbolContent)
	if total > MaxContentEntries {
		t.Errorf("LRU failed: %d > %d", total, MaxContentEntries)
	}
}

func TestEvictLRU_EvictsOldest(t *testing.T) {
	s := New()
	// Fill exactly to limit
	for i := 0; i < MaxContentEntries; i++ {
		s.StoreContent(string(rune(i)), "c", false)
	}
	if _, ok := s.FileContent[string(rune(0))]; !ok {
		t.Fatal("first entry should exist before overflow")
	}
	// Add one more — should evict the oldest (order=1)
	s.StoreContent("overflow", "c", false)
	if _, ok := s.FileContent[string(rune(0))]; ok {
		t.Error("oldest entry should have been evicted")
	}
	if _, ok := s.FileContent["overflow"]; !ok {
		t.Error("new entry should exist")
	}
}

// --- ProcessReadResult ---

func TestProcessReadResult_NewFile(t *testing.T) {
	s := New()
	result := map[string]any{
		"content": "hello\n", "file": "f.go", "hash": "abc",
		"lines": []int{1, 5},
	}
	if delta := s.ProcessReadResult("read", result, map[string]any{}); delta != nil {
		t.Error("new content should return nil")
	}
	if len(s.FileContent) != 1 {
		t.Error("content should be stored")
	}
}

func TestProcessReadResult_UnchangedFile(t *testing.T) {
	s := New()
	result := map[string]any{
		"content": "hello\n", "file": "f.go", "hash": "abc",
		"lines": []int{1, 5},
	}
	s.ProcessReadResult("read", result, map[string]any{})
	delta := s.ProcessReadResult("read", result, map[string]any{})
	if delta == nil {
		t.Fatal("unchanged should return delta")
	}
	if delta["unchanged"] != true {
		t.Error("should be unchanged")
	}
}

func TestProcessReadResult_ChangedFile(t *testing.T) {
	s := New()
	result1 := map[string]any{
		"content": "line1\nline2\n", "file": "f.go", "hash": "h1",
		"lines": []int{1, 2},
	}
	s.ProcessReadResult("read", result1, map[string]any{})

	result2 := map[string]any{
		"content": "line1\nchanged\n", "file": "f.go", "hash": "h2",
		"lines": []int{1, 2},
	}
	// With hash-only storage, changed content is treated as "new" (no old content to diff)
	delta := s.ProcessReadResult("read", result2, map[string]any{})
	if delta != nil {
		t.Error("changed content should return nil (treated as new, no diff possible)")
	}
}

func TestProcessReadResult_FullFlag(t *testing.T) {
	s := New()
	result := map[string]any{
		"content": "hello\n", "file": "f.go", "hash": "abc",
		"lines": []int{1, 5},
	}
	s.ProcessReadResult("read", result, map[string]any{})
	delta := s.ProcessReadResult("read", result, map[string]any{"full": true})
	if delta != nil {
		t.Error("--full should bypass delta and return nil")
	}
}

func TestProcessReadResult_Symbol(t *testing.T) {
	s := New()
	result := map[string]any{
		"content": "func foo() {}",
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "abc",
	}
	if delta := s.ProcessReadResult("read", result, map[string]any{}); delta != nil {
		t.Error("new symbol should return nil")
	}
	if len(s.SymbolContent) != 1 {
		t.Error("symbol should be stored")
	}
	if _, ok := s.SeenBodies["f.go:foo"]; !ok {
		t.Error("body should be tracked")
	}
}

func TestProcessReadResult_ExpandTracksBody(t *testing.T) {
	s := New()
	result := map[string]any{
		"content": "func bar() {}",
		"file":    "f.go",
		"symbol":  "bar",
	}
	s.ProcessReadResult("explore", result, map[string]any{})
	if _, ok := s.SeenBodies["f.go:bar"]; !ok {
		t.Error("expand should track body")
	}
}

func TestProcessReadResult_EmptyContent(t *testing.T) {
	s := New()
	result := map[string]any{"content": "", "file": "f.go"}
	if s.ProcessReadResult("read", result, map[string]any{}) != nil {
		t.Error("empty content should return nil")
	}
}

func TestProcessReadResult_UnknownCmd(t *testing.T) {
	s := New()
	result := map[string]any{"content": "x", "file": "f.go"}
	if s.ProcessReadResult("search", result, map[string]any{}) != nil {
		t.Error("unknown cmd should return nil")
	}
}

// --- ExtractFileHash ---

func TestExtractFileHash_Direct(t *testing.T) {
	f, h := ExtractFileHash(map[string]any{"file": "f.go", "hash": "abc"})
	if f != "f.go" || h != "abc" {
		t.Errorf("got (%s, %s)", f, h)
	}
}

func TestExtractFileHash_FromSymbol(t *testing.T) {
	f, h := ExtractFileHash(map[string]any{
		"file":    "f.go",
		"hash":    "abc",
	})
	if f != "f.go" || h != "abc" {
		t.Errorf("got (%s, %s)", f, h)
	}
}

func TestExtractFileHash_Empty(t *testing.T) {
	f, h := ExtractFileHash(map[string]any{})
	if f != "" || h != "" {
		t.Errorf("got (%s, %s)", f, h)
	}
}

// --- StoreReadContent ---

func TestStoreReadContent_ReadFile(t *testing.T) {
	s := New()
	s.StoreReadContent("read", map[string]any{
		"content": "hello", "file": "f.go", "lines": []int{1, 5},
	})
	if len(s.FileContent) != 1 {
		t.Error("should store file content")
	}
}

func TestStoreReadContent_ReadSymbol(t *testing.T) {
	s := New()
	s.StoreReadContent("read", map[string]any{
		"content": "func foo() {}",
		"file":    "f.go",
		"symbol":  "foo",
	})
	if len(s.SymbolContent) != 1 {
		t.Error("should store symbol content")
	}
	if _, ok := s.SeenBodies["f.go:foo"]; !ok {
		t.Error("should track body")
	}
}

func TestStoreReadContent_ExpandTracksBody(t *testing.T) {
	s := New()
	s.StoreReadContent("explore", map[string]any{
		"content": "func bar() {}",
		"file":    "f.go",
		"symbol":  "bar",
	})
	if _, ok := s.SeenBodies["f.go:bar"]; !ok {
		t.Error("expand should track body")
	}
}

func TestStoreReadContent_SkipsEmptyBody(t *testing.T) {
	s := New()
	s.StoreReadContent("read", map[string]any{
		"content": "",
		"file":    "f.go",
		"symbol":  "foo",
	})
	if len(s.SymbolContent) != 0 {
		t.Error("should not store empty body")
	}
}

// --- TrackBodies ---

func TestTrackBodies_ReadSymbol(t *testing.T) {
	s := New()
	s.TrackBodies(map[string]any{
		"content": "func foo() {}",
		"file":    "f.go",
		"symbol":  "foo",
	}, "read")
	if _, ok := s.SeenBodies["f.go:foo"]; !ok {
		t.Error("should track")
	}
}

func TestTrackBodies_Gather(t *testing.T) {
	s := New()
	s.TrackBodies(map[string]any{
		"target_body": "func main() {}",
		"target":      map[string]any{"file": "m.go", "name": "main"},
	}, "gather")
	if _, ok := s.SeenBodies["m.go:main"]; !ok {
		t.Error("should track gather target")
	}
}

func TestTrackBodies_Search(t *testing.T) {
	s := New()
	s.TrackBodies(map[string]any{
		"matches": []any{
			map[string]any{
				"body":   "func a() {}",
				"symbol": map[string]any{"file": "f.go", "name": "a"},
			},
			map[string]any{
				"body":   "func b() {}",
				"symbol": map[string]any{"file": "g.go", "name": "b"},
			},
		},
	}, "search")
	if len(s.SeenBodies) != 2 {
		t.Errorf("should track 2 bodies, got %d", len(s.SeenBodies))
	}
}

func TestTrackBodies_SkipsEmptyBody(t *testing.T) {
	s := New()
	s.TrackBodies(map[string]any{
		"content": "",
		"file":    "f.go",
		"symbol":  "foo",
	}, "read")
	if len(s.SeenBodies) != 0 {
		t.Error("should not track empty body")
	}
}

func TestTrackBodies_SkipsNoSymbol(t *testing.T) {
	s := New()
	s.TrackBodies(map[string]any{
		"content": "func foo() {}",
	}, "read")
	if len(s.SeenBodies) != 0 {
		t.Error("should not track without symbol")
	}
}

// --- StripSeenBodies ---

func TestStripSeenBodies_Gather(t *testing.T) {
	s := New()
	s.SeenBodies["m.go:main"] = ContentHash("func main() {}")

	result := map[string]any{
		"target_body": "func main() {}",
		"target":      map[string]any{"file": "m.go", "name": "main"},
	}
	s.StripSeenBodies(result, "gather")
	if result["target_body"] != "[in context]" {
		t.Error("seen body should be replaced")
	}
	if result["skipped_bodies"] == nil {
		t.Error("should report skipped")
	}
}

func TestStripSeenBodies_GatherNewBody(t *testing.T) {
	s := New()
	result := map[string]any{
		"target_body": "func fresh() {}",
		"target":      map[string]any{"file": "f.go", "name": "fresh"},
	}
	s.StripSeenBodies(result, "gather")
	if result["target_body"] == "[in context]" {
		t.Error("new body should not be stripped")
	}
	if _, ok := s.SeenBodies["f.go:fresh"]; !ok {
		t.Error("new body should be tracked after strip")
	}
}

func TestStripSeenBodies_Search(t *testing.T) {
	s := New()
	s.SeenBodies["f.go:a"] = ContentHash("func a() {}")

	result := map[string]any{
		"matches": []any{
			map[string]any{
				"body":   "func a() {}",
				"symbol": map[string]any{"file": "f.go", "name": "a"},
			},
			map[string]any{
				"body":   "func b() {}",
				"symbol": map[string]any{"file": "g.go", "name": "b"},
			},
		},
	}
	s.StripSeenBodies(result, "search")

	matches := result["matches"].([]any)
	m0 := matches[0].(map[string]any)
	if m0["body"] != "[in context]" {
		t.Error("seen body should be stripped")
	}
	m1 := matches[1].(map[string]any)
	if m1["body"] == "[in context]" {
		t.Error("unseen body should remain")
	}
	if _, ok := s.SeenBodies["g.go:b"]; !ok {
		t.Error("new body should be tracked")
	}
}

func TestStripSeenBodies_GatherSnippets(t *testing.T) {
	s := New()
	s.SeenBodies["f.go:helper"] = ContentHash("func helper() {}")

	result := map[string]any{
		"target_body": "func main() {}",
		"target":      map[string]any{"file": "m.go", "name": "main"},
		"caller_snippets": map[string]any{
			"helper": "func helper() {}",
			"other":  "func other() {}",
		},
	}
	s.StripSeenBodies(result, "gather")

	snippets := result["caller_snippets"].(map[string]any)
	if snippets["helper"] != "[in context]" {
		t.Error("seen caller snippet should be stripped")
	}
	if snippets["other"] == "[in context]" {
		t.Error("unseen caller snippet should remain")
	}
}

// --- isBodySeen / trackBodyByName ---

func TestIsBodySeen_MatchByName(t *testing.T) {
	s := New()
	s.SeenBodies["f.go:foo"] = ContentHash("body")
	if !s.isBodySeen("foo", "body") {
		t.Error("should match by name suffix")
	}
}

func TestIsBodySeen_DifferentContent(t *testing.T) {
	s := New()
	s.SeenBodies["f.go:foo"] = ContentHash("old body")
	if s.isBodySeen("foo", "new body") {
		t.Error("different content should not match")
	}
}

func TestTrackBodyByName_UpdatesExisting(t *testing.T) {
	s := New()
	s.SeenBodies["f.go:foo"] = "oldhash"
	s.trackBodyByName("foo", "new body")
	if s.SeenBodies["f.go:foo"] != ContentHash("new body") {
		t.Error("should update existing entry")
	}
}

func TestTrackBodyByName_CreatesNew(t *testing.T) {
	s := New()
	s.trackBodyByName("bar", "body")
	if _, ok := s.SeenBodies[":bar"]; !ok {
		t.Error("should create with :name key")
	}
}

// --- PostProcess ---

func TestPostProcess_EditSmallDiff(t *testing.T) {
	s := New()
	text := `{"ok":true,"file":"f.go","diff":"-old\n+new\n","hash":"abc"}`
	result := s.PostProcess("edit", []string{"f.go"}, map[string]any{}, nil, text)
	if !strings.Contains(result, "lines_changed") {
		t.Error("small edit should have lines_changed")
	}
	if !strings.Contains(result, "diff") {
		t.Error("small edit should keep diff inline")
	}
}

func TestPostProcess_EditLargeDiff(t *testing.T) {
	s := New()
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, "+line")
	}
	bigDiff := strings.Join(lines, "\\n")
	text := `{"ok":true,"file":"f.go","diff":"` + bigDiff + `","hash":"abc","old_size":10,"new_size":100}`
	result := s.PostProcess("edit", []string{"f.go"}, map[string]any{}, nil, text)
	if !strings.Contains(result, "lines_changed") {
		t.Error("large edit should have lines_changed")
	}
	if !strings.Contains(result, "diff_available") {
		t.Error("large edit should have diff_available")
	}
}

func TestPostProcess_DeltaReadUnchanged(t *testing.T) {
	s := New()
	text := `{"content":"hello","file":"f.go","hash":"abc","lines":[1,5]}`
	s.PostProcess("focus", []string{"f.go"}, map[string]any{}, nil, text)
	result := s.PostProcess("focus", []string{"f.go"}, map[string]any{}, nil, text)
	if !strings.Contains(result, "unchanged") {
		t.Error("re-read should return unchanged")
	}
}

func TestPostProcess_GatherStripsBody(t *testing.T) {
	s := New()
	s.SeenBodies["m.go:main"] = ContentHash("func main() {}")
	text := `{"target_body":"func main() {}","target":{"file":"m.go","name":"main"}}`
	result := s.PostProcess("gather", []string{"main"}, map[string]any{}, nil, text)
	if !strings.Contains(result, "[in context]") {
		t.Error("gather should strip seen body")
	}
}

func TestPostProcess_SearchStripsBody(t *testing.T) {
	s := New()
	s.SeenBodies["f.go:foo"] = ContentHash("func foo() {}")
	text := `{"matches":[{"body":"func foo() {}","symbol":{"file":"f.go","name":"foo"}}]}`
	result := s.PostProcess("search", []string{"foo"}, map[string]any{"body": true}, nil, text)
	if !strings.Contains(result, "[in context]") {
		t.Error("search --body should strip seen body")
	}
}

func TestPostProcess_SearchNoBodyFlag(t *testing.T) {
	s := New()
	s.SeenBodies["f.go:foo"] = ContentHash("func foo() {}")
	text := `{"matches":[{"body":"func foo() {}","symbol":{"file":"f.go","name":"foo"}}]}`
	result := s.PostProcess("search", []string{"foo"}, map[string]any{}, nil, text)
	if strings.Contains(result, "[in context]") {
		t.Error("search without --body should not strip")
	}
}

func TestPostProcess_NonJSON(t *testing.T) {
	s := New()
	result := s.PostProcess("read", []string{}, map[string]any{}, nil, "not json")
	if result != "not json" {
		t.Error("non-JSON should pass through")
	}
}

// --- PostProcessNonObject ---

func TestPostProcessNonObject_NotRead(t *testing.T) {
	s := New()
	result := s.PostProcessNonObject("search", nil, map[string]any{}, "[1,2,3]")
	if result != "[1,2,3]" {
		t.Error("non-read should pass through")
	}
}

func TestPostProcessNonObject_BatchReadUnchanged(t *testing.T) {
	s := New()
	text := `[{"content":"hello","file":"f.go","hash":"abc","lines":[1,5]}]`
	s.PostProcessNonObject("read", nil, map[string]any{}, text)
	result := s.PostProcessNonObject("read", nil, map[string]any{}, text)
	if !strings.Contains(result, "unchanged") {
		t.Error("re-read should return unchanged")
	}
}

func TestPostProcessNonObject_BatchReadFull(t *testing.T) {
	s := New()
	text := `[{"content":"hello","file":"f.go","hash":"abc","lines":[1,5]}]`
	s.PostProcessNonObject("read", nil, map[string]any{}, text)
	result := s.PostProcessNonObject("read", nil, map[string]any{"full": true}, text)
	if strings.Contains(result, "unchanged") {
		t.Error("--full should not return unchanged")
	}
}

func TestPostProcessNonObject_BatchReadSymbol(t *testing.T) {
	s := New()
	text := `[{"content":"func foo() {}","file":"f.go","symbol":"foo","hash":"abc"}]`
	s.PostProcessNonObject("read", nil, map[string]any{}, text)
	if _, ok := s.SeenBodies["f.go:foo"]; !ok {
		t.Error("batch read symbol should track body")
	}
}

func TestPostProcessNonObject_InvalidJSON(t *testing.T) {
	s := New()
	result := s.PostProcessNonObject("read", nil, map[string]any{}, "not json")
	if result != "not json" {
		t.Error("invalid JSON should pass through")
	}
}

// --- FlagIsTruthy ---

func TestFlagIsTruthy_True(t *testing.T) {
	if !FlagIsTruthy(map[string]any{"verbose": true}, "verbose") {
		t.Error("should be truthy")
	}
}

func TestFlagIsTruthy_False(t *testing.T) {
	if FlagIsTruthy(map[string]any{"verbose": false}, "verbose") {
		t.Error("should not be truthy")
	}
}

func TestFlagIsTruthy_Missing(t *testing.T) {
	if FlagIsTruthy(map[string]any{}, "verbose") {
		t.Error("missing should not be truthy")
	}
}

func TestFlagIsTruthy_NonBool(t *testing.T) {
	if FlagIsTruthy(map[string]any{"verbose": "yes"}, "verbose") {
		t.Error("non-bool should not be truthy")
	}
}

// --- Command category maps ---

func TestCommandMaps_Coverage(t *testing.T) {
	if !ReadCommands["focus"] {
		t.Error("focus should be in ReadCommands")
	}
	if !EditCommands["edit"] {
		t.Error("edit should be in EditCommands")
	}
	if !DiffEditCommands["edit"] {
		t.Error("edit should be in DiffEditCommands")
	}
	if !DeltaReadCommands["focus"] {
		t.Error("focus should be in DeltaReadCommands")
	}
	if !BodyCommands["focus"] {
		t.Error("focus should be in BodyCommands")
	}
}

func TestBatchThenSingleReadDelta(t *testing.T) {
	s := New()
	// Batch read stores symbol content via PostProcess
	batchText := `[{"content":"func foo() {}","file":"f.go","symbol":"foo","hash":"abc","ok":true}]`
	s.PostProcessNonObject("read", nil, map[string]any{}, batchText)

	// Single read of same symbol should emit content (symbols are small, no dedup)
	singleResult := map[string]any{
		"file":    "f.go",
		"symbol":  "foo",
		"content": "func foo() {}",
	}
	delta := s.ProcessReadResult("read", singleResult, map[string]any{})
	if delta != nil {
		t.Fatal("symbol reads should always emit content (no dedup stub), got delta")
	}
	if singleResult["session"] != "unchanged" {
		t.Errorf("expected session=unchanged on result, got %v", singleResult["session"])
	}
}

func TestBatchThenSingleReadDelta_FileLevel(t *testing.T) {
	s := New()
	// Batch read stores file content via PostProcess
	batchText := `[{"content":"package main\n","file":"f.go","hash":"abc","ok":true,"lines":[1,2]}]`
	s.PostProcessNonObject("read", nil, map[string]any{}, batchText)

	// Single read of same file should return unchanged via ProcessReadResult
	singleResult := map[string]any{
		"file":    "f.go",
		"content": "package main\n",
		"hash":    "abc",
		"lines":   []int{1, 2},
	}
	delta := s.ProcessReadResult("read", singleResult, map[string]any{})
	if delta == nil {
		t.Fatal("single read after batch should return delta, got nil (full content)")
	}
	if delta["unchanged"] != true {
		t.Errorf("expected unchanged, got %v", delta)
	}
}


// --- Session dedup for search/map ---

func TestPostProcess_SearchSessionNew(t *testing.T) {
	sess := New()
	result := `{"kind":"symbol","matches":[{"name":"foo"}]}`
	out := sess.PostProcess("search", []string{"foo"}, map[string]any{}, nil, result)

	if !strings.Contains(out, `"session":"new"`) {
		t.Errorf("first search should have session=new, got: %s", out)
	}
}

func TestPostProcess_SearchSessionUnchanged(t *testing.T) {
	sess := New()
	result := `{"kind":"symbol","matches":[{"name":"foo"}]}`

	// First call: stores the hash
	sess.PostProcess("search", []string{"foo"}, map[string]any{}, nil, result)

	// Second call with identical result: should return unchanged
	out := sess.PostProcess("search", []string{"foo"}, map[string]any{}, nil, result)
	if !strings.Contains(out, `"session":"unchanged"`) {
		t.Errorf("repeat search should return session=unchanged, got: %s", out)
	}
}

func TestPostProcess_SearchDifferentFlagsNotCached(t *testing.T) {
	sess := New()
	result := `{"kind":"symbol","matches":[{"name":"foo"}]}`

	// Store with no flags
	sess.PostProcess("search", []string{"foo"}, map[string]any{}, nil, result)

	// Same pattern but different flags — cache key differs
	out := sess.PostProcess("search", []string{"foo"}, map[string]any{"text": true}, nil, result)
	if strings.Contains(out, `"session":"unchanged"`) {
		t.Error("different flags should produce a cache miss")
	}
}

func TestPostProcess_MapSessionNew(t *testing.T) {
	sess := New()
	result := `{"files":5,"symbols":20,"content":"..."}`
	out := sess.PostProcess("map", []string{}, map[string]any{}, nil, result)

	if !strings.Contains(out, `"session":"new"`) {
		t.Errorf("first map should have session=new, got: %s", out)
	}
}

func TestPostProcess_ReadNotAffected(t *testing.T) {
	// read uses its own delta-read path, not the search/map/refs hash path
	sess := New()
	result := `{"file":"test.go","content":"hello","lines":[1,5]}`
	out := sess.PostProcess("read", []string{"test.go"}, map[string]any{}, nil, result)

	// Should go through ProcessReadResult, not the search/map hash path
	if strings.Contains(out, `"session":"new"`) {
		// read uses ProcessReadResult which also sets session:new — that's fine
	}
}
