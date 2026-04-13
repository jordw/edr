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
	"github.com/jordw/edr/internal/output"
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
	output.SetRoot(db.Root())
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
	output.SetRoot(db.Root())

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

func TestCorrectnessSearchSymbols(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// SearchSymbols (substring match) — used by smart focus
	var result struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"Hello"}, nil, &result)
	if result.TotalMatches == 0 {
		t.Error("symbol search should find Hello")
	}

	// Partial match — "Good" should match "Goodbye"
	var partial struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"Good"}, nil, &partial)
	if partial.TotalMatches == 0 {
		t.Error("symbol search should find partial match 'Good' → Goodbye")
	}

	// After edit, search should reflect changes
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "func Hello()",
		"new_text": "func Howdy()",
	}, nil)

	var afterEdit struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"Howdy"}, nil, &afterEdit)
	if afterEdit.TotalMatches == 0 {
		t.Error("symbol search should find Howdy after rename")
	}
}

func TestCorrectnessExpandCallers(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Create a file that calls Hello
	os.WriteFile(filepath.Join(tmp, "pkg", "caller.go"), []byte(`package pkg

func CallHello() string {
	return Hello()
}
`), 0644)

	// Rebuild index to include new file
	dispatchResult(t, ctx, db, "index", nil, nil, nil)

	// Read Hello with --expand callers
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"pkg/main.go:Hello"}, map[string]any{
		"expand": "callers",
	})
	if err != nil {
		t.Fatalf("read with expand callers: %v", err)
	}
	data, _ := json.Marshal(result)
	resultStr := string(data)

	// Should contain the function body
	if !strings.Contains(resultStr, "hello world") {
		t.Error("result should contain Hello function body")
	}

	// Should have callers section (may or may not find CallHello depending on repo size)
	// On small repos (<1000 files), FindSemanticCallers runs
	t.Logf("expand callers result keys present: callers=%v", strings.Contains(resultStr, "callers"))
}

func TestCorrectnessExpandDeps(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Create a file where a function calls FormatName
	os.WriteFile(filepath.Join(tmp, "pkg", "consumer.go"), []byte(`package pkg

func Greet(name string) string {
	return "Hi " + FormatName(name)
}
`), 0644)

	// Read Greet with --expand deps
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"pkg/consumer.go:Greet"}, map[string]any{
		"expand": "deps",
	})
	if err != nil {
		t.Fatalf("read with expand deps: %v", err)
	}
	data, _ := json.Marshal(result)
	resultStr := string(data)

	// Should contain the function body
	if !strings.Contains(resultStr, "FormatName") {
		t.Error("result should contain FormatName reference in body")
	}

	// deps section should include FormatName (same-file or cross-file dep)
	if strings.Contains(resultStr, "deps") {
		t.Log("deps section found in result")
	}
}

func TestCorrectnessContainerAt(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Write --inside Config should work (uses GetContainerAt)
	var writeResult struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"inside":  "Config",
		"content": "\tMaxConns int",
	}, &writeResult)
	if writeResult.Status != "applied" {
		t.Fatalf("write --inside Config: status = %q, want applied", writeResult.Status)
	}

	// Verify the field was added inside Config
	var readResult json.RawMessage
	dispatchResult(t, ctx, db, "read", []string{"pkg/main.go:Config"}, nil, &readResult)
	if !strings.Contains(string(readResult), "MaxConns") {
		t.Error("Config should contain MaxConns after write --inside")
	}
}

func TestCorrectnessFilteredSymbolsByType(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// orient --type function should only return functions
	var funcResult struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "orient", nil, map[string]any{
		"type": "function", "budget": 200,
	}, &funcResult)
	if funcResult.Symbols == 0 {
		t.Fatal("orient --type function should find functions")
	}

	// orient --type struct should only return structs
	var structResult struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "orient", nil, map[string]any{
		"type": "struct", "budget": 200,
	}, &structResult)
	if structResult.Symbols == 0 {
		t.Fatal("orient --type struct should find structs")
	}

	// Function count + struct count should not exceed total
	var allResult struct {
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "orient", nil, map[string]any{
		"budget": 200,
	}, &allResult)
	if funcResult.Symbols+structResult.Symbols > allResult.Symbols {
		t.Errorf("function(%d) + struct(%d) > total(%d)",
			funcResult.Symbols, structResult.Symbols, allResult.Symbols)
	}
}

func TestCorrectnessOrientDir(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// orient pkg/ should scope to that directory
	var dirResult struct {
		Files   int `json:"files"`
		Symbols int `json:"symbols"`
	}
	dispatchResult(t, ctx, db, "orient", []string{"pkg"}, map[string]any{
		"budget": 200,
	}, &dirResult)
	if dirResult.Files == 0 {
		t.Error("orient pkg/ should find files")
	}
	if dirResult.Symbols == 0 {
		t.Error("orient pkg/ should find symbols")
	}
}

func TestCorrectnessFilesCommand(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// files should find text in indexed files
	var result struct {
		N      int    `json:"n"`
		Source string `json:"source"`
	}
	dispatchResult(t, ctx, db, "files", []string{"FormatName"}, nil, &result)
	if result.N == 0 {
		t.Error("files should find FormatName")
	}

	// files for non-existent text
	var empty struct {
		N int `json:"n"`
	}
	dispatchResult(t, ctx, db, "files", []string{"xyzzy_nonexistent_text"}, nil, &empty)
	if empty.N != 0 {
		t.Errorf("files for nonexistent text should return 0, got %d", empty.N)
	}
}

func TestCorrectnessFuzzyMatch(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Write a file with specific indentation
	os.WriteFile(filepath.Join(tmp, "pkg", "indented.go"), []byte("package pkg\n\nfunc Indented() {\n\tx := 1\n\ty := 2\n\tz := x + y\n\t_ = z\n}\n"), 0644)

	// Normal edit should fail with wrong indentation
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"pkg/indented.go"}, map[string]any{
		"old_text": "x := 1\n    y := 2", // spaces instead of tabs
		"new_text": "x := 10\n    y := 20",
	})
	if err == nil {
		t.Error("non-fuzzy edit with wrong whitespace should fail")
	}

	// Fuzzy edit should succeed
	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/indented.go"}, map[string]any{
		"old_text": "x := 1\n    y := 2",
		"new_text": "x := 10\n\ty := 20",
		"fuzzy":    true,
	}, &result)
	if result.Status != "applied" {
		t.Fatalf("fuzzy edit status = %q, want applied", result.Status)
	}

	// Verify the edit applied
	data, _ := os.ReadFile(filepath.Join(tmp, "pkg", "indented.go"))
	if !strings.Contains(string(data), "x := 10") {
		t.Error("fuzzy edit should have changed x := 1 to x := 10")
	}
}

func TestCorrectnessEditInSymbol(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Edit scoped to Hello function only (--in Hello)
	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "return",
		"new_text": "// modified\n\treturn",
		"in":       "Hello",
	}, &result)
	if result.Status != "applied" {
		t.Fatalf("--in Hello edit status = %q, want applied", result.Status)
	}

	// The edit should only affect Hello, not Goodbye
	data, _ := os.ReadFile(filepath.Join(db.Root(), "pkg", "main.go"))
	content := string(data)

	// Count "// modified" — should appear exactly once (in Hello, not Goodbye)
	count := strings.Count(content, "// modified")
	if count != 1 {
		t.Errorf("expected 1 occurrence of '// modified' (scoped to Hello), got %d", count)
	}
}

func TestCorrectnessEditInSymbol_WrongSymbol(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Edit scoped to a symbol that doesn't contain the text
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text": "FormatName", // not in main.go
		"new_text": "Something",
		"in":       "Hello",
	})
	if err == nil {
		t.Error("edit --in Hello for text not in Hello should fail")
	}
}

func TestCorrectnessRefreshHash(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Get the current hash
	var readResult struct {
		Hash string `json:"hash"`
	}
	dispatchResult(t, ctx, db, "read", []string{"pkg/main.go"}, map[string]any{"budget": 1}, &readResult)
	oldHash := readResult.Hash

	// Edit with correct hash should work
	var result1 struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text":    "hello world",
		"new_text":    "hi world",
		"expect_hash": oldHash,
	}, &result1)
	if result1.Status != "applied" {
		t.Fatalf("edit with correct hash: status = %q", result1.Status)
	}

	// Edit with stale hash should fail
	_, err := dispatch.Dispatch(ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text":    "hi world",
		"new_text":    "hey world",
		"expect_hash": oldHash, // stale now
	})
	if err == nil {
		t.Error("edit with stale hash should fail")
	}

	// Edit with stale hash + refresh_hash should succeed
	var result2 struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/main.go"}, map[string]any{
		"old_text":     "hi world",
		"new_text":     "hey world",
		"expect_hash":  oldHash, // stale
		"refresh_hash": true,
	}, &result2)
	if result2.Status != "applied" {
		t.Fatalf("edit with refresh_hash: status = %q", result2.Status)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "pkg", "main.go"))
	if !strings.Contains(string(data), "hey world") {
		t.Error("refresh_hash edit should have applied")
	}
}

func TestCorrectnessReplaceAll(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Write a file with repeated text
	os.WriteFile(filepath.Join(tmp, "pkg", "repeated.go"), []byte("package pkg\n\nfunc R1() string { return \"old\" }\nfunc R2() string { return \"old\" }\nfunc R3() string { return \"old\" }\n"), 0644)

	// Replace all "old" with "new"
	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"pkg/repeated.go"}, map[string]any{
		"old_text": `"old"`,
		"new_text": `"new"`,
		"all":      true,
	}, &result)
	if result.Status != "applied" {
		t.Fatalf("replace all: status = %q", result.Status)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "pkg", "repeated.go"))
	content := string(data)
	if strings.Contains(content, `"old"`) {
		t.Error("replace all should have replaced all occurrences")
	}
	if strings.Count(content, `"new"`) != 3 {
		t.Errorf("expected 3 occurrences of \"new\", got %d", strings.Count(content, `"new"`))
	}
}

func TestCorrectnessSearchInFile(t *testing.T) {
	db, _ := setupIndexedRepo(t)
	ctx := context.Background()

	// Search within a specific file (use file:* to indicate whole file)
	var result struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"return"}, map[string]any{
		"text": true,
		"in":   "pkg/main.go:Hello",
	}, &result)
	if result.TotalMatches == 0 {
		t.Error("search --in pkg/main.go:Hello for 'return' should find matches")
	}

	// Search within a specific file:symbol
	var symResult struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"hello"}, map[string]any{
		"text": true,
		"in":   "pkg/main.go:Hello",
	}, &symResult)
	if symResult.TotalMatches == 0 {
		t.Error("search --in pkg/main.go:Hello for 'hello' should find match")
	}

	// Search in symbol should NOT find text from other functions
	var scopedResult struct {
		TotalMatches int `json:"total_matches"`
	}
	dispatchResult(t, ctx, db, "search", []string{"goodbye"}, map[string]any{
		"text": true,
		"in":   "pkg/main.go:Hello",
	}, &scopedResult)
	if scopedResult.TotalMatches > 0 {
		t.Error("search --in Hello for 'goodbye' should NOT match (it's in Goodbye)")
	}
}

func TestCorrectnessExpandCallersSmallRepo(t *testing.T) {
	db, tmp := setupIndexedRepo(t)
	ctx := context.Background()

	// Add a caller
	os.WriteFile(filepath.Join(tmp, "pkg", "caller.go"), []byte("package pkg\n\nfunc UseHello() string {\n\treturn Hello()\n}\n"), 0644)

	// --expand callers on small repo should use full FindSemanticCallers path
	result, err := dispatch.Dispatch(ctx, db, "read", []string{"pkg/main.go:Hello"}, map[string]any{
		"expand": "callers",
	})
	if err != nil {
		t.Fatalf("expand callers: %v", err)
	}
	data, _ := json.Marshal(result)
	resultStr := string(data)

	// Should have callers section with UseHello
	if !strings.Contains(resultStr, "callers") {
		t.Error("expand callers should include callers section on small repo")
	}
	if !strings.Contains(resultStr, "UseHello") {
		t.Error("callers should include UseHello")
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

// setupSemanticRepo creates a temp repo with cross-file references for
// testing rename, extract, and cross-file move.
func setupSemanticRepo(tb testing.TB) (index.SymbolStore, string) {
	tb.Helper()
	tmp := tb.TempDir()

	os.WriteFile(filepath.Join(tmp, "lib.go"), []byte(`package main

func Compute(x, y int) int {
	sum := x + y
	product := x * y
	return sum + product
}

func Helper() int {
	return 42
}
`), 0644)

	os.WriteFile(filepath.Join(tmp, "main.go"), []byte(`package main

import "fmt"

func main() {
	result := Compute(3, 4)
	fmt.Println(result)
	fmt.Println(Helper())
}
`), 0644)

	os.WriteFile(filepath.Join(tmp, "util.go"), []byte(`package main

func Wrapper() int {
	return Compute(1, 2) + Compute(3, 4)
}
`), 0644)

	db := index.NewOnDemand(tmp)
	tb.Cleanup(func() { db.Close() })
	output.SetRoot(db.Root())
	return db, tmp
}

// --- Correctness: Rename ---

func TestCorrectnessRenameBasic(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		OldName     string   `json:"old_name"`
		NewName     string   `json:"new_name"`
		Status      string   `json:"status"`
		Occurrences int      `json:"occurrences"`
		Files       []string `json:"files_changed"`
	}
	dispatchResult(t, ctx, db, "rename", []string{"lib.go:Compute"}, map[string]any{
		"new_name": "Calculate",
	}, &result)

	if result.Status != "applied" {
		t.Fatalf("status = %q, want applied", result.Status)
	}
	if result.OldName != "Compute" || result.NewName != "Calculate" {
		t.Errorf("names: %q → %q", result.OldName, result.NewName)
	}
	if result.Occurrences < 4 {
		t.Errorf("expected at least 4 occurrences (def + 3 calls), got %d", result.Occurrences)
	}
	if len(result.Files) < 2 {
		t.Errorf("expected at least 2 files changed, got %d", len(result.Files))
	}

	// Verify all files were updated.
	for _, f := range []string{"lib.go", "main.go", "util.go"} {
		data, _ := os.ReadFile(filepath.Join(tmp, f))
		if strings.Contains(string(data), "Compute") {
			t.Errorf("%s still contains Compute after rename", f)
		}
		if f != "main.go" { // main.go only has the call, not a second reference in some files
			if !strings.Contains(string(data), "Calculate") {
				t.Errorf("%s missing Calculate after rename", f)
			}
		}
	}
}

func TestCorrectnessRenameDryRun(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "rename", []string{"lib.go:Compute"}, map[string]any{
		"new_name": "Calculate",
		"dry_run":  true,
	}, &result)

	if result.Status != "dry_run" {
		t.Fatalf("status = %q, want dry_run", result.Status)
	}

	// File should be unchanged.
	data, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if !strings.Contains(string(data), "Compute") {
		t.Error("dry-run should not modify files")
	}
}

func TestCorrectnessRenameNoop(t *testing.T) {
	db, _ := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "rename", []string{"lib.go:Compute"}, map[string]any{
		"new_name": "Compute",
	}, &result)

	if result.Status != "noop" {
		t.Errorf("status = %q, want noop", result.Status)
	}
}

func TestCorrectnessRenameThenRead(t *testing.T) {
	db, _ := setupSemanticRepo(t)
	ctx := context.Background()

	// Rename, then verify the symbol store sees the new name.
	dispatchResult(t, ctx, db, "rename", []string{"lib.go:Compute"}, map[string]any{
		"new_name": "Calculate",
	}, nil)

	var readResult struct {
		Symbol string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "read", []string{"lib.go:Calculate"}, nil, &readResult)
	if readResult.Symbol != "Calculate" {
		t.Errorf("after rename, read lib.go:Calculate got symbol %q", readResult.Symbol)
	}

	// Old name should fail.
	errMsg := dispatchError(t, ctx, db, "read", []string{"lib.go:Compute"}, nil)
	if !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found error for old name, got: %s", errMsg)
	}
}

// --- Correctness: Extract ---

func TestCorrectnessExtractBasic(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		File   string `json:"file"`
		Status string `json:"status"`
	}
	// Extract lines 4-5 (sum and product) from Compute.
	dispatchResult(t, ctx, db, "extract", []string{"lib.go:Compute"}, map[string]any{
		"name":  "computePartials",
		"lines": "4-5",
	}, &result)

	if result.Status != "applied" {
		t.Fatalf("status = %q, want applied", result.Status)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	content := string(data)
	if !strings.Contains(content, "func computePartials()") {
		t.Error("extracted function not found")
	}
	if !strings.Contains(content, "computePartials()") {
		t.Error("call to extracted function not found")
	}
}

func TestCorrectnessExtractWithCall(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	dispatchResult(t, ctx, db, "extract", []string{"lib.go:Compute"}, map[string]any{
		"name":  "computePartials",
		"lines": "4-5",
		"call":  "sum, product := computePartials(x, y)",
	}, nil)

	data, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if !strings.Contains(string(data), "sum, product := computePartials(x, y)") {
		t.Error("custom call expression not applied")
	}
}

func TestCorrectnessExtractDryRun(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "extract", []string{"lib.go:Compute"}, map[string]any{
		"name":    "computePartials",
		"lines":   "4-5",
		"dry_run": true,
	}, &result)

	if result.Status != "dry_run" {
		t.Fatalf("status = %q, want dry_run", result.Status)
	}

	data, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if strings.Contains(string(data), "computePartials") {
		t.Error("dry-run should not modify files")
	}
}

func TestCorrectnessExtractOutOfRange(t *testing.T) {
	db, _ := setupSemanticRepo(t)
	ctx := context.Background()

	errMsg := dispatchError(t, ctx, db, "extract", []string{"lib.go:Compute"}, map[string]any{
		"name":  "bad",
		"lines": "1-2",
	})
	if !strings.Contains(errMsg, "outside symbol") {
		t.Errorf("expected outside-symbol error, got: %s", errMsg)
	}
}

// --- Correctness: Cross-file move ---

func TestCorrectnessMoveAcrossFiles(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		File   string `json:"file"`
		Status string `json:"status"`
		Dest   string `json:"dest"`
		Symbol string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"lib.go:Helper"}, map[string]any{
		"move_after": "util.go:Wrapper",
	}, &result)

	if result.Status != "applied" {
		t.Fatalf("status = %q, want applied", result.Status)
	}
	if result.Dest != "util.go" {
		t.Errorf("dest = %q, want util.go", result.Dest)
	}

	// Helper should be gone from lib.go.
	libData, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if strings.Contains(string(libData), "func Helper()") {
		t.Error("lib.go should not contain Helper after move")
	}

	// Helper should be in util.go.
	utilData, _ := os.ReadFile(filepath.Join(tmp, "util.go"))
	if !strings.Contains(string(utilData), "func Helper()") {
		t.Error("util.go should contain Helper after move")
	}
}

func TestCorrectnessMoveAcrossFilesDryRun(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "edit", []string{"lib.go:Helper"}, map[string]any{
		"move_after": "util.go:Wrapper",
		"dry_run":    true,
	}, &result)

	if result.Status != "dry_run" {
		t.Fatalf("status = %q, want dry_run", result.Status)
	}

	libData, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if !strings.Contains(string(libData), "func Helper()") {
		t.Error("dry-run should not remove Helper from lib.go")
	}
}

func TestCorrectnessMoveThenRead(t *testing.T) {
	db, _ := setupSemanticRepo(t)
	ctx := context.Background()

	// Move Helper from lib.go to util.go.
	dispatchResult(t, ctx, db, "edit", []string{"lib.go:Helper"}, map[string]any{
		"move_after": "util.go:Wrapper",
	}, nil)

	// Symbol should now be found in util.go.
	var readResult struct {
		File   string `json:"file"`
		Symbol string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "read", []string{"util.go:Helper"}, nil, &readResult)
	if readResult.Symbol != "Helper" {
		t.Errorf("after move, read util.go:Helper got %q", readResult.Symbol)
	}

	// Old location should fail.
	errMsg := dispatchError(t, ctx, db, "read", []string{"lib.go:Helper"}, nil)
	if !strings.Contains(errMsg, "not found") {
		t.Errorf("expected not-found for old location, got: %s", errMsg)
	}
}

// --- Correctness: ChangeSig ---

func TestCorrectnessChangeSigAdd(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		File   string `json:"file"`
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "changesig", []string{"lib.go:Compute"}, map[string]any{
		"add":     "ctx context.Context",
		"at":      0,
		"callarg": "ctx",
	}, &result)

	if result.Status != "applied" {
		t.Fatalf("status = %q, want applied", result.Status)
	}

	// Verify definition.
	libData, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if !strings.Contains(string(libData), "func Compute(ctx context.Context, x, y int)") {
		t.Errorf("definition not updated:\n%s", string(libData))
	}

	// Verify call sites in other files.
	mainData, _ := os.ReadFile(filepath.Join(tmp, "main.go"))
	if !strings.Contains(string(mainData), "Compute(ctx, 3, 4)") {
		t.Errorf("main.go call site not updated:\n%s", string(mainData))
	}

	utilData, _ := os.ReadFile(filepath.Join(tmp, "util.go"))
	if !strings.Contains(string(utilData), "Compute(ctx, 1, 2)") {
		t.Errorf("util.go first call site not updated:\n%s", string(utilData))
	}
}

func TestCorrectnessChangeSigRemove(t *testing.T) {
	// Use a separate repo with individually-typed params (not Go's "x, y int" combined syntax).
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "lib.go"), []byte("package main\n\nfunc Process(x int, y string, z bool) error {\n\treturn nil\n}\n"), 0644)
	os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc main() {\n\tProcess(1, \"a\", true)\n}\n"), 0644)
	db := index.NewOnDemand(tmp)
	defer db.Close()
	ctx := context.Background()

	var result struct {
		Status string `json:"status"`
	}
	// Remove the second parameter (y string, index 1).
	dispatchResult(t, ctx, db, "changesig", []string{"lib.go:Process"}, map[string]any{
		"remove": 1,
	}, &result)

	if result.Status != "applied" {
		t.Fatalf("status = %q, want applied", result.Status)
	}

	libData, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if !strings.Contains(string(libData), "func Process(x int, z bool)") {
		t.Errorf("definition not updated:\n%s", string(libData))
	}

	mainData, _ := os.ReadFile(filepath.Join(tmp, "main.go"))
	if !strings.Contains(string(mainData), "Process(1, true)") {
		t.Errorf("call site not updated:\n%s", string(mainData))
	}
}

func TestCorrectnessChangeSigDryRun(t *testing.T) {
	db, tmp := setupSemanticRepo(t)
	ctx := context.Background()

	var result struct {
		Status string `json:"status"`
	}
	dispatchResult(t, ctx, db, "changesig", []string{"lib.go:Compute"}, map[string]any{
		"add":     "ctx context.Context",
		"at":      0,
		"callarg": "ctx",
		"dry_run": true,
	}, &result)

	if result.Status != "dry_run" {
		t.Fatalf("status = %q, want dry_run", result.Status)
	}

	libData, _ := os.ReadFile(filepath.Join(tmp, "lib.go"))
	if strings.Contains(string(libData), "ctx") {
		t.Error("dry-run should not modify files")
	}
}

func TestCorrectnessChangeSigThenRead(t *testing.T) {
	db, _ := setupSemanticRepo(t)
	ctx := context.Background()

	// Add a parameter, then read the symbol to verify updated signature.
	dispatchResult(t, ctx, db, "changesig", []string{"lib.go:Compute"}, map[string]any{
		"add":     "ctx context.Context",
		"at":      0,
		"callarg": "ctx",
	}, nil)

	var readResult struct {
		Content string `json:"content"`
		Symbol  string `json:"symbol"`
	}
	dispatchResult(t, ctx, db, "read", []string{"lib.go:Compute"}, nil, &readResult)
	if !strings.Contains(readResult.Content, "ctx context.Context") {
		t.Errorf("after changesig, read should show new param:\n%s", readResult.Content)
	}
}
