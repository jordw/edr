// Scenario definitions, JSON loader, and dispatch validation.
//
// JSON scenarios in bench/scenarios/ are the single source of truth for
// benchmark definitions. Both the Go tests (TestScenarioDispatch) and
// the shell runner (native_comparison.sh) consume them.
//
// Run with: go test ./bench/ -run TestScenarioDispatch -v
package bench_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Scenario types and loader
// ---------------------------------------------------------------------------

// Scenario represents a benchmark scenario definition loaded from JSON.
type Scenario struct {
	Name      string                      `json:"name"`
	Root      string                      `json:"root"`
	ScopeDir  string                      `json:"scope_dir"`
	Scenarios map[string]json.RawMessage  `json:"scenarios"`
}

type ScenarioReadSignatures struct {
	Type string `json:"type"`
	File string `json:"file"`
	Spec string `json:"spec"`
}

type ScenarioReadSymbol struct {
	Type string `json:"type"`
	File string `json:"file"`
	Spec string `json:"spec"`
}

type ScenarioRefs struct {
	Type     string   `json:"type"`
	Pattern  string   `json:"pattern"`
	GrepRoot string   `json:"grep_root"`
	Args     []string `json:"args"`
}

type ScenarioSearch struct {
	Type       string `json:"type"`
	Pattern    string `json:"pattern"`
	SearchRoot string `json:"search_root"`
	Budget     int    `json:"budget"`
}

type ScenarioMap struct {
	Type      string   `json:"type"`
	Dir       string   `json:"dir"`
	Budget    int      `json:"budget"`
	Globs     []string `json:"globs"`
	ReadFiles []string `json:"read_files"`
}

type ScenarioEdit struct {
	Type    string `json:"type"`
	File    string `json:"file"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

type ScenarioWriteInside struct {
	Type    string `json:"type"`
	File    string `json:"file"`
	Inside  string `json:"inside"`
	Content string `json:"content"`
}

type ScenarioMultiRead struct {
	Type   string   `json:"type"`
	Budget int      `json:"budget"`
	Files  []string `json:"files"`
}

type ScenarioExplore struct {
	Type            string   `json:"type"`
	Pattern         string   `json:"pattern"`
	GrepRoot        string   `json:"grep_root"`
	Args            []string `json:"args"`
	NativeReadFiles []string `json:"native_read_files"`
}

// LoadScenario reads a scenario JSON file relative to the bench directory.
func LoadScenario(relPath string) (*Scenario, error) {
	_, filename, _, _ := runtime.Caller(0)
	benchDir := filepath.Dir(filename)
	path := filepath.Join(benchDir, relPath)

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var s Scenario
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// GetScenario unmarshals a named scenario from the Scenario's map.
func (s *Scenario) GetScenario(name string, dest any) error {
	raw, ok := s.Scenarios[name]
	if !ok {
		return os.ErrNotExist
	}
	return json.Unmarshal(raw, dest)
}

// ---------------------------------------------------------------------------
// Dispatch validation
// ---------------------------------------------------------------------------

// TestScenarioDispatch verifies that the JSON scenario definitions contain
// valid command parameters that dispatch successfully against the testdata.
//
// This does NOT validate end-to-end equivalence with the shell benchmark
// runner (native_comparison.sh), which uses different path semantics
// (BENCH_ROOT-relative vs Go test temp dir). The shell runner is the
// authoritative consumer of scenario files for real-repo benchmarks.
// This test catches schema/routing breakage: renamed fields, removed
// commands, or invalid argument shapes.
func TestScenarioDispatch(t *testing.T) {
	scenario, err := LoadScenario("scenarios/fixture.json")
	if err != nil {
		t.Skipf("scenario file not found: %v", err)
	}

	db, _ := setupRepo(t)
	ctx := context.Background()

	t.Run("understand_api", func(t *testing.T) {
		var s ScenarioReadSignatures
		if err := scenario.GetScenario("understand_api", &s); err != nil {
			t.Fatal(err)
		}
		out, err := dispatchJSON(ctx, db, "read", []string{s.Spec}, map[string]any{"signatures": true})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var result struct {
			Body string `json:"content"`
		}
		json.Unmarshal(out, &result)
		if result.Body == "" {
			t.Error("signatures response should have a body")
		}
		full, _ := dispatchJSON(ctx, db, "read", []string{s.Spec}, nil)
		if len(out) >= len(full) {
			t.Errorf("signatures (%dB) should be smaller than full read (%dB)", len(out), len(full))
		}
		t.Logf("understand_api: sigs=%dB full=%dB savings=%d%%",
			len(out), len(full), 100-len(out)*100/len(full))
	})

	t.Run("read_symbol", func(t *testing.T) {
		var s ScenarioReadSymbol
		if err := scenario.GetScenario("read_symbol", &s); err != nil {
			t.Fatal(err)
		}
		out, err := dispatchJSON(ctx, db, "read", []string{s.Spec}, nil)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var result struct {
			Body   string `json:"content"`
			Symbol struct {
				Name string `json:"name"`
			} `json:"symbol"`
		}
		json.Unmarshal(out, &result)
		if result.Body == "" {
			t.Error("read_symbol should return a body")
		}
		parts := strings.SplitN(s.Spec, ":", 2)
		if len(parts) == 2 && result.Symbol.Name != parts[1] {
			t.Errorf("expected symbol name %q, got %q", parts[1], result.Symbol.Name)
		}
		t.Logf("read_symbol: %dB name=%s", len(out), result.Symbol.Name)
	})

	t.Run("find_refs", func(t *testing.T) {
		var s ScenarioRefs
		if err := scenario.GetScenario("find_refs", &s); err != nil {
			t.Fatal(err)
		}
		out, err := dispatchJSON(ctx, db, "refs", s.Args, nil)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var result struct {
			Symbol     struct{ Name string } `json:"symbol"`
			References []json.RawMessage     `json:"references"`
		}
		json.Unmarshal(out, &result)
		if result.Symbol.Name == "" {
			t.Error("refs should return a symbol definition")
		}
		t.Logf("find_refs: %dB symbol=%s refs=%d", len(out), result.Symbol.Name, len(result.References))
	})

	t.Run("search_context", func(t *testing.T) {
		var s ScenarioSearch
		if err := scenario.GetScenario("search_context", &s); err != nil {
			t.Fatal(err)
		}
		out, err := dispatchJSON(ctx, db, "search", []string{s.Pattern}, map[string]any{
			"text":   true,
			"budget": s.Budget,
		})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var result struct {
			Matches      []json.RawMessage `json:"matches"`
			TotalMatches int               `json:"total_matches"`
		}
		json.Unmarshal(out, &result)
		if result.TotalMatches == 0 {
			t.Errorf("search for %q should find matches", s.Pattern)
		}
		t.Logf("search_context: %dB matches=%d", len(out), result.TotalMatches)
	})

	t.Run("orient_map", func(t *testing.T) {
		var s ScenarioMap
		if err := scenario.GetScenario("orient_map", &s); err != nil {
			t.Fatal(err)
		}
		// The scenario's dir field targets bench/testdata within the real repo;
		// here the temp dir IS the testdata root, so we omit dir.
		out, err := dispatchJSON(ctx, db, "map", nil, map[string]any{
			"budget": s.Budget,
		})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var result struct {
			Map     string `json:"map"`
			Files   int    `json:"files"`
			Symbols int    `json:"symbols"`
		}
		json.Unmarshal(out, &result)
		if result.Symbols == 0 {
			t.Error("map should return symbols")
		}
		if result.Map == "" {
			t.Error("map should return a map string")
		}
		t.Logf("orient_map: %dB files=%d symbols=%d", len(out), result.Files, result.Symbols)
	})

	t.Run("edit_function", func(t *testing.T) {
		var s ScenarioEdit
		if err := scenario.GetScenario("edit_function", &s); err != nil {
			t.Fatal(err)
		}
		out, err := dispatchJSON(ctx, db, "edit", []string{s.File}, map[string]any{
			"old_text": s.OldText,
			"new_text": s.NewText,
			"dry-run":  true,
		})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var result struct {
			DryRun bool   `json:"dry_run"`
			File   string `json:"file"`
			Diff   string `json:"diff"`
		}
		json.Unmarshal(out, &result)
		if !result.DryRun {
			t.Error("edit should be a dry-run")
		}
		if result.File == "" {
			t.Error("edit should return a file path")
		}
		if result.Diff == "" {
			t.Error("edit dry-run should return a diff")
		}
		t.Logf("edit_function: %dB file=%s diff=%dB", len(out), result.File, len(result.Diff))
	})

	t.Run("multi_file_read", func(t *testing.T) {
		var s ScenarioMultiRead
		if err := scenario.GetScenario("multi_file_read", &s); err != nil {
			t.Fatal(err)
		}
		out, err := dispatchJSON(ctx, db, "read", s.Files, map[string]any{"budget": s.Budget})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var results []json.RawMessage
		json.Unmarshal(out, &results)
		if len(results) < len(s.Files) {
			t.Errorf("expected results for %d files, got %d", len(s.Files), len(results))
		}
		t.Logf("multi_file_read: %dB files=%d/%d", len(out), len(results), len(s.Files))
	})

	t.Run("explore_symbol", func(t *testing.T) {
		var s ScenarioExplore
		if err := scenario.GetScenario("explore_symbol", &s); err != nil {
			t.Fatal(err)
		}
		out, err := dispatchJSON(ctx, db, "explore", s.Args, map[string]any{
			"body":    true,
			"callers": true,
			"deps":    true,
		})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		var result struct {
			Symbol struct{ Name string } `json:"symbol"`
			Body   string                `json:"content"`
		}
		json.Unmarshal(out, &result)
		if result.Symbol.Name == "" {
			t.Error("explore should return a symbol")
		}
		if result.Body == "" {
			t.Error("explore with --body should return body content")
		}
		t.Logf("explore_symbol: %dB symbol=%s body=%dB", len(out), result.Symbol.Name, len(result.Body))
	})
}
