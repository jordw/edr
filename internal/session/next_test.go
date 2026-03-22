package session

import "testing"
// --- Op log ---

func TestRecordOp_Basic(t *testing.T) {
	s := New()
	s.RecordOp("read", "f.go", "Foo", "read_symbol", "symbol_read", true)
	ops := s.GetRecentOps(0)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	op := ops[0]
	if op.OpID != "r1" {
		t.Errorf("op_id = %q, want r1", op.OpID)
	}
	if op.Cmd != "read" || op.File != "f.go" || op.Symbol != "Foo" {
		t.Errorf("unexpected op fields: %+v", op)
	}
	if op.Kind != "symbol_read" || !op.OK {
		t.Errorf("kind=%q ok=%v", op.Kind, op.OK)
	}
}

func TestRecordOp_IncrementingIDs(t *testing.T) {
	s := New()
	s.RecordOp("read", "a.go", "", "read_symbol", "symbol_read", true)
	s.RecordOp("edit", "b.go", "", "replace_text", "text_replaced", true)
	s.RecordOp("search", "", "pat", "search", "search", true)
	ops := s.GetRecentOps(0)
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	if ops[0].OpID != "r1" || ops[1].OpID != "e2" || ops[2].OpID != "s3" {
		t.Errorf("ids: %s, %s, %s", ops[0].OpID, ops[1].OpID, ops[2].OpID)
	}
}

func TestRecordOp_FIFOEviction(t *testing.T) {
	s := New()
	for i := 0; i < MaxOpLogEntries+10; i++ {
		s.RecordOp("read", "f.go", "", "read_symbol", "symbol_read", true)
	}
	ops := s.GetRecentOps(0)
	if len(ops) != MaxOpLogEntries {
		t.Errorf("expected %d ops after eviction, got %d", MaxOpLogEntries, len(ops))
	}
	// First op should be the 11th one recorded (after 10 evictions)
	if ops[0].OpID != "r11" {
		t.Errorf("first op after eviction: %q, want r11", ops[0].OpID)
	}
}

func TestGetRecentOps_Subset(t *testing.T) {
	s := New()
	for i := 0; i < 5; i++ {
		s.RecordOp("read", "f.go", "", "read_symbol", "symbol_read", true)
	}
	ops := s.GetRecentOps(3)
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	if ops[0].OpID != "r3" {
		t.Errorf("first of last 3: %q, want r3", ops[0].OpID)
	}
}

func TestOpCount_RestoredOnLoad(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sess.json"

	// Create session with some ops
	s := New()
	s.RecordOp("read", "f.go", "", "read_symbol", "symbol_read", true)
	s.RecordOp("edit", "f.go", "", "replace_text", "text_replaced", true)
	s.SaveToFile(path)

	// Load and record another op
	s2 := LoadFromFile(path)
	s2.RecordOp("search", "", "", "search", "search", true)
	ops := s2.GetRecentOps(0)
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops, got %d", len(ops))
	}
	// New op should continue from e2 → s3
	if ops[2].OpID != "s3" {
		t.Errorf("op after reload: %q, want s3", ops[2].OpID)
	}
}

// --- Focus ---

func TestFocus_SetAndGet(t *testing.T) {
	s := New()
	if s.GetFocus() != "" {
		t.Error("focus should start empty")
	}
	s.SetFocus("rename Timeout")
	if s.GetFocus() != "rename Timeout" {
		t.Errorf("focus = %q", s.GetFocus())
	}
	s.SetFocus("")
	if s.GetFocus() != "" {
		t.Error("focus should be clearable")
	}
}

func TestFocus_PersistsAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sess.json"
	s := New()
	s.SetFocus("test goal")
	s.SaveToFile(path)
	s2 := LoadFromFile(path)
	if s2.GetFocus() != "test goal" {
		t.Errorf("focus after reload: %q", s2.GetFocus())
	}
}

// --- Assumption tracking ---

func TestRecordAssumption_Basic(t *testing.T) {
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r1")
	assumptions := s.GetAssumptions()
	if len(assumptions) != 1 {
		t.Fatalf("expected 1 assumption, got %d", len(assumptions))
	}
	entry := assumptions["f.go:Foo"]
	if entry.OpID != "r1" {
		t.Errorf("op_id = %q, want r1", entry.OpID)
	}
	if entry.SigHash != SigHash("func Foo() error") {
		t.Error("sig hash mismatch")
	}
}

func TestCheckAssumptions_NoChange(t *testing.T) {
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r1")
	currentSigs := map[string]string{
		"f.go:Foo": SigHash("func Foo() error"),
	}
	stale := s.CheckAssumptions(currentSigs)
	if len(stale) != 0 {
		t.Errorf("expected 0 stale, got %d", len(stale))
	}
}

func TestCheckAssumptions_SignatureChanged(t *testing.T) {
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r1")
	currentSigs := map[string]string{
		"f.go:Foo": SigHash("func Foo(ctx context.Context) error"),
	}
	stale := s.CheckAssumptions(currentSigs)
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale, got %d", len(stale))
	}
	if stale[0].File != "f.go" || stale[0].Symbol != "Foo" {
		t.Errorf("stale entry: %+v", stale[0])
	}
	if stale[0].AssumedAt != "r1" {
		t.Errorf("assumed_at = %q, want r1", stale[0].AssumedAt)
	}
}

func TestCheckAssumptions_SymbolDeleted(t *testing.T) {
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r1")
	currentSigs := map[string]string{} // Foo no longer exists
	stale := s.CheckAssumptions(currentSigs)
	if len(stale) != 1 {
		t.Fatalf("expected 1 stale, got %d", len(stale))
	}
	if stale[0].Current != "" {
		t.Error("deleted symbol should have empty current hash")
	}
}

func TestAssumption_OverwriteOnReread(t *testing.T) {
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r1")
	s.RecordAssumption("f.go:Foo", "func Foo(ctx context.Context) error", "r5")

	assumptions := s.GetAssumptions()
	if assumptions["f.go:Foo"].OpID != "r5" {
		t.Error("re-read should overwrite assumption")
	}
	// Should not be stale against the new signature
	currentSigs := map[string]string{
		"f.go:Foo": SigHash("func Foo(ctx context.Context) error"),
	}
	if len(s.CheckAssumptions(currentSigs)) != 0 {
		t.Error("should not be stale after re-read")
	}
}

func TestUpdateAssumptionOpID(t *testing.T) {
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r?")
	s.UpdateAssumptionOpID("f.go:Foo", "r3")
	assumptions := s.GetAssumptions()
	if assumptions["f.go:Foo"].OpID != "r3" {
		t.Errorf("op_id = %q, want r3", assumptions["f.go:Foo"].OpID)
	}
	// SigHash should be unchanged
	if assumptions["f.go:Foo"].SigHash != SigHash("func Foo() error") {
		t.Error("sig hash should not change on op ID update")
	}
}

func TestClearAssumption(t *testing.T) {
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r1")
	s.ClearAssumption("f.go:Foo")
	if len(s.GetAssumptions()) != 0 {
		t.Error("assumption should be cleared")
	}
}

func TestAssumptions_PersistAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sess.json"
	s := New()
	s.RecordAssumption("f.go:Foo", "func Foo() error", "r1")
	s.SaveToFile(path)
	s2 := LoadFromFile(path)
	assumptions := s2.GetAssumptions()
	if len(assumptions) != 1 {
		t.Fatalf("expected 1 assumption after reload, got %d", len(assumptions))
	}
	if assumptions["f.go:Foo"].OpID != "r1" {
		t.Error("assumption op_id not preserved")
	}
}

// --- Build state ---

func TestBuildState_Initial(t *testing.T) {
	s := New()
	status, editsSince := s.BuildState()
	if status != "" || editsSince {
		t.Errorf("initial: status=%q editsSince=%v", status, editsSince)
	}
}

func TestBuildState_AfterVerify(t *testing.T) {
	s := New()
	s.RecordVerify("passed")
	status, editsSince := s.BuildState()
	if status != "passed" || editsSince {
		t.Errorf("after verify: status=%q editsSince=%v", status, editsSince)
	}
}

func TestBuildState_AfterVerifyThenEdit(t *testing.T) {
	s := New()
	s.RecordVerify("passed")
	s.RecordEdit()
	status, editsSince := s.BuildState()
	if status != "unknown" || !editsSince {
		t.Errorf("after edit: status=%q editsSince=%v", status, editsSince)
	}
}

func TestBuildState_VerifyResetsEdits(t *testing.T) {
	s := New()
	s.RecordVerify("passed")
	s.RecordEdit()
	s.RecordVerify("failed")
	status, editsSince := s.BuildState()
	if status != "failed" || editsSince {
		t.Errorf("after re-verify: status=%q editsSince=%v", status, editsSince)
	}
}

func TestBuildState_PersistsAcrossLoad(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sess.json"
	s := New()
	s.RecordVerify("passed")
	s.RecordEdit()
	s.SaveToFile(path)
	s2 := LoadFromFile(path)
	status, editsSince := s2.BuildState()
	if status != "unknown" || !editsSince {
		t.Errorf("after reload: status=%q editsSince=%v", status, editsSince)
	}
}
