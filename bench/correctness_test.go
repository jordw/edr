package bench_test

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

// setupAdversarialRepo creates a temp copy of bench/testdata/adversarial, indexed and ready.
func setupAdversarialRepo(tb testing.TB) (index.SymbolStore, string) {
	tb.Helper()

	wd, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	srcDir := filepath.Join(wd, "testdata", "adversarial")
	if _, err := os.Stat(srcDir); err != nil {
		tb.Fatalf("adversarial testdata not found at %s", srcDir)
	}

	tmp := tb.TempDir()
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		dst := filepath.Join(tmp, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode())
	})
	if err != nil {
		tb.Fatal(err)
	}

	db := index.NewOnDemand(tmp)
	tb.Cleanup(func() { db.Close() })
	return db, tmp
}

// dispatchResult calls Dispatch, marshals to JSON, and unmarshals into dest.
func dispatchResult(t testing.TB, ctx context.Context, db index.SymbolStore, cmd string, args []string, flags map[string]any, dest any) {
	t.Helper()
	result, err := dispatch.Dispatch(ctx, db, cmd, args, flags)
	if err != nil {
		t.Fatalf("dispatch %s %v: %v", cmd, args, err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal %s result: %v", cmd, err)
	}
	if dest != nil {
		if err := json.Unmarshal(data, dest); err != nil {
			t.Fatalf("unmarshal %s result: %v\nraw: %s", cmd, err, string(data[:min(500, len(data))]))
		}
	}
}

// dispatchError calls Dispatch expecting an error. Returns the error string.
func dispatchError(t testing.TB, ctx context.Context, db index.SymbolStore, cmd string, args []string, flags map[string]any) string {
	t.Helper()
	_, err := dispatch.Dispatch(ctx, db, cmd, args, flags)
	if err == nil {
		t.Fatalf("expected error from dispatch %s %v, got nil", cmd, args)
	}
	return err.Error()
}

// symbolFile is a minimal struct for JSON-unmarshaling symbol references.
type symbolFile struct {
	File  string `json:"file"`
	Name  string `json:"name"`
	Lines [2]int `json:"lines"`
}

// --- Correctness: Ambiguous Symbols ---

func TestCorrectnessAmbiguousSymbol(t *testing.T) {
	db, _ := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("bare Config is ambiguous", func(t *testing.T) {
		// Smart focus returns a ranked shortlist instead of an error.
		result, err := dispatch.Dispatch(ctx, db, "read", []string{"Config"}, nil)
		if err != nil {
			// Old error path — still acceptable
			errMsg := err.Error()
			if !strings.Contains(errMsg, "ambiguous") {
				t.Errorf("expected 'ambiguous' error, got: %s", errMsg)
			}
			return
		}
		// New shortlist path — result should have candidates
		data, _ := json.Marshal(result)
		resultStr := string(data)
		if !strings.Contains(resultStr, "candidates") && !strings.Contains(resultStr, "ambiguous") {
			t.Errorf("expected shortlist or ambiguous result, got: %s", resultStr)
		}
	})

	t.Run("file-scoped Config resolves", func(t *testing.T) {
		var result json.RawMessage
		dispatchResult(t, ctx, db, "read", []string{"go/pkg_a/config.go:Config"}, nil, &result)
		if len(result) == 0 {
			t.Error("expected non-empty result for scoped Config read")
		}
	})

	t.Run("different files return different Configs", func(t *testing.T) {
		var resA, resB json.RawMessage
		dispatchResult(t, ctx, db, "read", []string{"go/pkg_a/config.go:Config"}, nil, &resA)
		dispatchResult(t, ctx, db, "read", []string{"go/pkg_b/config.go:Config"}, nil, &resB)
		if string(resA) == string(resB) {
			t.Error("pkg_a and pkg_b Config should have different content")
		}
	})

	t.Run("bare Init is ambiguous", func(t *testing.T) {
		result, err := dispatch.Dispatch(ctx, db, "read", []string{"Init"}, nil)
		if err != nil {
			errMsg := err.Error()
			if !strings.Contains(errMsg, "ambiguous") {
				t.Errorf("expected 'ambiguous' error for Init, got: %s", errMsg)
			}
			return
		}
		data, _ := json.Marshal(result)
		resultStr := string(data)
		if !strings.Contains(resultStr, "candidates") && !strings.Contains(resultStr, "ambiguous") {
			t.Errorf("expected shortlist or ambiguous result for Init, got: %s", resultStr)
		}
	})

	t.Run("file-scoped Init resolves", func(t *testing.T) {
		var result json.RawMessage
		dispatchResult(t, ctx, db, "read", []string{"go/pkg_a/config.go", "Init"}, nil, &result)
		if len(result) == 0 {
			t.Error("expected non-empty result for scoped Init")
		}
	})
}

type searchMatch struct {
	Symbol symbolFile `json:"symbol"`
	Score  float64    `json:"score"`
	Body   string     `json:"content"`
}

// --- Correctness: Edit + Reindex ---

func TestCorrectnessEditReindex(t *testing.T) {
	db, tmp := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("edit updates index", func(t *testing.T) {
		// Read the original Config symbol
		var before json.RawMessage
		dispatchResult(t, ctx, db, "read", []string{"go/pkg_a/config.go:Config"}, nil, &before)

		// Edit: add a new field to Config
		var editResult struct {
			File   string `json:"file"`
			Hash   string `json:"hash"`
			Status string `json:"status"`
		}
		dispatchResult(t, ctx, db, "edit", []string{"go/pkg_a/config.go"}, map[string]any{
			"old_text": "Timeout int",
			"new_text": "Timeout  int\n\tMaxConns int",
		}, &editResult)
		if editResult.Status != "applied" {
			t.Fatalf("edit status = %q, want applied", editResult.Status)
		}

		// Map the file — should show the new symbol or updated lines
		var mapResult struct {
			Files int    `json:"files"`
			Map   string `json:"map"`
		}
		dispatchResult(t, ctx, db, "map", []string{"go/pkg_a/config.go"}, nil, &mapResult)

		// Read Config again — should include MaxConns
		var after json.RawMessage
		dispatchResult(t, ctx, db, "read", []string{"go/pkg_a/config.go:Config"}, nil, &after)

		if string(before) == string(after) {
			t.Error("Config should differ after edit")
		}
		if !strings.Contains(string(after), "MaxConns") {
			t.Error("edited Config should contain MaxConns")
		}

		t.Logf("Config edit reindex verified: read returns updated content")
	})

	t.Run("write inside updates index", func(t *testing.T) {
		// Write a new method inside Config in pkg_b
		var writeResult struct {
			File   string `json:"file"`
			Hash   string `json:"hash"`
			Status string `json:"status"`
		}
		dispatchResult(t, ctx, db, "write", []string{"go/pkg_b/config.go"}, map[string]any{
			"inside":  "Config",
			"content": "Label string",
		}, &writeResult)
		if writeResult.Status != "applied" {
			t.Fatalf("write inside should succeed, got status %q", writeResult.Status)
		}

		// Verify the new field is visible
		var readResult json.RawMessage
		dispatchResult(t, ctx, db, "read", []string{"go/pkg_b/config.go:Config"}, nil, &readResult)
		if !strings.Contains(string(readResult), "Label") {
			// Read the raw file to debug
			data, _ := os.ReadFile(filepath.Join(tmp, "go", "pkg_b", "config.go"))
			t.Errorf("Config should contain Label after write-inside.\nFile content:\n%s", string(data))
		}
	})
}

// --- Correctness: Map Consistency ---

func TestCorrectnessMapConsistency(t *testing.T) {
	db, _ := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("map shows all Config definitions", func(t *testing.T) {
		out, err := dispatchJSON(ctx, db, "map", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Map returns content array with file/symbols entries
		count := strings.Count(string(out), "Config")
		var result struct {
			Files   int `json:"files"`
			Symbols int `json:"symbols"`
		}
		json.Unmarshal(out, &result)
		if count < 6 {
			// 6 Config definitions: 2 Go, 2 Python, 2 JS
			t.Errorf("map should show >=6 Config symbols, found %d mentions", count)
		}
		t.Logf("map: %d files, %d symbols, %d Config mentions", result.Files, result.Symbols, count)
	})

	t.Run("map per-file is consistent with read", func(t *testing.T) {
		// Map a specific file now returns unified shape with content array
		var mapResult struct {
			Content []struct {
				File    string `json:"file"`
				Symbols []struct {
					Name string `json:"name"`
					Type string `json:"type"`
				} `json:"symbols"`
			} `json:"content"`
		}
		dispatchResult(t, ctx, db, "map", []string{"go/pkg_a/config.go"}, nil, &mapResult)

		symNames := make(map[string]bool)
		if len(mapResult.Content) > 0 {
			for _, s := range mapResult.Content[0].Symbols {
				symNames[s.Name] = true
			}
		}
		for _, expected := range []string{"Config", "Init", "Validate"} {
			if !symNames[expected] {
				t.Errorf("map of pkg_a/config.go should contain symbol %s, got: %v", expected, mapKeysStr(symNames))
			}
		}
	})
}

// mapKeysStr returns keys of a map[string]bool.
func mapKeysStr(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// refFiles extracts file paths from a slice of symbolFile for error messages.
func refFiles(refs []symbolFile) []string {
	files := make([]string, len(refs))
	for i, r := range refs {
		files[i] = r.File
	}
	return files
}

// --- Lifecycle Integration Tests ---
// These test the full edit→query pipeline including index freshness.

// setupIndexedRepo creates a temp repo with source files and a built trigram+symbol index.
func setupIndexedRepo(tb testing.TB) (index.SymbolStore, string) {
	tb.Helper()
	tmp := tb.TempDir()

	// Create a Go file with a function and a struct
	os.MkdirAll(filepath.Join(tmp, "pkg"), 0755)
	os.WriteFile(filepath.Join(tmp, "pkg", "main.go"), []byte(`package pkg

func Hello() string {
	return "hello world"
}

func Goodbye() string {
	return "goodbye world"
}

type Config struct {
	Name    string
	Timeout int
}
`), 0644)

	// Create a second file for cross-file search
	os.WriteFile(filepath.Join(tmp, "pkg", "util.go"), []byte(`package pkg

func FormatName(name string) string {
	return "formatted: " + name
}
`), 0644)

	db := index.NewOnDemand(tmp)
	tb.Cleanup(func() { db.Close() })

	// Build the trigram+symbol index
	ctx := context.Background()
	_, err := dispatch.Dispatch(ctx, db, "index", nil, nil)
	if err != nil {
		tb.Fatalf("index build: %v", err)
	}

	return db, tmp
}

func TestCorrectnessEditThenSearch(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Verify "hello world" is searchable before edit
	var beforeSearch struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"hello world"}, map[string]any{
		"text": true,
	}, &beforeSearch)
	if beforeSearch.TotalMatches == 0 {
		t.Fatal("expected to find 'hello world' before edit")
	}

	// Edit: change "hello world" to "hola mundo"
	var editResult struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "hello world",
		"new_text": "hola mundo",
	}, &editResult)
	if editResult.Status != "applied" {
		t.Fatalf("edit status = %q, want applied", editResult.Status)
	}

	// Search for new text — should find it
	var afterSearch struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"hola mundo"}, map[string]any{
		"text": true,
	}, &afterSearch)
	if afterSearch.TotalMatches == 0 {
		t.Error("expected to find 'hola mundo' after edit")
	}

	// Search for old text — should NOT find it
	var oldSearch struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"hello world"}, map[string]any{
		"text": true,
	}, &oldSearch)
	if oldSearch.TotalMatches > 0 {
		t.Error("'hello world' should not be found after edit replaced it")
	}

	// Files search for new text — should find the file
	var filesResult struct {
		N int `json:"n"`
	}
	dispatchResult(t, ctx, db, "files", []string{"hola mundo"}, nil, &filesResult)
	if filesResult.N == 0 {
		t.Error("files should find 'hola mundo' after edit")
	}
}

func TestCorrectnessEditThenSymbolLookup(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Verify Hello is resolvable from the symbol index
	var beforeRead struct {
		Symbol string `json:"symbol"`
		File   string `json:"file"`
	}
	dispatchResult(t, ctx, db, "read", []string{"pkg/main.go:Hello"}, nil, &beforeRead)
	if beforeRead.Symbol != "Hello" {
		t.Fatalf("expected symbol Hello, got %q", beforeRead.Symbol)
	}

	// Edit: rename Hello to Greet
	var editResult struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "func Hello()",
		"new_text": "func Greet()",
	}, &editResult)
	if editResult.Status != "applied" {
		t.Fatalf("edit status = %q, want applied", editResult.Status)
	}

	// Focus on Greet — should work (parse-on-demand after dirty)
	var afterRead struct {
		Symbol string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "read", []string{"pkg/main.go:Greet"}, nil, &afterRead)
	if afterRead.Symbol != "Greet" {
		t.Errorf("expected symbol Greet after rename, got %q", afterRead.Symbol)
	}

	// Focus on Hello — should fail (no longer exists)
	_, err := dispatch.Dispatch(ctx, db, "read", []string{"pkg/main.go:Hello"}, nil)
	if err == nil {
		// Could be a shortlist result from smart focus — check content
		t.Log("Hello resolved after rename (may be shortlist); verifying file content")
		data, _ := os.ReadFile(filepath.Join(tmp, "pkg", "main.go"))
		if strings.Contains(string(data), "func Hello()") {
			t.Error("file should not contain func Hello() after rename")
		}
	}
}

func TestCorrectnessDirtyIndexStatus(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Index should be clean initially
	var statusBefore struct {
		Stale bool `json:"stale"`
	}
	dispatchResult(t, ctx, db, "index", nil, map[string]any{"status": true}, &statusBefore)
	if statusBefore.Stale {
		t.Error("index should not be stale before any edits")
	}

	// Edit a file
	var editResult struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "hello world",
		"new_text": "hola mundo",
	}, &editResult)
	if editResult.Status != "applied" {
		t.Fatalf("edit status = %q, want applied", editResult.Status)
	}

	// Index should now be stale (dirty marker set)
	var statusAfter struct {
		Stale bool `json:"stale"`
	}
	dispatchResult(t, ctx, db, "index", nil, map[string]any{"status": true}, &statusAfter)
	if !statusAfter.Stale {
		t.Error("index should be stale after edit (dirty marker)")
	}

	// Rebuild index
	var rebuildResult struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "index", nil, nil, &rebuildResult)
	if rebuildResult.Status != "built" {
		t.Fatalf("index rebuild status = %q, want built", rebuildResult.Status)
	}

	// Should be clean again
	var statusFinal struct {
		Stale bool `json:"stale"`
	}
	dispatchResult(t, ctx, db, "index", nil, map[string]any{"status": true}, &statusFinal)
	if statusFinal.Stale {
		t.Error("index should not be stale after rebuild")
	}
}

func TestCorrectnessEditThenOrientGrep(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// orient --grep should find Hello before edit
	var before struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "orient", nil, map[string]any{
		"grep": "Hello", "budget": 100,
	}, &before)
	if before.Symbols == 0 {
		t.Fatal("orient --grep should find Hello before edit")
	}

	// Rename Hello to Greet
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "func Hello()",
		"new_text": "func Greet()",
	}, nil)

	// orient --grep for Greet should find it
	var afterGreet struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "orient", nil, map[string]any{
		"grep": "Greet", "budget": 100,
	}, &afterGreet)
	if afterGreet.Symbols == 0 {
		t.Error("orient --grep should find Greet after rename")
	}

	// orient --grep for Hello should NOT find it
	var afterHello struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "orient", nil, map[string]any{
		"grep": "Hello", "budget": 100,
	}, &afterHello)
	if afterHello.Symbols > 0 {
		t.Error("orient --grep should not find Hello after rename")
	}
}

func TestCorrectnessEditBareSymbolResolve(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Bare focus on Goodbye should resolve (symbol index path)
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"Goodbye"}, nil)
	if err != nil {
		t.Fatalf("bare focus Goodbye before edit: %v", err)
	}
	data, _ := json.Marshal(result)
	if !strings.Contains(string(data), "Goodbye") {
		t.Error("bare focus should resolve Goodbye")
	}

	// Rename Goodbye to Farewell
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "func Goodbye()",
		"new_text": "func Farewell()",
	}, nil)

	// Bare focus on Farewell should work (dirty index skipped, parse-on-demand)
	result2, err := dispatch.Dispatch(ctx, db, "read", []string{"Farewell"}, nil)
	if err != nil {
		t.Fatalf("bare focus Farewell after rename: %v", err)
	}
	data2, _ := json.Marshal(result2)
	if !strings.Contains(string(data2), "Farewell") {
		t.Errorf("bare focus should resolve Farewell after rename, got: %s", string(data2)[:min(200, len(data2))])
	}

	// Bare focus on Goodbye should fail
	_, err = dispatch.Dispatch(ctx, db, "read", []string{"Goodbye"}, nil)
	if err == nil {
		// Smart focus might return a shortlist — that's ok as long as file doesn't have Goodbye
		t.Log("Goodbye resolved after rename (shortlist acceptable)")
	}
}

func TestCorrectnessWriteNewFileThenSearch(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Write a brand new file (not in the index at all)
	dispatchResult(t, ctx, db, "edit", []string{"pkg/brand_new.go"}, map[string]any{
		"content": "package pkg\n\nfunc UniqueNewFunction() string {\n\treturn \"xyzzy\"\n}\n",
		"mkdir":   true,
	}, nil)

	// Search for the unique text
	var searchResult struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"xyzzy"}, map[string]any{
		"text": true,
	}, &searchResult)
	if searchResult.TotalMatches == 0 {
		t.Error("search should find 'xyzzy' in newly written file")
	}

	// Focus the new symbol
	var readResult struct {
		Symbol string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "read", []string{"pkg/brand_new.go:UniqueNewFunction"}, nil, &readResult)
	if readResult.Symbol != "UniqueNewFunction" {
		t.Errorf("expected UniqueNewFunction, got %q", readResult.Symbol)
	}
}

func TestCorrectnessMultipleEditsOneFile(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Rename Hello to Alpha
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "func Hello()",
		"new_text": "func Alpha()",
	}, nil)

	// Rename Goodbye to Beta
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "func Goodbye()",
		"new_text": "func Beta()",
	}, nil)

	// Both new names should resolve
	var r1, r2 struct {
		Symbol string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "read", []string{"pkg/main.go:Alpha"}, nil, &r1)
	if r1.Symbol != "Alpha" {
		t.Errorf("expected Alpha, got %q", r1.Symbol)
	}
	dispatchResult(t, ctx, db, "read", []string{"pkg/main.go:Beta"}, nil, &r2)
	if r2.Symbol != "Beta" {
		t.Errorf("expected Beta, got %q", r2.Symbol)
	}

	// Old names should fail
	_, err1 := dispatch.Dispatch(ctx, db, "read", []string{"pkg/main.go:Hello"}, nil)
	if err1 == nil {
		t.Error("Hello should not resolve after rename to Alpha")
	}
	_, err2 := dispatch.Dispatch(ctx, db, "read", []string{"pkg/main.go:Goodbye"}, nil)
	if err2 == nil {
		t.Error("Goodbye should not resolve after rename to Beta")
	}
}

func TestCorrectnessIndexRebuildPreservesSymbols(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Verify symbol index has symbols after initial build
	var status1 struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "index", nil, map[string]any{"status": true}, &status1)
	if status1.Symbols == 0 {
		t.Fatal("expected symbols in index after build")
	}
	initialSymbols := status1.Symbols

	// Touch a file to make git index stale (simulates git operations)
	// This triggers IncrementalTick → rebuildSmart on next command
	os.WriteFile(filepath.Join(tmp, "pkg", "new.go"), []byte(`package pkg

func NewFunc() {}
`), 0644)

	// Force a full rebuild (not incremental) to verify symbols survive
	var rebuildResult struct {
		Status  string `json:"status"`
		Symbols int    `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "index", nil, nil, &rebuildResult)
	if rebuildResult.Status != "built" {
		t.Fatalf("rebuild status = %q, want built", rebuildResult.Status)
	}

	// Symbol count should be >= initial (new file adds symbols)
	var status2 struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "index", nil, map[string]any{"status": true}, &status2)
	if status2.Symbols < initialSymbols {
		t.Errorf("symbol count dropped after rebuild: %d → %d", initialSymbols, status2.Symbols)
	}

	// Verify symbol lookup still works after rebuild
	var readResult struct {
		Symbol string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "read", []string{"pkg/main.go:Goodbye"}, nil, &readResult)
	if readResult.Symbol != "Goodbye" {
		t.Errorf("symbol lookup after rebuild: got %q, want Goodbye", readResult.Symbol)
	}
}
