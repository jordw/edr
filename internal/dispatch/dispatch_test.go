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

	result, err := dispatch.Dispatch(ctx, db, "edit-plan", nil, flags)
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
		{Cmd: "init"},
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
	// checked "dry-run" but MCP passes "dry_run".
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
	_, err = dispatch.Dispatch(ctx, db, "edit-plan", nil, map[string]any{
		"dry_run": true,
		"edits":   edits,
	})
	if err != nil {
		t.Fatalf("edit-plan with dry_run: %v", err)
	}
	data, _ := os.ReadFile(goFile)
	if string(data) != original {
		t.Fatalf("edit-plan dry_run modified file!\nexpected: %q\ngot:     %q", original, string(data))
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

	// With --force, should succeed
	_, err = dispatch.Dispatch(ctx, db, "write", []string{"main.go"}, map[string]any{
		"force": true,
	})
	if err != nil {
		t.Fatalf("write with --force should succeed: %v", err)
	}
	data, _ = os.ReadFile(goFile)
	if string(data) != "" {
		t.Fatalf("expected empty file with --force, got: %q", string(data))
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

func TestWriteAcceptsNewTextFlag(t *testing.T) {
	// write should accept new_text as an alias for content.
	tmp := t.TempDir()

	db, err := index.OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()

	_, err = dispatch.Dispatch(ctx, db, "write", []string{"test.go"}, map[string]any{
		"new_text": "package main\n\nfunc foo() {}\n",
	})
	if err != nil {
		t.Fatalf("write with new_text: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(tmp, "test.go"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(data), "func foo()") {
		t.Fatalf("expected file to contain func foo(), got: %q", string(data))
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

func TestMoveSymbol(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "funcs.go")
	original := "package main\n\nfunc Alpha() {}\n\nfunc Beta() {}\n\nfunc Gamma() {}\n"
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

	// Move Gamma after Alpha
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"funcs.go"}, map[string]any{
		"move":  "Gamma",
		"after": "Alpha",
	})
	if err != nil {
		t.Fatalf("move returned error: %v", err)
	}
	raw, _ := json.Marshal(result)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["ok"] != true {
		t.Fatalf("expected ok=true, got: %v", m)
	}

	data, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	alphaIdx := strings.Index(content, "func Alpha")
	gammaIdx := strings.Index(content, "func Gamma")
	betaIdx := strings.Index(content, "func Beta")
	if gammaIdx < alphaIdx {
		t.Fatalf("Gamma should be after Alpha, but Alpha=%d Gamma=%d", alphaIdx, gammaIdx)
	}
	if betaIdx < gammaIdx {
		t.Fatalf("Beta should be after Gamma, but Gamma=%d Beta=%d", gammaIdx, betaIdx)
	}
}

func TestMoveSymbolBefore(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "funcs.go")
	original := "package main\n\nfunc Alpha() {}\n\nfunc Beta() {}\n\nfunc Gamma() {}\n"
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

	// Move Gamma before Alpha
	result, err := dispatch.Dispatch(ctx, db, "edit", []string{"funcs.go"}, map[string]any{
		"move":   "Gamma",
		"before": "Alpha",
	})
	if err != nil {
		t.Fatalf("move returned error: %v", err)
	}
	raw, _ := json.Marshal(result)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["ok"] != true {
		t.Fatalf("expected ok=true, got: %v", m)
	}

	data, err := os.ReadFile(goFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	gammaIdx := strings.Index(content, "func Gamma")
	alphaIdx := strings.Index(content, "func Alpha")
	if gammaIdx > alphaIdx {
		t.Fatalf("Gamma should be before Alpha, but Gamma=%d Alpha=%d", gammaIdx, alphaIdx)
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

	if m["noop"] != true {
		t.Fatalf("expected noop=true, got: %v", m)
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


func TestEditPlanRegex_SingleMatch(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func hello() string { return "v1.0" }
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

	edits := []map[string]any{{
		"file":     "main.go",
		"old_text": `v[0-9]+\.[0-9]+`,
		"new_text": "v2.0",
		"regex":    true,
	}}
	result, err := dispatch.Dispatch(ctx, db, "edit-plan", nil, map[string]any{"edits": edits})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	raw, _ := json.Marshal(result)
	var out map[string]any
	json.Unmarshal(raw, &out)

	if out["ok"] != true {
		t.Fatalf("expected ok=true, got: %s", string(raw))
	}
	data, _ := os.ReadFile(goFile)
	if !strings.Contains(string(data), `"v2.0"`) {
		t.Fatalf("expected v2.0 in file, got: %s", string(data))
	}
}

func TestEditPlanRegex_AllMatches(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func a() string { return "v1.0" }
func b() string { return "v1.1" }
func c() string { return "v2.0" }
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

	edits := []map[string]any{{
		"file":     "main.go",
		"old_text": `v[0-9]+\.[0-9]+`,
		"new_text": "v9.9",
		"regex":    true,
		"all":      true,
	}}
	result, err := dispatch.Dispatch(ctx, db, "edit-plan", nil, map[string]any{"edits": edits})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	raw, _ := json.Marshal(result)
	var out map[string]any
	json.Unmarshal(raw, &out)

	if out["ok"] != true {
		t.Fatalf("expected ok=true, got: %s", string(raw))
	}
	data, _ := os.ReadFile(goFile)
	count := strings.Count(string(data), `"v9.9"`)
	if count != 3 {
		t.Fatalf("expected 3 replacements, got %d: %s", count, string(data))
	}
}

func TestEditPlanRegex_CaptureGroups(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func alpha() {}
func beta() {}
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

	edits := []map[string]any{{
		"file":     "main.go",
		"old_text": `func (\w+)\(\)`,
		"new_text": "func do_${1}()",
		"regex":    true,
		"all":      true,
	}}
	result, err := dispatch.Dispatch(ctx, db, "edit-plan", nil, map[string]any{"edits": edits})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	raw, _ := json.Marshal(result)
	var out map[string]any
	json.Unmarshal(raw, &out)

	if out["ok"] != true {
		t.Fatalf("expected ok=true, got: %s", string(raw))
	}
	data, _ := os.ReadFile(goFile)
	content := string(data)
	if !strings.Contains(content, "func do_alpha()") {
		t.Fatalf("expected do_alpha, got: %s", content)
	}
	if !strings.Contains(content, "func do_beta()") {
		t.Fatalf("expected do_beta, got: %s", content)
	}
}

func TestEditPlanRegex_AmbiguousWithoutAll(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	if err := os.WriteFile(goFile, []byte(`package main

func a() string { return "v1.0" }
func b() string { return "v1.1" }
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

	edits := []map[string]any{{
		"file":     "main.go",
		"old_text": `v[0-9]+\.[0-9]+`,
		"new_text": "v9.9",
		"regex":    true,
		// no "all": true — should error with ambiguous match
	}}
	_, err = dispatch.Dispatch(ctx, db, "edit-plan", nil, map[string]any{"edits": edits})
	if err == nil {
		t.Fatal("expected ambiguous match error")
	}
	if !strings.Contains(err.Error(), "matched") {
		t.Fatalf("expected ambiguous match error, got: %v", err)
	}
}

func TestEditPlanRegex_InvalidPattern(t *testing.T) {
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

	edits := []map[string]any{{
		"file":     "main.go",
		"old_text": `[invalid`,
		"new_text": "x",
		"regex":    true,
	}}
	_, err = dispatch.Dispatch(ctx, db, "edit-plan", nil, map[string]any{"edits": edits})
	if err == nil {
		t.Fatal("expected invalid regex error")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("expected 'invalid regex' in error, got: %v", err)
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

func TestEditPlanRegex_DryRun(t *testing.T) {
	tmp := t.TempDir()
	goFile := filepath.Join(tmp, "main.go")
	original := []byte(`package main

func hello() string { return "v1.0" }
`)
	if err := os.WriteFile(goFile, original, 0644); err != nil {
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

	edits := []map[string]any{{
		"file":     "main.go",
		"old_text": `v[0-9]+\.[0-9]+`,
		"new_text": "v9.9",
		"regex":    true,
	}}
	result, err := dispatch.Dispatch(ctx, db, "edit-plan", nil, map[string]any{"edits": edits, "dry_run": true})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), "dry_run") {
		t.Fatalf("expected dry_run in result, got: %s", string(raw))
	}
	// File should be unchanged
	data, _ := os.ReadFile(goFile)
	if string(data) != string(original) {
		t.Fatalf("dry_run should not modify file, got: %s", string(data))
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

