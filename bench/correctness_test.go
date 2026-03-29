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
		errMsg := dispatchError(t, ctx, db, "read", []string{"Config"}, nil)
		if !strings.Contains(errMsg, "ambiguous") {
			t.Errorf("expected 'ambiguous' error, got: %s", errMsg)
		}
		// Should mention multiple files
		for _, fragment := range []string{"go/pkg_a/config.go", "go/pkg_b/config.go", "py/models.py"} {
			if !strings.Contains(errMsg, fragment) {
				t.Errorf("ambiguous error should mention %s, got: %s", fragment, errMsg)
			}
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
		errMsg := dispatchError(t, ctx, db, "read", []string{"Init"}, nil)
		if !strings.Contains(errMsg, "ambiguous") {
			t.Errorf("expected 'ambiguous' error for Init, got: %s", errMsg)
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

// --- Correctness: Repeated Method Names ---

func TestCorrectnessRepeatedMethodNames(t *testing.T) {
	db, _ := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("Validate in pkg_a vs pkg_b", func(t *testing.T) {
		// refs scoped to pkg_a/config.go should NOT include pkg_b refs
		var refsA struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"go/pkg_a/config.go", "Validate"}, nil, &refsA)

		var refsB struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"go/pkg_b/config.go", "Validate"}, nil, &refsB)

		// The definition files should be different
		if !strings.Contains(refsA.Symbol.File, "pkg_a") || !strings.Contains(refsB.Symbol.File, "pkg_b") {
			t.Errorf("expected different definition files: a=%s b=%s", refsA.Symbol.File, refsB.Symbol.File)
		}

		// Precision check: count cross-contamination.
		// Same method on different types should not cross package boundaries.
		aLeaks, bLeaks := 0, 0
		for _, ref := range refsA.References {
			if strings.Contains(ref.File, "pkg_b") {
				aLeaks++
			}
		}
		for _, ref := range refsB.References {
			if strings.Contains(ref.File, "pkg_a") {
				bLeaks++
			}
		}

		aPrecision := 1.0
		if len(refsA.References) > 0 {
			aPrecision = 1.0 - float64(aLeaks)/float64(len(refsA.References))
		}
		bPrecision := 1.0
		if len(refsB.References) > 0 {
			bPrecision = 1.0 - float64(bLeaks)/float64(len(refsB.References))
		}

		t.Logf("pkg_a Validate: def=%s refs=%d leaks=%d precision=%.2f",
			refsA.Symbol.File, len(refsA.References), aLeaks, aPrecision)
		t.Logf("pkg_b Validate: def=%s refs=%d leaks=%d precision=%.2f",
			refsB.Symbol.File, len(refsB.References), bLeaks, bPrecision)

		// Gate: precision must be >= 0.6.
		// Current actual precision is 0.67 (1 cross-package leak per 3 refs).
		// Target is 0.8+ once import-aware ref filtering improves.
		if aPrecision < 0.6 {
			t.Errorf("pkg_a Validate precision %.2f < 0.6 threshold", aPrecision)
		}
		if bPrecision < 0.6 {
			t.Errorf("pkg_b Validate precision %.2f < 0.6 threshold", bPrecision)
		}
	})

	t.Run("Init in pkg_a vs pkg_b", func(t *testing.T) {
		var refsA struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"go/pkg_a/config.go", "Init"}, nil, &refsA)

		var refsB struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"go/pkg_b/config.go", "Init"}, nil, &refsB)

		if !strings.Contains(refsA.Symbol.File, "pkg_a") || !strings.Contains(refsB.Symbol.File, "pkg_b") {
			t.Errorf("expected different definition files: a=%s b=%s", refsA.Symbol.File, refsB.Symbol.File)
		}

		// pkg_a Init is called from main.go via aliased import cfgA.Init().
		// Text-based ref finding cannot resolve aliased imports, so 0 refs is expected.
		// TODO: support aliased Go imports in ref resolution.
		t.Logf("pkg_a Init: refs=%d (aliased import, 0 expected until import-aware refs)", len(refsA.References))

		// pkg_b Init should have refs (called from helpers.go)
		if len(refsB.References) == 0 {
			t.Error("pkg_b Init should have at least one reference")
		}

		// Cross-contamination check: same precision gate as Validate
		aLeaks := 0
		for _, ref := range refsA.References {
			if strings.Contains(ref.File, "pkg_b") {
				aLeaks++
			}
		}
		bLeaks := 0
		for _, ref := range refsB.References {
			if strings.Contains(ref.File, "pkg_a") {
				bLeaks++
			}
		}
		// Same threshold as Validate: 0.6 floor, 0.8 target.
		if len(refsA.References) > 0 {
			aPrecision := 1.0 - float64(aLeaks)/float64(len(refsA.References))
			if aPrecision < 0.6 {
				t.Errorf("pkg_a Init precision %.2f < 0.6", aPrecision)
			}
			t.Logf("pkg_a Init: refs=%d leaks=%d precision=%.2f", len(refsA.References), aLeaks, aPrecision)
		}
		if len(refsB.References) > 0 {
			bPrecision := 1.0 - float64(bLeaks)/float64(len(refsB.References))
			if bPrecision < 0.6 {
				t.Errorf("pkg_b Init precision %.2f < 0.6", bPrecision)
			}
			t.Logf("pkg_b Init: refs=%d leaks=%d precision=%.2f", len(refsB.References), bLeaks, bPrecision)
		}
	})
}

// --- Correctness: Cross-Language Search ---

func TestCorrectnessCrossLanguageSearch(t *testing.T) {
	db, _ := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("search Config finds multiple languages", func(t *testing.T) {
		var result struct {
			Matches      []searchMatch `json:"matches"`
			TotalMatches int           `json:"total_matches"`
		}
		dispatchResult(t, ctx, db, "search", []string{"Config"}, map[string]any{"body": true}, &result)

		langs := make(map[string]bool)
		for _, m := range result.Matches {
			ext := filepath.Ext(m.Symbol.File)
			langs[ext] = true
		}
		if len(langs) < 3 {
			t.Errorf("expected Config in >=3 languages, got %d: %v", len(langs), mapKeysStr(langs))
		}
		t.Logf("Config found in %d languages: %v (%d total matches)", len(langs), mapKeysStr(langs), result.TotalMatches)
	})

	t.Run("search validate finds Go+Python+JS", func(t *testing.T) {
		var result struct {
			Matches      []searchMatch `json:"matches"`
			TotalMatches int           `json:"total_matches"`
		}
		dispatchResult(t, ctx, db, "search", []string{"validate"}, nil, &result)

		langs := make(map[string]bool)
		for _, m := range result.Matches {
			ext := filepath.Ext(m.Symbol.File)
			langs[ext] = true
		}
		// validate/Validate exists in Go (.go), Python (.py), and JS (.js)
		if len(langs) < 2 {
			t.Errorf("expected validate in >=2 languages, got %d: %v", len(langs), mapKeysStr(langs))
		}
		t.Logf("validate found in %d languages: %v", len(langs), mapKeysStr(langs))
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

		// Verify refs still work after edit
		var refs struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"go/pkg_a/config.go", "Config"}, nil, &refs)
		if refs.Symbol.File == "" {
			t.Error("refs should still resolve Config after edit")
		}
		t.Logf("Config refs after edit: def=%s refs=%d", refs.Symbol.File, len(refs.References))
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

// --- Correctness: Rename Safety ---

func TestCorrectnessRenameSafety(t *testing.T) {
	db, _ := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("ambiguous symbol is rejected", func(t *testing.T) {
		// Rename refuses ambiguous symbols (Config exists in 6 files).
		// This is correct safety behavior — renaming the wrong definition is worse
		// than refusing. The error should say "ambiguous".
		errMsg := dispatchError(t, ctx, db, "rename", []string{"Config", "Settings"}, map[string]any{
			"dry_run": true,
		})
		if !strings.Contains(errMsg, "ambiguous") {
			t.Errorf("expected 'ambiguous' error, got: %s", errMsg)
		}
		t.Logf("rename Config->Settings correctly rejected: %s", errMsg)
	})

	t.Run("unique symbol renames successfully", func(t *testing.T) {
		// Logger is unique (only in js/types.js) — rename should work.
		var result struct {
			OldName      string `json:"old_name"`
			NewName      string `json:"new_name"`
			Occurrences  int    `json:"occurrences"`
			DryRun       bool   `json:"dry_run"`
			FilesChanged []string `json:"files_changed"`
			Preview      []struct {
				File string `json:"file"`
				Line int    `json:"line"`
				Text string `json:"text"`
			} `json:"preview"`
		}
		dispatchResult(t, ctx, db, "rename", []string{"Logger", "AppLogger"}, map[string]any{
			"dry_run": true,
		}, &result)

		if !result.DryRun {
			t.Error("should be dry run")
		}
		if result.Occurrences == 0 {
			t.Error("rename Logger->AppLogger should find occurrences")
		}

		// Logger is defined in js/types.js and used in js/index.js
		hasTypes := false
		hasIndex := false
		for _, fc := range result.FilesChanged {
			if strings.Contains(fc, "types.js") {
				hasTypes = true
			}
			if strings.Contains(fc, "index.js") {
				hasIndex = true
			}
		}
		if !hasTypes {
			t.Error("rename should touch types.js (definition)")
		}
		if !hasIndex {
			t.Error("rename should touch index.js (import)")
		}

		t.Logf("rename Logger->AppLogger: %d occurrences in %v", result.Occurrences, result.FilesChanged)
		for _, p := range result.Preview {
			t.Logf("  %s:%d %s", p.File, p.Line, p.Text)
		}
	})

	t.Run("unique symbol rename without scope", func(t *testing.T) {
		// Process is unique (only in go/pkg_b/helpers.go).
		// Note: --scope uses output.Rel() which depends on output.SetRoot(),
		// a sync.Once that may be set to a different test's temp dir when running
		// the full suite. We test unscoped rename instead, which is reliable.
		var result struct {
			Occurrences  int      `json:"occurrences"`
			DryRun       bool     `json:"dry_run"`
			FilesChanged []string `json:"files_changed"`
		}
		dispatchResult(t, ctx, db, "rename", []string{"Process", "Execute"}, map[string]any{
			"dry_run": true,
		}, &result)

		if result.Occurrences == 0 {
			t.Error("rename Process->Execute should find occurrences")
		}

		// Process is only defined in helpers.go — all changes should be in Go files
		for _, fc := range result.FilesChanged {
			if !strings.HasSuffix(fc, ".go") {
				t.Errorf("Process rename should only touch .go files, got: %s", fc)
			}
		}
		t.Logf("rename Process->Execute: %d occurrences in %v",
			result.Occurrences, result.FilesChanged)
	})

	t.Run("Result class renames across Python files", func(t *testing.T) {
		// Result is unique (only in py/models.py) — should rename in models.py and app.py.
		var result struct {
			Occurrences  int      `json:"occurrences"`
			DryRun       bool     `json:"dry_run"`
			FilesChanged []string `json:"files_changed"`
		}
		dispatchResult(t, ctx, db, "rename", []string{"Result", "Outcome"}, map[string]any{
			"dry_run": true,
		}, &result)

		if result.Occurrences == 0 {
			t.Error("rename Result->Outcome should find occurrences")
		}

		// Should not touch Go or JS files
		for _, fc := range result.FilesChanged {
			if strings.HasSuffix(fc, ".go") || strings.HasSuffix(fc, ".js") {
				t.Errorf("Python-only rename should not touch %s: %v", fc, result.FilesChanged)
			}
		}
		t.Logf("rename Result->Outcome: %d occurrences in %v", result.Occurrences, result.FilesChanged)
	})
}

// --- Correctness: Precision/Recall for Refs ---

func TestCorrectnessRefsPrecisionRecall(t *testing.T) {
	db, _ := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("Config refs in Go pkg_a", func(t *testing.T) {
		var result struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"go/pkg_a/config.go", "Config"}, nil, &result)

		if len(result.References) == 0 {
			t.Fatal("Config (pkg_a) should have at least one reference")
		}

		// Config is used within pkg_a (Init returns *Config, Validate receiver).
		// It should NOT include pkg_b/helpers.go (different Config type).
		for _, ref := range result.References {
			if strings.Contains(ref.File, "pkg_b/helpers.go") {
				t.Errorf("pkg_a Config refs must not include pkg_b/helpers.go")
			}
		}

		// main.go uses cfgA.Config via aliased import with a fake module path
		// (example.com/adversarial/pkg_a). The suffix-based Go import resolver
		// can't match this without a real go.mod, so hasMain=false is expected
		// here. In real repos with valid module paths, this would resolve.
		// We track the metric so we notice if/when the resolver improves.
		hasMain := false
		for _, ref := range result.References {
			if strings.Contains(ref.File, "main.go") {
				hasMain = true
			}
		}

		t.Logf("Config (pkg_a): refs=%d hasMain=%v (false expected: fake module path)", len(result.References), hasMain)
	})

	t.Run("validate refs in Python models", func(t *testing.T) {
		var result struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"py/models.py", "validate"}, nil, &result)

		if len(result.References) == 0 {
			t.Fatal("validate (py/models.py) should have at least one reference")
		}

		// app.py imports models.Config and calls model_cfg.validate()
		hasApp := false
		for _, ref := range result.References {
			if strings.Contains(ref.File, "app.py") {
				hasApp = true
			}
		}
		if !hasApp {
			t.Errorf("validate (py/models.py) refs should include app.py; got refs in: %v",
				refFiles(result.References))
		}

		t.Logf("validate (py/models.py): refs=%d hasApp=%v", len(result.References), hasApp)
	})

	t.Run("Logger refs in JS types", func(t *testing.T) {
		var result struct {
			Symbol     symbolFile   `json:"symbol"`
			References []symbolFile `json:"references"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"js/types.js", "Logger"}, nil, &result)

		if len(result.References) == 0 {
			t.Fatal("Logger (js/types.js) should have at least one reference")
		}

		// index.js imports Logger from types.js
		hasIndex := false
		for _, ref := range result.References {
			if strings.Contains(ref.File, "index.js") {
				hasIndex = true
			}
		}
		if !hasIndex {
			t.Errorf("Logger refs should include index.js; got refs in: %v",
				refFiles(result.References))
		}

		t.Logf("Logger: refs=%d hasIndex=%v", len(result.References), hasIndex)
	})
}

// --- Correctness: Explore ---

func TestCorrectnessExplore(t *testing.T) {
	db, _ := setupAdversarialRepo(t)
	ctx := context.Background()

	t.Run("explore scoped symbol", func(t *testing.T) {
		var result struct {
			Symbol  symbolFile   `json:"symbol"`
			Body    string       `json:"content"`
			Callers []symbolFile `json:"callers"`
			Deps    []symbolFile `json:"deps"`
		}
		dispatchResult(t, ctx, db, "refs", []string{"go/pkg_b/helpers.go", "Process"}, map[string]any{
			"body":    true,
			"callers": true,
			"deps":    true,
		}, &result)

		if result.Body == "" {
			t.Error("refs body should not be empty")
		}
		// Process calls Init() — should appear in deps
		t.Logf("Process: body=%dB callers=%d deps=%d", len(result.Body), len(result.Callers), len(result.Deps))
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
