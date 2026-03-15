package cmd

import (
	"encoding/json"
	"testing"
)

func TestCmdToDoJSON_Read(t *testing.T) {
	result := cmdToDoJSON("read", []string{"src/main.go"}, map[string]any{
		"budget": 200,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	if err := json.Unmarshal(result, &m); err != nil {
		t.Fatal(err)
	}

	reads, ok := m["reads"].([]any)
	if !ok || len(reads) != 1 {
		t.Fatalf("expected 1 read, got: %v", m)
	}
	r := reads[0].(map[string]any)
	if r["file"] != "src/main.go" {
		t.Errorf("file = %v", r["file"])
	}
	if r["budget"] != float64(200) {
		t.Errorf("budget = %v", r["budget"])
	}
}

func TestCmdToDoJSON_ReadSymbol(t *testing.T) {
	result := cmdToDoJSON("read", []string{"src/main.go:Server"}, map[string]any{
		"signatures": true,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	reads := m["reads"].([]any)
	r := reads[0].(map[string]any)
	if r["file"] != "src/main.go" {
		t.Errorf("file = %v", r["file"])
	}
	if r["symbol"] != "Server" {
		t.Errorf("symbol = %v", r["symbol"])
	}
	if r["signatures"] != true {
		t.Errorf("signatures = %v", r["signatures"])
	}
}

func TestCmdToDoJSON_ReadLineRange(t *testing.T) {
	result := cmdToDoJSON("read", []string{"main.go", "10", "50"}, map[string]any{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	reads := m["reads"].([]any)
	r := reads[0].(map[string]any)
	if r["file"] != "main.go" {
		t.Errorf("file = %v", r["file"])
	}
	if r["start_line"] != float64(10) {
		t.Errorf("start_line = %v", r["start_line"])
	}
	if r["end_line"] != float64(50) {
		t.Errorf("end_line = %v", r["end_line"])
	}
}

func TestCmdToDoJSON_ReadTwoArgs(t *testing.T) {
	result := cmdToDoJSON("read", []string{"main.go", "hello"}, map[string]any{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	reads := m["reads"].([]any)
	r := reads[0].(map[string]any)
	if r["file"] != "main.go" {
		t.Errorf("file = %v", r["file"])
	}
	if r["symbol"] != "hello" {
		t.Errorf("symbol = %v", r["symbol"])
	}
}

func TestCmdToDoJSON_Search(t *testing.T) {
	result := cmdToDoJSON("search", []string{"handleRequest"}, map[string]any{
		"body":   true,
		"budget": 500,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	queries := m["queries"].([]any)
	q := queries[0].(map[string]any)
	if q["cmd"] != "search" {
		t.Errorf("cmd = %v", q["cmd"])
	}
	if q["pattern"] != "handleRequest" {
		t.Errorf("pattern = %v", q["pattern"])
	}
	if q["body"] != true {
		t.Errorf("body = %v", q["body"])
	}
}

func TestCmdToDoJSON_Map(t *testing.T) {
	result := cmdToDoJSON("map", []string{}, map[string]any{
		"dir":    "internal/",
		"type":   "function",
		"budget": 300,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	queries := m["queries"].([]any)
	q := queries[0].(map[string]any)
	if q["cmd"] != "map" {
		t.Errorf("cmd = %v", q["cmd"])
	}
	if q["dir"] != "internal/" {
		t.Errorf("dir = %v", q["dir"])
	}
}

func TestCmdToDoJSON_Explore(t *testing.T) {
	result := cmdToDoJSON("explore", []string{"src/config.go", "parseConfig"}, map[string]any{
		"gather": true,
		"body":   true,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	queries := m["queries"].([]any)
	q := queries[0].(map[string]any)
	if q["cmd"] != "explore" {
		t.Errorf("cmd = %v", q["cmd"])
	}
	if q["file"] != "src/config.go" {
		t.Errorf("file = %v", q["file"])
	}
	if q["symbol"] != "parseConfig" {
		t.Errorf("symbol = %v", q["symbol"])
	}
	if q["gather"] != true {
		t.Errorf("gather = %v", q["gather"])
	}
}

func TestCmdToDoJSON_Refs(t *testing.T) {
	result := cmdToDoJSON("refs", []string{"parseConfig"}, map[string]any{
		"impact": true,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	queries := m["queries"].([]any)
	q := queries[0].(map[string]any)
	if q["cmd"] != "refs" {
		t.Errorf("cmd = %v", q["cmd"])
	}
	if q["symbol"] != "parseConfig" {
		t.Errorf("symbol = %v", q["symbol"])
	}
	if q["impact"] != true {
		t.Errorf("impact = %v", q["impact"])
	}
}

func TestCmdToDoJSON_Edit(t *testing.T) {
	result := cmdToDoJSON("edit", []string{"src/main.go"}, map[string]any{
		"old_text": "oldFunc()",
		"new_text": "newFunc()",
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	edits := m["edits"].([]any)
	e := edits[0].(map[string]any)
	if e["file"] != "src/main.go" {
		t.Errorf("file = %v", e["file"])
	}
	if e["old_text"] != "oldFunc()" {
		t.Errorf("old_text = %v", e["old_text"])
	}
	if e["new_text"] != "newFunc()" {
		t.Errorf("new_text = %v", e["new_text"])
	}
}

func TestCmdToDoJSON_Write(t *testing.T) {
	result := cmdToDoJSON("write", []string{"src/new.go"}, map[string]any{
		"content": "package main\n",
		"mkdir":   true,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	writes := m["writes"].([]any)
	w := writes[0].(map[string]any)
	if w["file"] != "src/new.go" {
		t.Errorf("file = %v", w["file"])
	}
	if w["content"] != "package main\n" {
		t.Errorf("content = %v", w["content"])
	}
	if w["mkdir"] != true {
		t.Errorf("mkdir = %v", w["mkdir"])
	}
}

func TestCmdToDoJSON_Rename(t *testing.T) {
	result := cmdToDoJSON("rename", []string{"OldFunc", "NewFunc"}, map[string]any{
		"dry_run": true,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	renames := m["renames"].([]any)
	r := renames[0].(map[string]any)
	if r["old_name"] != "OldFunc" {
		t.Errorf("old_name = %v", r["old_name"])
	}
	if r["new_name"] != "NewFunc" {
		t.Errorf("new_name = %v", r["new_name"])
	}
	if r["dry_run"] != true {
		t.Errorf("dry_run = %v", r["dry_run"])
	}
}

func TestCmdToDoJSON_Verify(t *testing.T) {
	result := cmdToDoJSON("verify", []string{}, map[string]any{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	if m["verify"] != true {
		t.Errorf("verify = %v", m["verify"])
	}
}

func TestCmdToDoJSON_Init(t *testing.T) {
	result := cmdToDoJSON("init", []string{}, map[string]any{})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	if m["init"] != true {
		t.Errorf("init = %v", m["init"])
	}
}

func TestCmdToDoJSON_Find(t *testing.T) {
	result := cmdToDoJSON("find", []string{"**/*.go"}, map[string]any{
		"budget": 500,
	})
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	var m map[string]any
	json.Unmarshal(result, &m)
	queries := m["queries"].([]any)
	q := queries[0].(map[string]any)
	if q["cmd"] != "find" {
		t.Errorf("cmd = %v", q["cmd"])
	}
	if q["pattern"] != "**/*.go" {
		t.Errorf("pattern = %v", q["pattern"])
	}
}

func TestCmdToDoJSON_UnknownCommand(t *testing.T) {
	result := cmdToDoJSON("nonexistent", []string{}, map[string]any{})
	if result != nil {
		t.Errorf("unknown command should return nil, got: %s", string(result))
	}
}

func TestExtractResultForCmd_Read(t *testing.T) {
	full := json.RawMessage(`{"reads":[{"cmd":"read","ok":true,"result":{"file":"main.go","content":"hello"}}]}`)
	extracted := extractResultForCmd("read", full)
	var m map[string]any
	if err := json.Unmarshal(extracted, &m); err != nil {
		t.Fatal(err)
	}
	if m["file"] != "main.go" {
		t.Errorf("expected file=main.go, got: %v", m)
	}
}

func TestExtractResultForCmd_Search(t *testing.T) {
	full := json.RawMessage(`{"queries":[{"cmd":"search","ok":true,"result":{"matches":[]}}]}`)
	extracted := extractResultForCmd("search", full)
	var m map[string]any
	if err := json.Unmarshal(extracted, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["matches"]; !ok {
		t.Errorf("expected matches in result, got: %v", m)
	}
}

func TestExtractResultForCmd_Verify(t *testing.T) {
	full := json.RawMessage(`{"verify":{"ok":true,"command":"go build"}}`)
	extracted := extractResultForCmd("verify", full)
	var m map[string]any
	if err := json.Unmarshal(extracted, &m); err != nil {
		t.Fatal(err)
	}
	if m["command"] != "go build" {
		t.Errorf("expected command=go build, got: %v", m)
	}
}

func TestFileArgToRead(t *testing.T) {
	tests := []struct {
		arg    string
		file   string
		symbol string
	}{
		{"main.go", "main.go", ""},
		{"src/main.go:Server", "src/main.go", "Server"},
		{"main.go:hello", "main.go", "hello"},
		{"/abs/path/file.go:Func", "/abs/path/file.go", "Func"},
	}

	for _, tt := range tests {
		r := fileArgToRead(tt.arg, map[string]any{})
		if r["file"] != tt.file {
			t.Errorf("fileArgToRead(%q): file = %v, want %v", tt.arg, r["file"], tt.file)
		}
		sym, _ := r["symbol"].(string)
		if sym != tt.symbol {
			t.Errorf("fileArgToRead(%q): symbol = %v, want %v", tt.arg, r["symbol"], tt.symbol)
		}
	}
}
