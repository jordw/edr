package dispatch_test

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
	"github.com/jordw/edr/internal/search"
)

func TestBatchReadErrorEntries(t *testing.T) {
	// Create a temp directory with a valid Go file.
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func hello() {
	println("hello")
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Open DB and index the repo.
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Call batch-read with a mix of valid and invalid args.
	args := []string{
		"main.go",              // valid file
		"nonexistent.go",       // invalid file
		"main.go:hello",        // valid symbol
		"main.go:nosuchsymbol", // invalid symbol
	}
	result, err := dispatch.Dispatch(ctx, db, "read", args, map[string]any{})
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	// Marshal and unmarshal to inspect structured output.
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var entries []struct {
		File    string `json:"file"`
		Symbol  string `json:"symbol,omitempty"`
		OK      bool   `json:"ok"`
		Error   string `json:"error,omitempty"`
		Content string `json:"content,omitempty"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// We should get exactly 4 entries — one per arg, no silent drops.
	if len(entries) != 4 {
		t.Fatalf("expected 4 entries, got %d: %s", len(entries), string(raw))
	}

	// Entry 0: valid file read
	if !entries[0].OK {
		t.Errorf("entry 0 (main.go): expected ok=true, got ok=false, error=%q", entries[0].Error)
	}
	if entries[0].Content == "" {
		t.Error("entry 0 (main.go): expected non-empty content")
	}

	// Entry 1: invalid file
	if entries[1].OK {
		t.Error("entry 1 (nonexistent.go): expected ok=false, got ok=true")
	}
	if entries[1].Error == "" {
		t.Error("entry 1 (nonexistent.go): expected non-empty error message")
	}

	// Entry 2: valid symbol read
	if !entries[2].OK {
		t.Errorf("entry 2 (main.go:hello): expected ok=true, got ok=false, error=%q", entries[2].Error)
	}
	if entries[2].Content == "" {
		t.Error("entry 2 (main.go:hello): expected non-empty content")
	}
	if entries[2].Symbol != "hello" {
		t.Errorf("entry 2: expected symbol=\"hello\", got %q", entries[2].Symbol)
	}

	// Entry 3: invalid symbol
	if entries[3].OK {
		t.Error("entry 3 (main.go:nosuchsymbol): expected ok=false, got ok=true")
	}
	if entries[3].Error == "" {
		t.Error("entry 3 (main.go:nosuchsymbol): expected non-empty error message")
	}
}

func TestEditPlanDryRunIncludesDiff(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func hello() {
	println("hello")
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	edits := []map[string]any{
		{
			"file":     "main.go",
			"old_text": `println("hello")`,
			"new_text": `println("world")`,
		},
	}

	flags := map[string]any{
		"dry-run": true,
		"edits":   edits,
	}

	result, err := dispatch.Dispatch(ctx, db, "edit", nil, flags)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\nraw: %s", err, string(raw))
	}

	if dryRun, ok := out["dry_run"].(bool); !ok || !dryRun {
		t.Errorf("expected dry_run=true, got %v", out["dry_run"])
	}

	editsRaw, ok := out["edits"].([]any)
	if !ok {
		t.Fatalf("expected edits to be array, got %T: %s", out["edits"], string(raw))
	}
	if len(editsRaw) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(editsRaw))
	}

	entry, ok := editsRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("expected edit entry to be object, got %T", editsRaw[0])
	}

	diff, ok := entry["diff"].(string)
	if !ok || diff == "" {
		t.Fatalf("expected non-empty diff field in edit entry, got %v", entry["diff"])
	}
	if !strings.Contains(diff, "-") {
		t.Errorf("expected diff to contain '-' lines, got: %s", diff)
	}
	if !strings.Contains(diff, "+") {
		t.Errorf("expected diff to contain '+' lines, got: %s", diff)
	}
}

func TestDispatchMulti_ParallelDifferentFiles(t *testing.T) {
	tmp := t.TempDir()
	// Create two separate Go files
	for _, name := range []string{"a.go", "b.go"} {
		content := "package main\n\nfunc " + strings.TrimSuffix(name, ".go") + "() {}\n"
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	commands := []dispatch.MultiCmd{
		{Cmd: "read", Args: []string{"a.go"}},
		{Cmd: "read", Args: []string{"b.go"}},
	}

	results := dispatch.DispatchMulti(ctx, db, commands)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if !r.OK {
			t.Errorf("result %d: expected ok=true, got error=%q", i, r.Error)
		}
	}
}

func TestDispatchMulti_GlobalMutatingIsSequential(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "a.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Mix of init (global-mutating) and read — should run sequentially
	commands := []dispatch.MultiCmd{
		{Cmd: "read", Args: []string{"a.go"}},
		{Cmd: "reindex"},
		{Cmd: "read", Args: []string{"a.go"}},
	}

	results := dispatch.DispatchMulti(ctx, db, commands)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	for i, r := range results {
		if !r.OK {
			t.Errorf("result %d (%s): expected ok=true, got error=%q", i, commands[i].Cmd, r.Error)
		}
	}
}

func TestDispatchMulti_PreservesResultOrder(t *testing.T) {
	tmp := t.TempDir()
	for _, name := range []string{"x.go", "y.go", "z.go"} {
		content := "package main\n\nfunc " + strings.TrimSuffix(name, ".go") + "() {}\n"
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	commands := []dispatch.MultiCmd{
		{Cmd: "read", Args: []string{"x.go"}},
		{Cmd: "read", Args: []string{"y.go"}},
		{Cmd: "read", Args: []string{"z.go"}},
	}

	results := dispatch.DispatchMulti(ctx, db, commands)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// Verify each result corresponds to the right command
	for i, r := range results {
		if !r.OK {
			t.Errorf("result %d: expected ok=true, error=%q", i, r.Error)
			continue
		}
		raw, _ := json.Marshal(r.Result)
		if !strings.Contains(string(raw), strings.TrimSuffix(commands[i].Args[0], ".go")+".go") {
			t.Errorf("result %d: expected file reference to %s, got %s", i, commands[i].Args[0], string(raw))
		}
	}
}

func TestDispatchMulti_BudgetDistribution(t *testing.T) {
	tmp := t.TempDir()
	// Create a file with enough content to exceed a small budget
	var big strings.Builder
	big.WriteString("package main\n\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&big, "func f%d() { println(%d) }\n", i, i)
	}
	if err := os.WriteFile(filepath.Join(tmp, "big.go"), []byte(big.String()), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Without budget: full content
	cmdsNoBudget := []dispatch.MultiCmd{
		{Cmd: "read", Args: []string{"big.go"}},
	}
	resultsNoBudget := dispatch.DispatchMulti(ctx, db, cmdsNoBudget)
	noBudgetRaw, _ := json.Marshal(resultsNoBudget[0].Result)

	// With budget: truncated content
	cmdsWithBudget := []dispatch.MultiCmd{
		{Cmd: "read", Args: []string{"big.go"}},
	}
	resultsWithBudget := dispatch.DispatchMulti(ctx, db, cmdsWithBudget, 100)
	withBudgetRaw, _ := json.Marshal(resultsWithBudget[0].Result)

	if len(withBudgetRaw) >= len(noBudgetRaw) {
		t.Errorf("budget should reduce output: no_budget=%d, with_budget=%d", len(noBudgetRaw), len(withBudgetRaw))
	}
}

func TestEditReindexesImmediately(t *testing.T) {
	// After an edit, the new symbol should be immediately queryable
	// without any separate flush or reindex step.
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc oldName() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Edit: rename oldName to newName via old_text/new_text
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func oldName()",
		"new_text": "func newName()",
	})
	if err != nil {
		t.Fatalf("edit dispatch: %v", err)
	}

	// Verify the edit response has ok=true and no index_error
	raw, _ := json.Marshal(result)
	var editOut map[string]any
	json.Unmarshal(raw, &editOut)
	if editOut["ok"] != true {
		t.Fatalf("expected ok=true, got: %s", string(raw))
	}
	if ie, ok := editOut["index_error"]; ok && ie != "" {
		t.Errorf("unexpected index_error: %v", ie)
	}

	// Immediately read the new symbol — should work without any flush
	result, err = dispatch.Dispatch(ctx, db, "read", []string{"main.go", "newName"}, map[string]any{})
	if err != nil {
		t.Fatalf("read newName after edit: %v", err)
	}
	raw, _ = json.Marshal(result)
	if !strings.Contains(string(raw), "newName") {
		t.Fatalf("expected newName to be queryable immediately after edit, got: %s", string(raw))
	}
}

func TestWriteReindexesImmediately(t *testing.T) {
	// After a write, the new symbols should be immediately queryable.
	tmp := t.TempDir()

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Write a new Go file with a function
	_, err = dispatch.Dispatch(ctx, db, "write", []string{"hello.go"}, map[string]any{
		"content": "package main\n\nfunc Hello() {\n\tprintln(\"hello\")\n}\n",
	})
	if err != nil {
		t.Fatalf("write dispatch: %v", err)
	}

	// Immediately read the symbol — should work without any flush
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"hello.go", "Hello"}, map[string]any{})
	if err != nil {
		t.Fatalf("read Hello after write: %v", err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "Hello") {
		t.Fatalf("expected Hello to be queryable immediately after write, got: %s", string(raw))
	}
}

func TestEditNoIndexErrorOnSuccess(t *testing.T) {
	// Verify that a normal edit returns no index_error field.
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"),
		[]byte("package main\n\nfunc foo() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func foo()",
		"new_text": "func bar()",
	})
	if err != nil {
		t.Fatalf("edit: %v", err)
	}

	raw, _ := json.Marshal(result)
	var out map[string]any
	json.Unmarshal(raw, &out)

	if ie, exists := out["index_error"]; exists {
		t.Errorf("expected no index_error field on success, got: %v", ie)
	}
}

func TestWriteIndexErrorIsSurfaced(t *testing.T) {
	// Verify that the EditResult struct includes IndexError when present.
	// We can't easily trigger a real IndexFile failure, but we can verify
	// the struct serializes the field correctly.
	r := output.EditResult{
		OK:         true,
		File:       "test.go",
		Message:    "wrote 100 bytes",
		Hash:       "abcd1234",
		IndexError: "simulated index failure",
	}
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var out map[string]any
	json.Unmarshal(raw, &out)

	if out["index_error"] != "simulated index failure" {
		t.Errorf("expected index_error to be serialized, got: %s", string(raw))
	}

	// Verify omitempty: no index_error when empty
	r.IndexError = ""
	raw, _ = json.Marshal(r)
	var out2 map[string]any
	json.Unmarshal(raw, &out2)
	if _, exists := out2["index_error"]; exists {
		t.Errorf("expected index_error to be omitted when empty, got: %s", string(raw))
	}
}

func TestFlagNormalization_DryRunUnderscore(t *testing.T) {
	// dry_run (underscore) must work the same as dry-run (hyphen)
	// for all commands. Regression test: edit-plan and rename only
	// checked "dry-run" but batch JSON passes "dry_run".
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc hello() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Test edit-plan with dry_run (underscore)
	edits := []map[string]any{{
		"file":     "main.go",
		"old_text": "func hello()",
		"new_text": "func goodbye()",
	}}
	_, err = dispatch.Dispatch(ctx, db, "edit", nil, map[string]any{
		"dry_run": true,
		"edits":   edits,
	})
	if err != nil {
		t.Fatalf("edit with dry_run: %v", err)
	}
	data, _ := os.ReadFile(goFile)
	if string(data) != original {
		t.Fatalf("edit dry_run modified file!\nexpected: %q\ngot:     %q", original, string(data))
	}

	// Test rename with dry_run (underscore)
	_, err = dispatch.Dispatch(ctx, db, "rename", []string{"hello", "goodbye"}, map[string]any{
		"dry_run": true,
	})
	if err != nil {
		t.Fatalf("rename with dry_run: %v", err)
	}
	data, _ = os.ReadFile(goFile)
	if string(data) != original {
		t.Fatalf("rename dry_run modified file!\nexpected: %q\ngot:     %q", original, string(data))
	}

	// Test edit with dry_run (underscore)
	_, err = dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func hello()",
		"new_text": "func goodbye()",
		"dry_run":  true,
	})
	if err != nil {
		t.Fatalf("edit with dry_run: %v", err)
	}
	data, _ = os.ReadFile(goFile)
	if string(data) != original {
		t.Fatalf("edit dry_run modified file!\nexpected: %q\ngot:     %q", original, string(data))
	}
}

func TestWriteRefusesEmptyOverwrite(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	// Write with empty content should be refused
	_, err = dispatch.Dispatch(ctx, db, "write", []string{"main.go"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for empty overwrite, got nil")
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify file was not clobbered
	data, _ := os.ReadFile(goFile)
	if string(data) != "package main\n" {
		t.Fatalf("file was clobbered: %q", string(data))
	}

	// Empty write on non-empty file should always error now (--force removed)
	_, err = dispatch.Dispatch(ctx, db, "write", []string{"main.go"}, map[string]any{})
	if err == nil {
		t.Fatalf("empty write on non-empty file should error")
	}
}

func TestEditAmbiguousMatchErrors(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nvar x = \"Hello\"\nvar y = \"Hello\"\nvar z = \"Hello\"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Ambiguous match without all: true should error
	_, err = dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "Hello",
		"new_text": "World",
	})
	if err == nil {
		t.Fatal("expected error for ambiguous match, got nil")
	}
	if !strings.Contains(err.Error(), "matched 3 locations") {
		t.Errorf("expected ambiguous match error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "lines") {
		t.Errorf("expected line numbers in error, got: %v", err)
	}

	// With all: true should succeed
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "Hello",
		"new_text": "World",
		"all":      true,
	})
	if err != nil {
		t.Fatalf("edit with all: %v", err)
	}
	raw, _ := json.Marshal(result)
	var out map[string]any
	json.Unmarshal(raw, &out)
	if count, ok := out["count"]; !ok || count != float64(3) {
		t.Errorf("expected count=3, got %v (raw: %s)", out["count"], string(raw))
	}
}

func TestReadFileSignatures(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func hello() {
	println("hello")
	if true {
		println("nested")
	}
}

func world() {
	println("world")
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Read with --signatures
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"main.go"}, map[string]any{
		"signatures": true,
	})
	if err != nil {
		t.Fatalf("read --signatures: %v", err)
	}

	raw, _ := json.Marshal(result)
	var out map[string]any
	json.Unmarshal(raw, &out)

	if out["signatures"] != true {
		t.Errorf("expected signatures=true, got: %s", string(raw))
	}

	content, _ := out["content"].(string)
	if content == "" {
		t.Fatal("expected non-empty content")
	}

	// Full read for comparison
	fullResult, _ := dispatch.Dispatch(ctx, db, "read", []string{"main.go"}, map[string]any{})
	fullRaw, _ := json.Marshal(fullResult)
	var fullOut map[string]any
	json.Unmarshal(fullRaw, &fullOut)
	fullContent, _ := fullOut["content"].(string)

	if len(content) >= len(fullContent) {
		t.Errorf("signatures content (%d) should be smaller than full content (%d)", len(content), len(fullContent))
	}
}

func TestSearchEmptyPatternReturnsError(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Symbol search with empty pattern
	_, err = dispatch.Dispatch(ctx, db, "search", []string{""}, nil)
	if err == nil {
		t.Fatal("expected error for empty search pattern, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected error about non-empty pattern, got: %v", err)
	}

	// Text search with empty pattern
	_, err = dispatch.Dispatch(ctx, db, "search", []string{""}, map[string]any{"text": true})
	if err == nil {
		t.Fatal("expected error for empty text search pattern, got nil")
	}
	if !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("expected error about non-empty pattern, got: %v", err)
	}

	// No args at all
	_, err = dispatch.Dispatch(ctx, db, "search", []string{}, nil)
	if err == nil {
		t.Fatal("expected error for missing search pattern, got nil")
	}
}


func TestSearchLimit(t *testing.T) {
	tmp := t.TempDir()
	// Create a file with multiple functions so symbol search has many results
	var src strings.Builder
	src.WriteString("package main\n\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&src, "func Handle%d() {}\n", i)
	}
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(src.String()), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Symbol search: no limit returns all
	sr, err := search.SearchSymbol(ctx, db, "Handle", 0, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.Matches) < 10 {
		t.Fatalf("expected at least 10 matches without limit, got %d", len(sr.Matches))
	}

	// Symbol search: limit=3
	sr, err = search.SearchSymbol(ctx, db, "Handle", 0, false, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.Matches) != 3 {
		t.Fatalf("expected 3 matches with limit=3, got %d", len(sr.Matches))
	}
	if sr.TotalMatches < 10 {
		t.Fatalf("expected total_matches >= 10, got %d", sr.TotalMatches)
	}

	// Text search: limit=2
	sr, err = search.SearchText(ctx, db, "Handle", 0, false, search.WithLimit(2))
	if err != nil {
		t.Fatal(err)
	}
	if len(sr.Matches) != 2 {
		t.Fatalf("expected 2 text matches with limit=2, got %d", len(sr.Matches))
	}
}

func TestWriteInsideAfterGo(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "store.go")
	original := "package store\n\ntype S struct {\n\tdb string\n}\n\nfunc (s *S) Get() string {\n\treturn s.db\n}\n\nfunc (s *S) Put(v string) {\n\t_ = v\n}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Write a method --inside S --after Get
	_, err = dispatch.Dispatch(ctx, db, "write", []string{"store.go"}, map[string]any{
		"inside":  "S",
		"after":   "Get",
		"content": "func (s *S) Delete() error {\n\treturn nil\n}",
	})
	if err != nil {
		t.Fatalf("write --inside --after returned error: %v", err)
	}

	data, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	getIdx := strings.Index(content, "func (s *S) Get")
	deleteIdx := strings.Index(content, "func (s *S) Delete")
	putIdx := strings.Index(content, "func (s *S) Put")
	if deleteIdx < getIdx {
		t.Fatalf("Delete should be after Get, but Get=%d Delete=%d", getIdx, deleteIdx)
	}
	if deleteIdx > putIdx {
		t.Fatalf("Delete should be before Put, but Delete=%d Put=%d", deleteIdx, putIdx)
	}
}

func TestEditNoopDetection(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc hello() {\n\tprintln(\"hello\")\n}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Edit with identical old_text and new_text
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "hello",
		"new_text": "hello",
	})
	if err != nil {
		t.Fatalf("noop edit returned error: %v", err)
	}

	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if m["status"] != "noop" {
		t.Fatalf("expected status=noop, got: %v", m)
	}

	// Verify file was not modified
	data, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("file was modified during noop edit: %q", string(data))
	}
}


// === Tests for bug fixes ===

func TestReadLineRangeStartBeyondEnd(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// start_line > end_line should return an error, not panic
	_, err = dispatch.Dispatch(ctx, db, "read", []string{"main.go", "100", "50"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for start > end line range, got nil")
	}
	if !strings.Contains(err.Error(), "beyond end line") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReadLineRangeBeyondEOF(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// start_line beyond EOF should return an error, not panic
	_, err = dispatch.Dispatch(ctx, db, "read", []string{"main.go", "99999", "100000"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for line range beyond EOF, got nil")
	}
	if !strings.Contains(err.Error(), "beyond end line") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEditEmptyNewTextIsDeletion(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc remove() {}\n\nfunc keep() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Empty new_text with old_text should delete the matched text
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func remove() {}\n\n",
		"new_text": "",
	})
	if err != nil {
		t.Fatalf("edit with empty new_text returned error: %v", err)
	}

	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("expected ok:true, got: %s", string(raw))
	}

	data, _ := os.ReadFile(goFile)
	if strings.Contains(string(data), "remove") {
		t.Fatalf("deleted text still present: %s", string(data))
	}
	if !strings.Contains(string(data), "func keep()") {
		t.Fatalf("kept text missing: %s", string(data))
	}
}

func TestEditEmptyNewTextRequiresEditMode(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Empty new_text with no edit mode should still error
	_, err = dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"new_text": "",
	})
	if err == nil {
		t.Fatal("expected error for edit with empty new_text and no edit mode")
	}
}

// TestRenameScopedDoesNotRenameUnrelatedFile verifies that rename only targets
// resolved references via the refs table, not unrelated files that happen to
// use the same identifier name.
func TestRenameScopedDoesNotRenameUnrelatedFile(t *testing.T) {
	tmp := t.TempDir()

	// lib/ defines "count"
	libDir := filepath.Join(tmp, "lib")
	os.MkdirAll(libDir, 0755)
	if err := os.WriteFile(filepath.Join(libDir, "counter.go"), []byte(`package lib

func count() int {
	return 42
}

func useCount() int {
	return count() + 1
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// unrelated/ also uses "count" as a local variable — not a reference to lib.count.
	unrelDir := filepath.Join(tmp, "unrelated")
	os.MkdirAll(unrelDir, 0755)
	unrelFile := filepath.Join(unrelDir, "other.go")
	unrelSrc := `package unrelated

func doStuff() int {
	count := 10
	return count + 1
}
`
	if err := os.WriteFile(unrelFile, []byte(unrelSrc), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Rename the lib-level "count" function to "tally".
	// The unrelated package's local "count" should NOT be renamed.
	result, err := dispatch.Dispatch(ctx, db, "rename", []string{"count", "tally"}, map[string]any{
		"dry_run": true,
	})
	if err != nil {
		t.Fatalf("rename dry_run: %v", err)
	}

	raw, _ := json.Marshal(result)
	var rr output.RenameResult
	if err := json.Unmarshal(raw, &rr); err != nil {
		t.Fatal(err)
	}

	// Should find occurrences only in lib/ (definition + useCount), not in unrelated/.
	for _, p := range rr.Preview {
		if strings.Contains(p.File, "unrelated") {
			t.Errorf("rename should not touch unrelated package, but found: %+v", p)
		}
	}
	if rr.Occurrences < 2 {
		t.Errorf("expected at least 2 occurrences in lib/ (definition + useCount), got %d", rr.Occurrences)
	}

	// Verify unrelated file is untouched.
	data, _ := os.ReadFile(unrelFile)
	if string(data) != unrelSrc {
		t.Errorf("unrelated file was modified: %s", string(data))
	}
}

// TestRenameAmbiguousSymbolFailsFast verifies that renaming an ambiguous symbol
// returns an error instead of silently falling back to repo-wide text scanning.
func TestRenameAmbiguousSymbolFailsFast(t *testing.T) {
	tmp := t.TempDir()
	// Two packages define the same symbol name "Process".
	pkgA := filepath.Join(tmp, "pkga")
	pkgB := filepath.Join(tmp, "pkgb")
	os.MkdirAll(pkgA, 0755)
	os.MkdirAll(pkgB, 0755)

	if err := os.WriteFile(filepath.Join(pkgA, "a.go"), []byte(`package pkga

func Process() string {
	return "a"
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgB, "b.go"), []byte(`package pkgb

func Process() string {
	return "b"
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Renaming "Process" should fail because it's ambiguous (exists in two packages).
	_, err = dispatch.Dispatch(ctx, db, "rename", []string{"Process", "Handle"}, nil)
	if err == nil {
		t.Fatal("expected error for ambiguous rename target, got nil")
	}
	if !strings.Contains(err.Error(), "ambiguous") && !strings.Contains(err.Error(), "multiple") {
		t.Fatalf("expected ambiguous error, got: %v", err)
	}
}

// TestCallChainSameNameDifferentFiles verifies that call-chain traversal
// treats same-named symbols in different files as distinct nodes.
func TestCallChainSameNameDifferentFiles(t *testing.T) {
	tmp := t.TempDir()

	// Create a chain: main → helperA.Process → helperB.Process → target
	// Without the fix, both Process nodes collapse into one.
	if err := os.WriteFile(filepath.Join(tmp, "target.go"), []byte(`package main

func target() string {
	return "done"
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "helper_b.go"), []byte(`package main

func helperB() string {
	return target()
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "helper_a.go"), []byte(`package main

func helperA() string {
	return helperB()
}
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "entry.go"), []byte(`package main

func entry() string {
	return helperA()
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	result, err := dispatch.Dispatch(ctx, db, "refs", []string{"entry"}, map[string]any{
		"chain": "target",
	})
	if err != nil {
		t.Fatalf("call-chain: %v", err)
	}

	raw, _ := json.Marshal(result)
	var cc map[string]any
	if err := json.Unmarshal(raw, &cc); err != nil {
		t.Fatal(err)
	}
	if cc["found"] != true {
		t.Fatalf("expected call chain to be found, got: %s", string(raw))
	}
	chain, ok := cc["chain"].([]any)
	if !ok || len(chain) < 3 {
		t.Fatalf("expected chain of length >= 3, got: %s", string(raw))
	}
}

// TestIndexWarningsSurfaced verifies that per-file index errors are
// accumulated and accessible via DB.IndexWarnings().
func TestIndexWarningsSurfaced(t *testing.T) {
	tmp := t.TempDir()
	// Valid file
	if err := os.WriteFile(filepath.Join(tmp, "good.go"), []byte(`package main

func good() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	_, _, err = index.IndexRepo(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	// After a successful index, warnings should be empty.
	warnings := db.IndexWarnings()
	if len(warnings) != 0 {
		t.Fatalf("expected 0 warnings for valid repo, got %d: %+v", len(warnings), warnings)
	}
}

// TestSourceCacheReducesReads verifies that WithSourceCache prevents redundant reads.
func TestSourceCacheReducesReads(t *testing.T) {
	tmp := t.TempDir()
	f := filepath.Join(tmp, "test.go")
	if err := os.WriteFile(f, []byte(`package main

func hello() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	ctx := index.WithSourceCache(context.Background())

	// First read should succeed.
	data1, err := index.CachedReadFile(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data1), "hello") {
		t.Fatal("unexpected content")
	}

	// Delete the file — cached read should still return data.
	os.Remove(f)
	data2, err := index.CachedReadFile(ctx, f)
	if err != nil {
		t.Fatalf("cached read after delete should work: %v", err)
	}
	if string(data1) != string(data2) {
		t.Fatal("cached data should be identical")
	}

	// Without cache, should fail.
	data3, err := index.CachedReadFile(context.Background(), f)
	if err == nil {
		t.Fatalf("uncached read of deleted file should fail, got: %s", string(data3))
	}
}


func TestSymbolsHintForUnindexedFile(t *testing.T) {
	tmp := t.TempDir()

	// Create and index a Go file.
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Create a file AFTER indexing (simulates a gitignored file that exists but isn't indexed).
	unindexed := filepath.Join(tmp, "ignored.go")
	if err := os.WriteFile(unindexed, []byte("package main\n\nfunc secret() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Dispatch "map" with a file arg routes to symbols.
	result, err := dispatch.Dispatch(ctx, db, "map", []string{"ignored.go"}, map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}

	s := string(data)
	if !strings.Contains(s, "not indexed") {
		t.Errorf("expected hint about unindexed file, got: %s", s)
	}
	if !strings.Contains(s, "gitignored") {
		t.Errorf("expected gitignored mention, got: %s", s)
	}
}

func TestSymbolNotFoundHintForUnindexedFile(t *testing.T) {
	tmp := t.TempDir()

	// Create and index a Go file.
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n\nfunc hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// Create a file AFTER indexing.
	unindexed := filepath.Join(tmp, "ignored.go")
	if err := os.WriteFile(unindexed, []byte("package main\n\nfunc secret() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// Try to read a symbol from the unindexed file — error should mention gitignored.
	_, err = dispatch.Dispatch(ctx, db, "read", []string{"ignored.go:secret"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for symbol in unindexed file")
	}
	if !strings.Contains(err.Error(), "not indexed") {
		t.Errorf("expected 'not indexed' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "gitignored") {
		t.Errorf("expected 'gitignored' in error, got: %v", err)
	}
}

// --- Signatures on non-container returns error ---

func TestReadSymbol_SignaturesOnFunction_ReturnsError(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc Execute() {\n\tfmt.Println(\"hi\")\n}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)
	output.SetRoot(db.Root())

	_, err = dispatch.Dispatch(ctx, db, "read", []string{"main.go:Execute"}, map[string]any{"signatures": true})
	if err == nil {
		t.Fatal("expected error for --signatures on a function")
	}
	if !strings.Contains(err.Error(), "not a container") {
		t.Errorf("error should mention 'not a container', got: %v", err)
	}
}

// --- Default budget on search/map ---

func TestSearch_DefaultBudgetApplied(t *testing.T) {
	tmp := t.TempDir()
	// Create enough files to exceed 2000 tokens if unbounded
	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("file%d.go", i)
		content := fmt.Sprintf("package main\n\nfunc Handler%d() {\n\t// handler implementation %d\n}\n", i, i)
		os.WriteFile(filepath.Join(tmp, name), []byte(content), 0644)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)
	output.SetRoot(db.Root())

	// Search without --budget or --full: should apply default cap
	result, err := dispatch.Dispatch(ctx, db, "search", []string{"Handler"}, map[string]any{})
	if err != nil {
		t.Fatalf("search: %v", err)
	}

	data, _ := json.Marshal(result)
	if len(data) > 20000 {
		t.Errorf("search without budget should be capped, got %d bytes", len(data))
	}
}

func TestSearch_FullBypassesDefaultBudget(t *testing.T) {
	tmp := t.TempDir()
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("file%d.go", i)
		content := fmt.Sprintf("package main\n\nfunc Handler%d() {}\n", i)
		os.WriteFile(filepath.Join(tmp, name), []byte(content), 0644)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)
	output.SetRoot(db.Root())

	// With --full, no default budget
	result, err := dispatch.Dispatch(ctx, db, "search", []string{"Handler"}, map[string]any{"full": true})
	if err != nil {
		t.Fatalf("search --full: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
}

func TestRead_NoBudgetCapByDefault(t *testing.T) {
	tmp := t.TempDir()
	// Create a file larger than 2000 tokens
	var content strings.Builder
	content.WriteString("package main\n\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&content, "func Function%d() {\n\t// implementation %d\n}\n\n", i, i)
	}
	os.WriteFile(filepath.Join(tmp, "big.go"), []byte(content.String()), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)
	output.SetRoot(db.Root())

	// Read without --budget: should NOT truncate (read has no default cap)
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"big.go"}, map[string]any{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if trunc, _ := m["truncated"].(bool); trunc {
		t.Error("read should NOT truncate by default — only search/map have default budget cap")
	}
}

// TestInternalCommandsRejected verifies that removed internal command names
// are no longer accepted by Dispatch.
func TestInternalCommandsRejected(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)

	for _, cmd := range []string{"explore", "find", "edit-plan", "multi", "init"} {
		_, err := dispatch.Dispatch(ctx, db, cmd, nil, nil)
		if err == nil {
			t.Errorf("dispatch(%q) should fail, but succeeded", cmd)
		}
		if !strings.Contains(err.Error(), "unknown command") {
			t.Errorf("dispatch(%q) error = %q, want 'unknown command'", cmd, err.Error())
		}
	}
}

// TestRefsWithCallersAbsorbedExplore verifies that refs --callers works
// (formerly the explore command).
func TestRefsWithCallersAbsorbedExplore(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte(`package main

func caller() { target() }
func target() {}
`), 0644)
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)

	// refs with --callers should work (absorbed from explore)
	result, err := dispatch.Dispatch(ctx, db, "refs", []string{"target"}, map[string]any{
		"callers": true,
		"body":    true,
	})
	if err != nil {
		t.Fatalf("refs --callers: %v", err)
	}
	if result == nil {
		t.Fatal("refs --callers returned nil")
	}
}

// TestReindexDispatch verifies the reindex command name works in dispatch.
func TestReindexDispatch(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)
	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)

	result, err := dispatch.Dispatch(ctx, db, "reindex", nil, nil)
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}
	if result == nil {
		t.Fatal("reindex returned nil")
	}
}

// TestReadResultShape_UnifiedBaseKeys verifies that all read modes share the
// same base keys: file, hash, lines, size, truncated, plus content (as "body"
// internally — normalizeReadBody renames to "content" in the CLI layer).
func TestReadResultShape_UnifiedBaseKeys(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	os.WriteFile(goFile, []byte(`package main

type Config struct {
	Name string
	Port int
}

func hello() {
	println("hello")
}
`), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)

	baseKeys := []string{"file", "hash", "lines", "size", "truncated"}

	tests := []struct {
		name  string
		args  []string
		flags map[string]any
	}{
		{"file read", []string{"main.go"}, nil},
		{"symbol read", []string{"main.go:hello"}, nil},
		{"line range", []string{"main.go"}, map[string]any{"start_line": 1, "end_line": 3}},
		{"signatures", []string{"main.go"}, map[string]any{"signatures": true}},
		{"symbol signatures", []string{"main.go:Config"}, map[string]any{"signatures": true}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := dispatch.Dispatch(ctx, db, "read", tt.args, tt.flags)
			if err != nil {
				t.Fatalf("dispatch: %v", err)
			}
			data, _ := json.Marshal(result)
			var m map[string]any
			if json.Unmarshal(data, &m) != nil {
				t.Fatalf("result is not a map: %T", result)
			}
			for _, key := range baseKeys {
				if _, ok := m[key]; !ok {
					t.Errorf("missing base key %q in %s result. Keys: %v", key, tt.name, mapKeys(m))
				}
			}
			// Content must be present as either "body" (internal) or "content" (post-normalization)
			_, hasBody := m["body"]
			_, hasContent := m["content"]
			if !hasBody && !hasContent {
				t.Errorf("missing content/body in %s result. Keys: %v", tt.name, mapKeys(m))
			}
		})
	}
}

// TestReadSymbolResult_HasSizeAndTruncated verifies that symbol reads include
// size and truncated at the top level (not just in the symbol sub-object).
func TestReadSymbolResult_HasSizeAndTruncated(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() { println(\"hi\") }\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)

	result, err := dispatch.Dispatch(ctx, db, "read", []string{"main.go:hello"}, nil)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := json.Marshal(result)
	var m map[string]any
	json.Unmarshal(data, &m)

	if _, ok := m["size"]; !ok {
		t.Error("symbol read missing top-level 'size'")
	}
	if _, ok := m["truncated"]; !ok {
		t.Error("symbol read missing top-level 'truncated'")
	}
}

// TestReadSignatures_HasLines verifies that --signatures reads include
// the lines field (covering the full file range).
func TestReadSignatures_HasLines(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc hello() {}\nfunc world() {}\n"), 0644)

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	index.IndexRepo(ctx, db)

	result, err := dispatch.Dispatch(ctx, db, "read", []string{"main.go"}, map[string]any{"signatures": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := json.Marshal(result)
	var m map[string]any
	json.Unmarshal(data, &m)

	lines, ok := m["lines"]
	if !ok {
		t.Fatal("signatures read missing 'lines'")
	}
	arr, ok := lines.([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("lines should be [start, end], got %v", lines)
	}
	if int(arr[0].(float64)) != 1 {
		t.Errorf("lines[0] = %v, want 1", arr[0])
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
