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

