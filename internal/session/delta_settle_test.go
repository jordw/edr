package session

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDeltaSettlesToUnchanged_ProcessReadResult tests at the ProcessReadResult level:
// store content A, read content B (treated as new since hash-only), read B again (unchanged).
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

	// Step 2: Read content B (different) — with hash-only storage, treated as new (nil)
	resultB := map[string]any{
		"file":    "f.go",
		"lines":   []any{float64(1), float64(10)},
		"content": "line1\nmodified\nline3",
		"hash":    "def",
	}
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta != nil {
		t.Fatalf("second read should return nil (new, hash-only), got: %v", delta)
	}

	// Step 3: Read content B again — should settle to unchanged
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("third read should return unchanged, got nil")
	}
	if delta["unchanged"] != true {
		t.Fatalf("expected unchanged=true after storing B, got: %v", delta)
	}
}

// TestDeltaSettlesToUnchanged_Symbol_ProcessReadResult tests symbol reads settle correctly.
func TestDeltaSettlesToUnchanged_Symbol_ProcessReadResult(t *testing.T) {
	s := New()

	resultA := map[string]any{
		"content": "func foo() { v1 }",
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "abc",
	}
	delta := s.ProcessReadResult("read", resultA, map[string]any{})
	if delta != nil {
		t.Fatalf("first read should return nil (new), got: %v", delta)
	}

	resultB := map[string]any{
		"content": "func foo() { v2 }",
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "def",
	}
	// With hash-only storage, changed content is treated as new
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta != nil {
		t.Fatalf("second read should return nil (new, hash-only), got: %v", delta)
	}

	// Should settle to unchanged
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("third read should return unchanged, got nil")
	}
	if delta["unchanged"] != true {
		t.Fatalf("expected unchanged=true after storing B, got: %v", delta)
	}
}

// TestDeltaSettlesToUnchanged_ViaPostProcess tests the full PostProcess pipeline for file reads.
func TestDeltaSettlesToUnchanged_ViaPostProcess(t *testing.T) {
	s := New()

	textA := `{"file":"f.go","lines":[1,10],"content":"line1\nline2\nline3","hash":"abc"}`
	textB := `{"file":"f.go","lines":[1,10],"content":"line1\nmodified\nline3","hash":"def"}`

	// Step 1: First read — stores content A
	r1 := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textA)
	if strings.Contains(r1, "unchanged") {
		t.Fatalf("first read should pass through, got: %s", r1)
	}

	// Step 2: Second read with different content — treated as new (hash-only, no diff)
	r2 := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if strings.Contains(r2, "unchanged") {
		t.Fatalf("second read should pass through (new content), got: %s", r2)
	}

	// Step 3: Third read with same content B — should settle to unchanged
	r3 := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if !strings.Contains(r3, "unchanged") {
		t.Fatalf("third read should return unchanged after storing B, got: %s", r3)
	}
}

// TestDeltaSettlesToUnchanged_Symbol_ViaPostProcess tests the full PostProcess pipeline for symbol reads.
func TestDeltaSettlesToUnchanged_Symbol_ViaPostProcess(t *testing.T) {
	s := New()

	textA := `{"content":"func foo() { v1 }","file":"f.go","symbol":"foo","hash":"abc"}`
	textB := `{"content":"func foo() { v2 }","file":"f.go","symbol":"foo","hash":"def"}`

	r1 := s.PostProcess("read", []string{"f.go", "foo"}, map[string]any{}, nil, textA)
	if strings.Contains(r1, "unchanged") {
		t.Fatalf("first read should pass through, got: %s", r1)
	}

	// With hash-only storage, changed content is treated as new
	r2 := s.PostProcess("read", []string{"f.go", "foo"}, map[string]any{}, nil, textB)
	if strings.Contains(r2, "unchanged") {
		t.Fatalf("second read should pass through (new content), got: %s", r2)
	}

	r3 := s.PostProcess("read", []string{"f.go", "foo"}, map[string]any{}, nil, textB)
	if !strings.Contains(r3, "unchanged") {
		t.Fatalf("third read should return unchanged after storing B, got: %s", r3)
	}
}

// TestDeltaSettlesToUnchanged_MultipleChanges verifies repeated A->B->B->C->C transitions.
// With hash-only storage, A->B is "new" (not delta), B->B is unchanged, etc.
func TestDeltaSettlesToUnchanged_MultipleChanges(t *testing.T) {
	s := New()

	textA := `{"file":"f.go","lines":[1,10],"content":"version1","hash":"aaa"}`
	textB := `{"file":"f.go","lines":[1,10],"content":"version2","hash":"bbb"}`
	textC := `{"file":"f.go","lines":[1,10],"content":"version3","hash":"ccc"}`

	// Store A
	s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textA)

	// Change to B — treated as new (hash-only, no diff)
	r := s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if strings.Contains(r, "unchanged") {
		t.Fatalf("A->B should pass through (new), got: %s", r)
	}

	// B again — unchanged
	r = s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textB)
	if !strings.Contains(r, "unchanged") {
		t.Fatalf("B->B should be unchanged, got: %s", r)
	}

	// Change to C — treated as new (hash-only, no diff)
	r = s.PostProcess("read", []string{"f.go"}, map[string]any{}, nil, textC)
	if strings.Contains(r, "unchanged") {
		t.Fatalf("B->C should pass through (new), got: %s", r)
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
// much smaller than new content (e.g. signatures->full body), it's treated as
// new (hash-only storage), and re-reading settles to unchanged.
func TestDeltaSkippedWhenOldContentMuchSmaller(t *testing.T) {
	s := New()

	// Step 1: Store a short signature-like body
	shortBody := "func handleDo(ctx context.Context) (string, error)"
	resultSig := map[string]any{
		"content": shortBody,
		"file":    "cmd/mcp.go",
		"symbol":  "handleDo",
		"hash":    "abc",
	}
	delta := s.ProcessReadResult("read", resultSig, map[string]any{})
	if delta != nil {
		t.Fatalf("first read should return nil (new), got: %v", delta)
	}

	// Step 2: Read the full body — treated as new (hash-only, different content)
	fullBody := shortBody + " {\n" + strings.Repeat("\tline\n", 100) + "}"
	resultFull := map[string]any{
		"content": fullBody,
		"file":    "cmd/mcp.go",
		"symbol":  "handleDo",
		"hash":    "def",
	}
	delta = s.ProcessReadResult("read", resultFull, map[string]any{})
	if delta != nil {
		t.Fatalf("signatures->full body should return nil (new), got: %v", delta)
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

// TestDeltaNewThenUnchanged_SimilarSizedContent verifies that normal edits
// (similar-sized old and new) are treated as new, then settle to unchanged.
func TestDeltaNewThenUnchanged_SimilarSizedContent(t *testing.T) {
	s := New()

	// Store a reasonably sized function body
	oldBody := "func foo() {\n\treturn 1\n}\n" + strings.Repeat("// padding\n", 20)
	resultA := map[string]any{
		"content": oldBody,
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "aaa",
	}
	s.ProcessReadResult("read", resultA, map[string]any{})

	// Change one line in a similar-sized body — with hash-only, treated as new
	newBody := "func foo() {\n\treturn 2\n}\n" + strings.Repeat("// padding\n", 20)
	resultB := map[string]any{
		"content": newBody,
		"file":    "f.go",
		"symbol":  "foo",
		"hash":    "bbb",
	}
	delta := s.ProcessReadResult("read", resultB, map[string]any{})
	if delta != nil {
		t.Fatalf("changed content should return nil (new, hash-only), got: %v", delta)
	}

	// Re-read same content — should be unchanged
	delta = s.ProcessReadResult("read", resultB, map[string]any{})
	if delta == nil {
		t.Fatal("re-read should return unchanged")
	}
	if delta["unchanged"] != true {
		t.Fatalf("expected unchanged=true, got: %v", delta)
	}
}

