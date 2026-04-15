package ledger

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderJSON_Roundtrip(t *testing.T) {
	l := renderableRenameLedger()
	data, err := RenderJSON(l)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var back Ledger
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Version != l.Version || back.Command != l.Command {
		t.Errorf("roundtrip mismatch: %+v", back)
	}
	if len(back.Sites) != len(l.Sites) {
		t.Errorf("sites count: got %d want %d", len(back.Sites), len(l.Sites))
	}
}

func TestRenderJSONHeader_SingleLine(t *testing.T) {
	l := renderableRenameLedger()
	data, err := RenderJSONHeader(l)
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	if strings.Contains(string(data), "\n") {
		t.Errorf("header should be single-line, got:\n%s", data)
	}
	// Parse to confirm it's valid JSON.
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatalf("unmarshal header: %v", err)
	}
	if obj["command"] != "rename" {
		t.Errorf("command = %v want rename", obj["command"])
	}
	if obj["version"] != SchemaVersion {
		t.Errorf("version = %v want %s", obj["version"], SchemaVersion)
	}
}
