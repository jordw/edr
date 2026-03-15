package output

import (
	"encoding/json"
	"testing"
)

func TestToFlatMap_MapInput(t *testing.T) {
	m := map[string]any{"file": "test.go", "hash": "abc"}
	flat, err := toFlatMap(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if flat["file"] != "test.go" {
		t.Errorf("file = %v, want test.go", flat["file"])
	}
	// Verify it's a copy, not the same map
	flat["extra"] = true
	if _, ok := m["extra"]; ok {
		t.Error("toFlatMap should copy, not alias")
	}
}

func TestToFlatMap_StructInput(t *testing.T) {
	type result struct {
		File string `json:"file"`
		OK   bool   `json:"ok"`
	}
	flat, err := toFlatMap(result{File: "test.go", OK: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if flat["file"] != "test.go" {
		t.Errorf("file = %v, want test.go", flat["file"])
	}
}

func TestToFlatMap_NilReturnsError(t *testing.T) {
	_, err := toFlatMap(nil)
	if err == nil {
		t.Error("expected error for nil input")
	}
}

func TestToFlatMap_ArrayReturnsError(t *testing.T) {
	_, err := toFlatMap([]string{"a", "b"})
	if err == nil {
		t.Error("expected error for array input")
	}
}

func TestToFlatMap_ScalarReturnsError(t *testing.T) {
	_, err := toFlatMap("just a string")
	if err == nil {
		t.Error("expected error for scalar input")
	}
}

func TestAddOp_FlatFields(t *testing.T) {
	env := NewEnvelope("test")
	err := env.AddOp("r0", "read", map[string]any{"file": "test.go", "content": "hello"})
	if err != nil {
		t.Fatalf("AddOp: %v", err)
	}
	if len(env.Ops) != 1 {
		t.Fatalf("ops count = %d, want 1", len(env.Ops))
	}
	op := env.Ops[0]
	if op["op_id"] != "r0" {
		t.Errorf("op_id = %v, want r0", op["op_id"])
	}
	if op["type"] != "read" {
		t.Errorf("type = %v, want read", op["type"])
	}
	if op["file"] != "test.go" {
		t.Errorf("file = %v, want test.go", op["file"])
	}
	// Should NOT have a "result" wrapper
	if _, has := op["result"]; has {
		t.Error("op should not have 'result' wrapper")
	}
}

func TestAddOp_NonObjectResultFails(t *testing.T) {
	env := NewEnvelope("test")
	err := env.AddOp("r0", "read", []string{"a", "b"})
	if err == nil {
		t.Error("expected error for non-object result")
	}
	// Should have created a failed op instead
	if len(env.Ops) != 1 {
		t.Fatalf("ops count = %d, want 1", len(env.Ops))
	}
	if _, hasErr := env.Ops[0]["error"]; !hasErr {
		t.Error("non-object result should create a failed op")
	}
}

func TestAddSkippedOp(t *testing.T) {
	env := NewEnvelope("batch")
	env.AddSkippedOp("w0", "write", "edits failed")

	if len(env.Ops) != 1 {
		t.Fatalf("ops count = %d, want 1", len(env.Ops))
	}
	op := env.Ops[0]
	if op["status"] != "skipped" {
		t.Errorf("status = %v, want skipped", op["status"])
	}
	if op["reason"] != "edits failed" {
		t.Errorf("reason = %v, want edits failed", op["reason"])
	}
	// Skipped ops should NOT set ok=false
	if !env.OK {
		t.Error("skipped op should not set envelope ok=false")
	}
	// Skipped ops should NOT have "error" key
	if _, has := op["error"]; has {
		t.Error("skipped op should not have error key")
	}
}

func TestComputeOK_AllSuccess(t *testing.T) {
	env := NewEnvelope("test")
	env.AddOp("r0", "read", map[string]any{"file": "a.go"})
	env.ComputeOK()
	if !env.OK {
		t.Error("all-success envelope should be ok=true")
	}
}

func TestComputeOK_OpWithError(t *testing.T) {
	env := NewEnvelope("test")
	env.AddOp("r0", "read", map[string]any{"file": "a.go"})
	env.AddFailedOp("e0", "edit", "not found")
	env.OK = true // reset to test ComputeOK
	env.ComputeOK()
	if env.OK {
		t.Error("envelope with failed op should be ok=false")
	}
}

func TestComputeOK_VerifyFailure(t *testing.T) {
	env := NewEnvelope("test")
	env.AddOp("e0", "edit", map[string]any{"status": "applied"})
	env.SetVerify(map[string]any{"ok": false, "error": "exit status 1"})
	env.ComputeOK()
	if env.OK {
		t.Error("envelope with failed verify should be ok=false")
	}
}

func TestComputeOK_SkippedDoesNotFail(t *testing.T) {
	env := NewEnvelope("test")
	env.AddOp("e0", "edit", map[string]any{"status": "applied"})
	env.AddSkippedOp("w0", "write", "edits failed")
	env.ComputeOK()
	// Skipped ops don't have "error" key, so they shouldn't fail
	// But the edit failure that caused the skip already set ok=false via AddFailedOp
	// In isolation, skipped ops should not fail the envelope
	if !env.OK {
		t.Error("skipped op alone should not set ok=false")
	}
}

func TestHasOpErrors(t *testing.T) {
	env := NewEnvelope("test")
	env.AddOp("r0", "read", map[string]any{"file": "a.go"})
	if env.HasOpErrors() {
		t.Error("no errors, HasOpErrors should be false")
	}
	env.AddFailedOp("e0", "edit", "not found")
	if !env.HasOpErrors() {
		t.Error("has failed op, HasOpErrors should be true")
	}
}

func TestIsVerifyOnlyFailure(t *testing.T) {
	env := NewEnvelope("test")
	env.AddOp("e0", "edit", map[string]any{"status": "applied"})
	env.SetVerify(map[string]any{"ok": false, "error": "exit 1"})
	env.ComputeOK()

	if !env.IsVerifyOnlyFailure() {
		t.Error("should be verify-only failure")
	}

	// Add an op error — no longer verify-only
	env.AddFailedOp("e1", "edit", "not found")
	if env.IsVerifyOnlyFailure() {
		t.Error("should NOT be verify-only failure when ops also failed")
	}
}

func TestEnvelopeJSON_FlatOps(t *testing.T) {
	env := NewEnvelope("read")
	env.AddOp("r0", "read", map[string]any{"file": "test.go", "content": "hello"})
	env.ComputeOK()

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var parsed map[string]any
	json.Unmarshal(data, &parsed)

	ops := parsed["ops"].([]any)
	op := ops[0].(map[string]any)

	// Verify flat structure
	if op["file"] != "test.go" {
		t.Errorf("file should be flat on op, got keys: %v", op)
	}
	if _, has := op["result"]; has {
		t.Error("should not have 'result' wrapper in JSON")
	}
	if parsed["schema_version"].(float64) != 2 {
		t.Errorf("schema_version = %v, want 2", parsed["schema_version"])
	}
}
