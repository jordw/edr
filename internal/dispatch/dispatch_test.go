package dispatch_test

import (
	"context"
	"encoding/json"
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
	result, err := dispatch.Dispatch(ctx, db, "batch-read", args, map[string]any{})
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

