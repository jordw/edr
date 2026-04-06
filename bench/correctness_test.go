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
