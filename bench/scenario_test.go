package bench_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
)

// Scenario represents a benchmark scenario definition loaded from JSON.
type Scenario struct {
	Name     string                       `json:"name"`
	Root     string                       `json:"root"`
	ScopeDir string                       `json:"scope_dir"`
	Scenarios map[string]json.RawMessage  `json:"scenarios"`
}

// ScenarioReadSignatures is the "read_signatures" scenario type.
type ScenarioReadSignatures struct {
	Type string `json:"type"`
	File string `json:"file"`
	Spec string `json:"spec"`
}

// ScenarioReadSymbol is the "read_symbol" scenario type.
type ScenarioReadSymbol struct {
	Type string `json:"type"`
	File string `json:"file"`
	Spec string `json:"spec"`
}

// ScenarioRefs is the "refs" scenario type.
type ScenarioRefs struct {
	Type     string   `json:"type"`
	Pattern  string   `json:"pattern"`
	GrepRoot string   `json:"grep_root"`
	Args     []string `json:"args"`
}

// ScenarioSearch is the "search" scenario type.
type ScenarioSearch struct {
	Type       string `json:"type"`
	Pattern    string `json:"pattern"`
	SearchRoot string `json:"search_root"`
	Budget     int    `json:"budget"`
}

// ScenarioMap is the "map" scenario type.
type ScenarioMap struct {
	Type      string   `json:"type"`
	Dir       string   `json:"dir"`
	Budget    int      `json:"budget"`
	Globs     []string `json:"globs"`
	ReadFiles []string `json:"read_files"`
}

// ScenarioEdit is the "edit" scenario type.
type ScenarioEdit struct {
	Type    string `json:"type"`
	File    string `json:"file"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// ScenarioWriteInside is the "write_inside" scenario type.
type ScenarioWriteInside struct {
	Type    string `json:"type"`
	File    string `json:"file"`
	Inside  string `json:"inside"`
	Content string `json:"content"`
}

// ScenarioMultiRead is the "multi_read" scenario type.
type ScenarioMultiRead struct {
	Type   string   `json:"type"`
	Budget int      `json:"budget"`
	Files  []string `json:"files"`
}

// ScenarioExplore is the "explore" scenario type.
type ScenarioExplore struct {
	Type            string   `json:"type"`
	Pattern         string   `json:"pattern"`
	GrepRoot        string   `json:"grep_root"`
	Args            []string `json:"args"`
	NativeReadFiles []string `json:"native_read_files"`
}

// LoadScenario reads a scenario JSON file relative to the bench directory.
func LoadScenario(relPath string) (*Scenario, error) {
	// Find bench directory relative to this test file
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
