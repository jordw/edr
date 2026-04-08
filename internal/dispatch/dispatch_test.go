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
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	// Multi-file reads now return MultiResults
	multi, ok := result.(dispatch.MultiResults)
	if !ok {
		t.Fatalf("expected MultiResults, got %T", result)
	}

	// We should get exactly 4 entries — one per arg, no silent drops.
	if len(multi) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(multi))
	}

	// Entry 0: valid file read
	if !multi[0].OK {
		t.Errorf("entry 0 (main.go): expected ok=true, error=%q", multi[0].Error)
	}
	if multi[0].Result == nil {
		t.Error("entry 0 (main.go): expected non-nil result")
	}

	// Entry 1: invalid file
	if multi[1].OK {
		t.Error("entry 1 (nonexistent.go): expected ok=false")
	}
	if multi[1].Error == "" {
		t.Error("entry 1 (nonexistent.go): expected non-empty error")
	}

	// Entry 2: valid symbol read
	if !multi[2].OK {
		t.Errorf("entry 2 (main.go:hello): expected ok=true, error=%q", multi[2].Error)
	}
	if multi[2].Result != nil {
		raw, _ := json.Marshal(multi[2].Result)
		var m map[string]any
		json.Unmarshal(raw, &m)
		if m["symbol"] != "hello" {
			t.Errorf("entry 2: expected symbol=\"hello\", got %v", m["symbol"])
		}
	}

	// Entry 3: invalid symbol
	if multi[3].OK {
		t.Error("entry 3 (main.go:nosuchsymbol): expected ok=false")
	}
	if multi[3].Error == "" {
		t.Error("entry 3 (main.go:nosuchsymbol): expected non-empty error")
	}
}

func TestEditDryRunIncludesDiff(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func hello() {
	println("hello")
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"dry_run":  true,
		"old_text": `println("hello")`,
		"new_text": `println("world")`,
	})
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

	if status, _ := out["status"].(string); status != "dry_run" {
		t.Errorf("expected status=dry_run, got %v", out["status"])
	}

	diff, ok := out["diff"].(string)
	if !ok || diff == "" {
		t.Fatalf("expected non-empty diff field, got %v", out["diff"])
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Mix of undo (global-mutating) and read — should run sequentially
	commands := []dispatch.MultiCmd{
		{Cmd: "read", Args: []string{"a.go"}},
		{Cmd: "undo", Args: nil},
		{Cmd: "read", Args: []string{"a.go"}},
	}

	results := dispatch.DispatchMulti(ctx, db, commands)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}
	// undo may error (nothing to undo), but the point is sequencing
	if !results[0].OK {
		t.Errorf("result 0 (read): expected ok=true, got error=%q", results[0].Error)
	}
	if !results[2].OK {
		t.Errorf("result 2 (read): expected ok=true, got error=%q", results[2].Error)
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Edit: rename oldName to newName via old_text/new_text
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func oldName()",
		"new_text": "func newName()",
	})
	if err != nil {
		t.Fatalf("edit dispatch: %v", err)
	}

	// Verify the edit response has status and no index_error
	raw, _ := json.Marshal(result)
	var editOut map[string]any
	json.Unmarshal(raw, &editOut)
	if editOut["status"] == nil {
		t.Fatalf("expected status field, got: %s", string(raw))
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()


	// Write a new Go file with a function
	_, err := dispatch.Dispatch(ctx, db, "write", []string{"hello.go"}, map[string]any{
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Test edit with dry_run (underscore)
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"dry_run":  true,
		"old_text": "func hello()",
		"new_text": "func goodbye()",
	})
	if err != nil {
		t.Fatalf("edit with dry_run: %v", err)
	}
	data, _ := os.ReadFile(goFile)
	if string(data) != original {
		t.Fatalf("edit dry_run modified file!\nexpected: %q\ngot:     %q", original, string(data))
	}

	// Test edit with dry_run (underscore) — second edit
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()


	// Write with empty content should be refused
	_, err := dispatch.Dispatch(ctx, db, "write", []string{"main.go"}, map[string]any{})
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Ambiguous match without all: true should error
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

func TestWriteInsideAfterGo(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "store.go")
	original := "package store\n\ntype S struct {\n\tdb string\n}\n\nfunc (s *S) Get() string {\n\treturn s.db\n}\n\nfunc (s *S) Put(v string) {\n\t_ = v\n}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Write a method --inside S --after Get
	_, err := dispatch.Dispatch(ctx, db, "write", []string{"store.go"}, map[string]any{
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
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// start_line > end_line should return an error, not panic
	_, err := dispatch.Dispatch(ctx, db, "read", []string{"main.go", "100", "50"}, map[string]any{})
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
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// start_line beyond EOF should return an error, not panic
	_, err := dispatch.Dispatch(ctx, db, "read", []string{"main.go", "99999", "100000"}, map[string]any{})
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
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Empty new_text with old_text should delete the matched text
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func remove() {}\n\n",
		"new_text": "",
	})
	if err != nil {
		t.Fatalf("edit with empty new_text returned error: %v", err)
	}

	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"status"`) {
		t.Fatalf("expected status field, got: %s", string(raw))
	}

	data, _ := os.ReadFile(goFile)
	if strings.Contains(string(data), "remove") {
		t.Fatalf("deleted text still present: %s", string(data))
	}
	if !strings.Contains(string(data), "func keep()") {
		t.Fatalf("kept text missing: %s", string(data))
	}
}

func TestEditDeleteFlag(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc remove() {}\n\nfunc keep() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// --delete with old_text should delete the matched text
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func remove() {}",
		"delete":   true,
	})
	if err != nil {
		t.Fatalf("edit --delete returned error: %v", err)
	}

	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"status"`) {
		t.Fatalf("expected status field, got: %s", string(raw))
	}

	data, _ := os.ReadFile(goFile)
	if strings.Contains(string(data), "remove") {
		t.Fatalf("deleted text still present: %s", string(data))
	}
	if !strings.Contains(string(data), "func keep()") {
		t.Fatalf("kept text missing: %s", string(data))
	}
}

func TestEditDeleteFlagSymbol(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc remove() {}\n\nfunc keep() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// --delete with symbol should delete the symbol and trailing newline
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go", "remove"}, map[string]any{
		"delete": true,
	})
	if err != nil {
		t.Fatalf("edit --delete symbol returned error: %v", err)
	}

	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"status"`) {
		t.Fatalf("expected status field, got: %s", string(raw))
	}

	data, _ := os.ReadFile(goFile)
	if strings.Contains(string(data), "remove") {
		t.Fatalf("deleted symbol still present: %s", string(data))
	}
	if !strings.Contains(string(data), "func keep()") {
		t.Fatalf("kept text missing: %s", string(data))
	}
	// Should not have double blank lines from orphaned trailing newline
	if strings.Contains(string(data), "\n\n\n") {
		t.Errorf("extra blank line left after symbol deletion: %q", string(data))
	}
}

func TestEditLinesFlag(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc a() {}\n\nfunc b() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Edit with --lines instead of --start-line/--end-line
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"lines":    "3:3",
		"new_text": "func replaced() {}\n",
	})
	if err != nil {
		t.Fatalf("edit with --lines: %v", err)
	}
	m, _ := result.(map[string]any)
	if m["status"] != "applied" {
		t.Errorf("expected applied, got %v", m["status"])
	}
	data, _ := os.ReadFile(goFile)
	if !strings.Contains(string(data), "replaced") {
		t.Error("edit via --lines did not apply")
	}
}

func TestEditInsertAt(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc a() {}\n\nfunc b() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Insert before line 3
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"insert_at": 3,
		"new_text":  "func inserted() {}\n",
	})
	if err != nil {
		t.Fatalf("edit --insert-at: %v", err)
	}
	m, _ := result.(map[string]any)
	if m["status"] != "applied" {
		t.Errorf("expected applied, got %v", m["status"])
	}
	data, _ := os.ReadFile(goFile)
	content := string(data)
	if !strings.Contains(content, "inserted") {
		t.Error("inserted text not found")
	}
	// Verify insertion is before func a
	insertIdx := strings.Index(content, "inserted")
	aIdx := strings.Index(content, "func a")
	if insertIdx >= aIdx {
		t.Error("inserted text should appear before func a")
	}
}

func TestEditInsertAtEOF(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc a() {}\n"
	if err := os.WriteFile(goFile, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Insert at EOF (line 4, which is past last content line)
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"insert_at": 4,
		"new_text":  "func appended() {}",
	})
	if err != nil {
		t.Fatalf("edit --insert-at EOF: %v", err)
	}
	m, _ := result.(map[string]any)
	if m["status"] != "applied" {
		t.Errorf("expected applied, got %v", m["status"])
	}
	data, _ := os.ReadFile(goFile)
	if !strings.Contains(string(data), "appended") {
		t.Error("appended text not found at EOF")
	}
}

func TestEditFuzzyWhitespace(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	// File has tabs, but old_text will use spaces
	original := "package main\n\nfunc hello() {\n\tfmt.Println(\"hi\")\n}\n"
	os.WriteFile(goFile, []byte(original), 0644)
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	defer db.Close()
	ctx := context.Background()
	// Without --fuzzy: should fail (spaces vs tabs mismatch)
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func hello() {\n  fmt.Println(\"hi\")\n}",
		"new_text": "func hello() {\n\tfmt.Println(\"hello\")\n}",
	})
	if err == nil {
		t.Fatal("expected not_found error without --fuzzy")
	}

	// With --fuzzy: should succeed
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "func hello() {\n  fmt.Println(\"hi\")\n}",
		"new_text": "func hello() {\n\tfmt.Println(\"hello\")\n}",
		"fuzzy":    true,
	})
	if err != nil {
		t.Fatalf("--fuzzy edit failed: %v", err)
	}
	m, _ := result.(map[string]any)
	if m["status"] != "applied" {
		t.Errorf("expected applied, got %v", m["status"])
	}
	data, _ := os.ReadFile(goFile)
	if !strings.Contains(string(data), "hello") {
		t.Error("fuzzy edit did not apply replacement")
	}
}

func TestEditFuzzyAmbiguousRejects(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	// Two functions with same whitespace-normalized body
	original := "package main\n\nfunc a() {\n\tx := 1\n}\n\nfunc b() {\n\tx := 1\n}\n"
	os.WriteFile(goFile, []byte(original), 0644)
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	defer db.Close()
	ctx := context.Background()
	// Fuzzy match should reject ambiguous (multiple matches)
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "x := 1",
		"new_text": "x := 2",
		"fuzzy":    true,
	})
	if err == nil {
		t.Fatal("expected error for ambiguous fuzzy match")
	}
}

func TestEditFuzzyAndAllMutuallyExclusive(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	os.WriteFile(goFile, []byte("package main\n"), 0644)
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	defer db.Close()
	ctx := context.Background()
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"old_text": "x",
		"new_text": "y",
		"fuzzy":    true,
		"all":      true,
	})
	if err == nil {
		t.Fatal("expected error for --fuzzy + --all")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected mutually exclusive error, got: %v", err)
	}
}

func TestEditMoveAfter(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := "package main\n\nfunc first() {}\n\nfunc second() {}\n\nfunc third() {}\n"
	os.WriteFile(goFile, []byte(original), 0644)
	db := index.NewOnDemand(tmp)
	defer db.Close()
	ctx := context.Background()
	output.SetRoot(db.Root())

	// Move "first" after "third"
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go", "first"}, map[string]any{
		"move_after": "third",
	})
	if err != nil {
		t.Fatalf("move-after: %v", err)
	}
	m, _ := result.(map[string]any)
	if m["status"] != "applied" {
		t.Errorf("expected applied, got %v", m["status"])
	}

	data, _ := os.ReadFile(goFile)
	content := string(data)
	firstIdx := strings.Index(content, "func first")
	secondIdx := strings.Index(content, "func second")
	thirdIdx := strings.Index(content, "func third")

	if firstIdx < 0 || secondIdx < 0 || thirdIdx < 0 {
		t.Fatalf("missing functions in result:\n%s", content)
	}
	if secondIdx >= thirdIdx {
		t.Error("second should still be before third")
	}
	if firstIdx <= thirdIdx {
		t.Error("first should now be after third")
	}
}

func TestEditMoveAfterCrossFileFails(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "a.go"), []byte("package main\n\nfunc fromA() {}\n"), 0644)
	os.WriteFile(filepath.Join(tmp, "b.go"), []byte("package main\n\nfunc fromB() {}\n"), 0644)
	db := index.NewOnDemand(tmp)
	defer db.Close()
	ctx := context.Background()
	output.SetRoot(db.Root())

	// Cross-file move should fail because target is in a different file
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"a.go", "fromA"}, map[string]any{
		"move_after": "fromB",
	})
	// This will fail at target resolution since "fromB" isn't in a.go
	if err == nil {
		t.Fatal("cross-file move should fail")
	}
}

func TestEditEmptyNewTextRequiresEditMode(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
	// Empty new_text with no edit mode should still error
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"main.go"}, map[string]any{
		"new_text": "",
	})
	if err == nil {
		t.Fatal("expected error for edit with empty new_text and no edit mode")
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


// --- Signatures on non-container returns signature line ---

func TestReadSymbol_SignaturesOnFunction_ReturnsSig(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc Execute() {\n\tfmt.Println(\"hi\")\n}\n"), 0644)

	db := index.NewOnDemand(tmp)
	ctx := context.Background()
	defer db.Close()
	output.SetRoot(db.Root())

	result, err := dispatch.Dispatch(ctx, db, "read", []string{"main.go:Execute"}, map[string]any{"signatures": true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	content, _ := m["content"].(string)
	if !strings.Contains(content, "func Execute()") {
		t.Errorf("expected signature to contain func Execute(), got: %s", content)
	}
}

// --- Default budget on search/map ---

func TestRead_DefaultBudgetCap(t *testing.T) {
	tmp := t.TempDir()
	// Create a file larger than 4000 tokens
	var content strings.Builder
	content.WriteString("package main\n\n")
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&content, "func Function%d() {\n\t// implementation %d\n}\n\n", i, i)
	}
	os.WriteFile(filepath.Join(tmp, "big.go"), []byte(content.String()), 0644)

	db := index.NewOnDemand(tmp)
	ctx := context.Background()
	defer db.Close()
	output.SetRoot(db.Root())

	// Read without --budget or --full: should truncate at default 4000 tokens
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"big.go"}, map[string]any{})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if trunc, _ := m["truncated"].(bool); !trunc {
		t.Error("full-file read should truncate at default 4000 token budget")
	}

	// Read with --full: should NOT truncate
	result2, err := dispatch.Dispatch(ctx, db, "read", []string{"big.go"}, map[string]any{"full": true})
	if err != nil {
		t.Fatalf("read --full: %v", err)
	}
	m2, ok := result2.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result2)
	}
	if trunc, _ := m2["truncated"].(bool); trunc {
		t.Error("read --full should NOT truncate")
	}

	// Read with line range: should NOT truncate (explicit range = explicit intent)
	result3, err := dispatch.Dispatch(ctx, db, "read", []string{"big.go"}, map[string]any{"start_line": 1, "end_line": 999})
	if err != nil {
		t.Fatalf("read with line range: %v", err)
	}
	m3, ok := result3.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result3)
	}
	if trunc, _ := m3["truncated"].(bool); trunc {
		t.Error("read with explicit line range should NOT truncate at default budget")
	}
}


func TestBudgetUsedReported(t *testing.T) {
	tmp := t.TempDir()
	// Create a file larger than 100 tokens (~400 chars)
	var content strings.Builder
	content.WriteString("package main\n\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&content, "func Function%d() {\n\t// implementation %d\n}\n\n", i, i)
	}
	os.WriteFile(filepath.Join(tmp, "big.go"), []byte(content.String()), 0644)

	db := index.NewOnDemand(tmp)
	ctx := context.Background()
	defer db.Close()
	output.SetRoot(db.Root())

	// Read with small budget to force truncation
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"big.go"}, map[string]any{"budget": 100})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if trunc, _ := m["truncated"].(bool); !trunc {
		t.Error("read with budget=100 on large file should truncate")
	}
	bu, hasBU := m["budget_used"]
	if !hasBU {
		t.Fatal("truncated read should include budget_used")
	}
	if buInt, ok := bu.(int); !ok || buInt <= 0 {
		t.Errorf("budget_used should be a positive int, got %v (%T)", bu, bu)
	}

	// Read without truncation should NOT have budget_used
	result2, err := dispatch.Dispatch(ctx, db, "read", []string{"big.go"}, map[string]any{"full": true})
	if err != nil {
		t.Fatalf("read --full: %v", err)
	}
	m2 := result2.(map[string]any)
	if _, has := m2["budget_used"]; has {
		t.Error("non-truncated read should NOT include budget_used")
	}
}


// TestInternalCommandsRejected verifies that removed internal command names
// are no longer accepted by Dispatch.
func TestInternalCommandsRejected(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n"), 0644)
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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

	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	ctx := context.Background()
	defer db.Close()
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
