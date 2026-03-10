package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDeltaSettlesToUnchanged_ProcessReadResult tests at the ProcessReadResult level:
// store content A, check content B (get delta), check content B again (should get unchanged).
func TestDeltaSettlesToUnchanged_ProcessReadResult(t *testing.T) {
	s := New()

	// Step 1: Store content A (file read)
	resultA := map[string]any{
		"file":    "f.go",
		"lines":   []any{float64(1), float64(10)},
		"content": "line1\nline2\nline3",
		"hash":    "abc",
	}
	delta := s.ProcessReadResult("read", resultA, map[string]any{})
	if delta != nil {
		t.Fatalf("first read should return nil (new), got: %v", delta)
	}

	// Step 2: Read content B (different) — should get delta
	resultB := map[string]any{
		"file":    "f.go",
		"lines":   []any{float64(1), float64(10)},
		"content": "line1\nmodified\nline3",
		"hash":    "def",
	}
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("second read should return delta")
	}
	if delta["delta"] != true {
		t.Fatalf("expected delta=true, got: %v", delta)
	}

	// Step 3: Read content B again — should settle to unchanged
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("third read should return unchanged, got nil")
	}
	if delta["unchanged"] != true {
		t.Fatalf("expected unchanged=true after delta delivery, got: %v", delta)
	}
}

// TestDeltaSettlesToUnchanged_Symbol_ProcessReadResult tests symbol reads settle correctly.
func TestDeltaSettlesToUnchanged_Symbol_ProcessReadResult(t *testing.T) {
	s := New()

	resultA := map[string]any{
		"body":   "func foo() { v1 }",
		"symbol": map[string]any{"file": "f.go", "name": "foo", "hash": "abc"},
	}
	delta := s.ProcessReadResult("read", resultA, map[string]any{})
	if delta != nil {
		t.Fatalf("first read should return nil (new), got: %v", delta)
	}

	resultB := map[string]any{
		"body":   "func foo() { v2 }",
		"symbol": map[string]any{"file": "f.go", "name": "foo", "hash": "def"},
	}
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("second read should return delta")
	}
	if delta["delta"] != true {
		t.Fatalf("expected delta=true, got: %v", delta)
	}

	// Should settle to unchanged
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("third read should return unchanged, got nil")
	}
	if delta["unchanged"] != true {
		t.Fatalf("expected unchanged=true after delta delivery, got: %v", delta)
	}
}

// TestDeltaSettlesToUnchanged_ViaPostProcess tests the full PostProcess pipeline for file reads.
func TestDeltaSettlesToUnchanged_ViaPostProcess(t *testing.T) {
	s := New()

	textA := `{"file":"f.go","lines":[1,10],"content":"line1\nline2\nline3","hash":"abc"}`
	textB := `{"file":"f.go","lines":[1,10],"content":"line1\nmodified\nline3","hash":"def"}`

	// Step 1: First read — stores content A
	r1 := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textA)
	if strings.Contains(r1, "unchanged") || strings.Contains(r1, "delta") {
		t.Fatalf("first read should pass through, got: %s", r1)
	}

	// Step 2: Second read with different content — should get delta
	r2 := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if !strings.Contains(r2, "delta") {
		t.Fatalf("second read should return delta, got: %s", r2)
	}

	// Step 3: Third read with same content B — should settle to unchanged
	r3 := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if !strings.Contains(r3, "unchanged") {
		t.Fatalf("third read should return unchanged after delta delivery, got: %s", r3)
	}
}

// TestDeltaSettlesToUnchanged_Symbol_ViaPostProcess tests the full PostProcess pipeline for symbol reads.
func TestDeltaSettlesToUnchanged_Symbol_ViaPostProcess(t *testing.T) {
	s := New()

	textA := `{"body":"func foo() { v1 }","symbol":{"file":"f.go","name":"foo","hash":"abc"}}`
	textB := `{"body":"func foo() { v2 }","symbol":{"file":"f.go","name":"foo","hash":"def"}}`

	r1 := s.PostProcess("read", []string{"f.go", "foo"}, map[string]any{}, nil, textA)
	if strings.Contains(r1, "unchanged") || strings.Contains(r1, "delta") {
		t.Fatalf("first read should pass through, got: %s", r1)
	}

	r2 := s.PostProcess("read", []string{"f.go", "foo"}, map[string]any{}, nil, textB)
	if !strings.Contains(r2, "delta") {
		t.Fatalf("second read should return delta, got: %s", r2)
	}

	r3 := s.PostProcess("read", []string{"f.go", "foo"}, map[string]any{}, nil, textB)
	if !strings.Contains(r3, "unchanged") {
		t.Fatalf("third read should return unchanged after delta delivery, got: %s", r3)
	}
}

// TestDeltaSettlesToUnchanged_MultipleChanges verifies repeated A->B->B->C->C transitions.
func TestDeltaSettlesToUnchanged_MultipleChanges(t *testing.T) {
	s := New()

	textA := `{"file":"f.go","lines":[1,10],"content":"version1","hash":"aaa"}`
	textB := `{"file":"f.go","lines":[1,10],"content":"version2","hash":"bbb"}`
	textC := `{"file":"f.go","lines":[1,10],"content":"version3","hash":"ccc"}`

	// Store A
	s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textA)

	// Change to B — delta
	r := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if !strings.Contains(r, "delta") {
		t.Fatalf("A->B should be delta, got: %s", r)
	}

	// B again — unchanged
	r = s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if !strings.Contains(r, "unchanged") {
		t.Fatalf("B->B should be unchanged, got: %s", r)
	}

	// Change to C — delta
	r = s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textC)
	if !strings.Contains(r, "delta") {
		t.Fatalf("B->C should be delta, got: %s", r)
	}

	// C again — unchanged
	r = s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textC)
	var parsed map[string]any
	json.Unmarshal([]byte(r), &parsed)
	if parsed["unchanged"] != true {
		t.Fatalf("C->C should be unchanged, got: %s", r)
	}
}

// TestDeltaSkippedWhenOldContentMuchSmaller verifies that when old content is
// much smaller than new content (e.g. signatures→full body), delta is skipped
// and full content is returned instead.
func TestDeltaSkippedWhenOldContentMuchSmaller(t *testing.T) {
	s := New()

	// Step 1: Store a short signature-like body
	shortBody := "func handleDo(ctx context.Context) (string, error)"
	resultSig := map[string]any{
		"body":   shortBody,
		"symbol": map[string]any{"file": "cmd/mcp.go", "name": "handleDo", "hash": "abc"},
	}
	delta := s.ProcessReadResult("read", resultSig, map[string]any{})
	if delta != nil {
		t.Fatalf("first read should return nil (new), got: %v", delta)
	}

	// Step 2: Read the full body (>4x longer) — should NOT return delta
	// because the diff would be a near-complete rewrite
	fullBody := shortBody + " {\n" + strings.Repeat("\tline\n", 100) + "}"
	resultFull := map[string]any{
		"body":   fullBody,
		"symbol": map[string]any{"file": "cmd/mcp.go", "name": "handleDo", "hash": "def"},
	}
	delta = s.ProcessReadResult("read", resultFull, map[string]any{})
	if delta != nil {
		t.Fatalf("signatures→full body should skip delta (return nil), got: %v", delta)
	}

	// Step 3: Read same full body again — should be unchanged (content was stored)
	delta = s.ProcessReadResult("read", resultFull, map[string]any{})
	if delta == nil {
		t.Fatal("third read should return unchanged, got nil")
	}
	if delta["unchanged"] != true {
		t.Fatalf("expected unchanged=true, got: %v", delta)
	}
}

// TestDeltaStillWorksForSimilarSizedContent verifies that normal edits
// (similar-sized old and new) still produce useful deltas.
func TestDeltaStillWorksForSimilarSizedContent(t *testing.T) {
	s := New()

	// Store a reasonably sized function body
	oldBody := "func foo() {\n\treturn 1\n}\n" + strings.Repeat("// padding\n", 20)
	resultA := map[string]any{
		"body":   oldBody,
		"symbol": map[string]any{"file": "f.go", "name": "foo", "hash": "aaa"},
	}
	s.ProcessReadResult("read", resultA, map[string]any{})

	// Change one line in a similar-sized body
	newBody := "func foo() {\n\treturn 2\n}\n" + strings.Repeat("// padding\n", 20)
	resultB := map[string]any{
		"body":   newBody,
		"symbol": map[string]any{"file": "f.go", "name": "foo", "hash": "bbb"},
	}
	delta := s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("similar-sized content change should return delta")
	}
	if delta["delta"] != true {
		t.Fatalf("expected delta=true, got: %v", delta)
	}
	if _, ok := delta["diff"]; !ok {
		t.Fatal("delta should include diff")
	}
}

