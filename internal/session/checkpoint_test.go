package session

import (
	"os"
	"path/filepath"
	"testing"
)

// --- GetDirtyFiles ---

func TestGetDirtyFiles_Empty(t *testing.T) {
	s := New()
	if files := s.GetDirtyFiles(); len(files) != 0 {
		t.Errorf("expected 0 dirty files, got %d", len(files))
	}
}

func TestGetDirtyFiles_TracksEditsAndWrites(t *testing.T) {
	s := New()
	s.RecordOp("read", "a.go", "", "read_symbol", "symbol_read", true)
	s.RecordOp("edit", "b.go", "Foo", "replace_text", "text_replaced", true)
	s.RecordOp("write", "c.go", "", "write_file", "file_written", true)
	s.RecordOp("edit", "b.go", "Bar", "replace_text", "text_replaced", true) // duplicate file
	s.RecordOp("search", "", "", "search", "search", true)

	files := s.GetDirtyFiles()
	if len(files) != 2 {
		t.Fatalf("expected 2 dirty files, got %d: %v", len(files), files)
	}
	if files[0] != "b.go" || files[1] != "c.go" {
		t.Errorf("expected [b.go, c.go], got %v", files)
	}
}

func TestGetDirtyFiles_IgnoresFailedOps(t *testing.T) {
	s := New()
	s.RecordOp("edit", "fail.go", "", "replace_text", "text_replaced", false)
	if files := s.GetDirtyFiles(); len(files) != 0 {
		t.Errorf("failed edits should not be dirty: %v", files)
	}
}

// --- CreateCheckpoint ---

func TestCreateCheckpoint_Basic(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	// Create a file to snapshot
	os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("package main"), 0644)

	s := New()
	s.RecordOp("edit", "a.go", "Foo", "replace_text", "text_replaced", true)
	s.SetFocus("test focus")
	s.RecordVerify("passed")

	cp, err := s.CreateCheckpoint(sessDir, repoRoot, "before refactor", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}

	if cp.ID != "cp_1" {
		t.Errorf("id = %q, want cp_1", cp.ID)
	}
	if cp.Label != "before refactor" {
		t.Errorf("label = %q", cp.Label)
	}
	if len(cp.Files) != 1 {
		t.Fatalf("expected 1 file snapshot, got %d", len(cp.Files))
	}
	if cp.Files[0].Path != "a.go" {
		t.Errorf("file path = %q", cp.Files[0].Path)
	}
	if string(cp.Files[0].Content) != "package main" {
		t.Errorf("file content = %q", string(cp.Files[0].Content))
	}
	if cp.Session.Focus != "test focus" {
		t.Errorf("session focus = %q", cp.Session.Focus)
	}
	if cp.Session.LastVerifyStatus != "passed" {
		t.Errorf("verify status = %q", cp.Session.LastVerifyStatus)
	}
}

func TestCreateCheckpoint_IncrementingIDs(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	s := New()
	cp1, err := s.CreateCheckpoint(sessDir, repoRoot, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cp2, err := s.CreateCheckpoint(sessDir, repoRoot, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cp1.ID != "cp_1" || cp2.ID != "cp_2" {
		t.Errorf("ids: %s, %s", cp1.ID, cp2.ID)
	}
}

func TestCreateCheckpoint_SkipsMissingFiles(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	s := New()
	cp, err := s.CreateCheckpoint(sessDir, repoRoot, "", []string{"nonexistent.go"})
	if err != nil {
		t.Fatal(err)
	}
	// Missing files are now recorded with nil content (new file markers)
	// so that restore can delete them if they were created after the checkpoint.
	if len(cp.Files) != 1 {
		t.Errorf("expected 1 file snapshot (nil content marker), got %d", len(cp.Files))
	}
	if len(cp.Files) == 1 && cp.Files[0].Content != nil {
		t.Errorf("expected nil content for missing file, got %d bytes", len(cp.Files[0].Content))
	}
}

// --- RestoreCheckpoint ---

func TestRestoreCheckpoint_Basic(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	// Create initial file
	filePath := filepath.Join(repoRoot, "a.go")
	os.WriteFile(filePath, []byte("package main\nv1"), 0644)

	s := New()
	s.RecordOp("edit", "a.go", "", "replace_text", "text_replaced", true)
	s.SetFocus("original focus")
	s.RecordVerify("passed")

	// Create checkpoint
	_, err := s.CreateCheckpoint(sessDir, repoRoot, "v1", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Modify the file and session
	os.WriteFile(filePath, []byte("package main\nv2 modified"), 0644)
	s.RecordOp("edit", "a.go", "", "replace_text", "text_replaced", true)
	s.SetFocus("new focus")
	s.RecordVerify("failed")

	// Restore
	restored, notRemoved, preRestoreID, err := s.RestoreCheckpoint(
		sessDir, repoRoot, "cp_1", true, []string{"a.go"},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Check file was restored
	content, _ := os.ReadFile(filePath)
	if string(content) != "package main\nv1" {
		t.Errorf("file content after restore: %q", string(content))
	}

	// Check restored file list
	if len(restored) != 1 || restored[0] != "a.go" {
		t.Errorf("restored = %v", restored)
	}

	// Check pre-restore checkpoint was created
	if preRestoreID == "" {
		t.Error("expected pre-restore checkpoint")
	}

	// Check no files flagged as not-removed
	if len(notRemoved) != 0 {
		t.Errorf("not_removed = %v", notRemoved)
	}

	// Check session state was restored
	if s.GetFocus() != "original focus" {
		t.Errorf("focus after restore = %q", s.GetFocus())
	}
	status, _ := s.BuildState()
	if status != "passed" {
		t.Errorf("build state after restore = %q", status)
	}

	// Check restore was recorded in op log
	ops := s.GetRecentOps(1)
	if len(ops) != 1 || ops[0].Kind != "checkpoint_restored" {
		t.Errorf("last op after restore: %+v", ops)
	}
}

func TestRestoreCheckpoint_NoSave(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("v1"), 0644)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "", []string{"a.go"})

	os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("v2"), 0644)

	_, _, preRestoreID, err := s.RestoreCheckpoint(
		sessDir, repoRoot, "cp_1", false, []string{"a.go"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if preRestoreID != "" {
		t.Error("expected no pre-restore checkpoint with --no-save")
	}
}

func TestRestoreCheckpoint_DetectsNewFiles(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("v1"), 0644)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "", []string{"a.go"})

	// "Create" a new file after checkpoint
	_, _, _, err := s.RestoreCheckpoint(
		sessDir, repoRoot, "cp_1", false, []string{"a.go", "new.go"},
	)
	if err != nil {
		t.Fatal(err)
	}
	// new.go should be flagged but not touched — tested via notRemoved
}

func TestRestoreCheckpoint_AssumptionsRestored(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	s := New()
	s.RecordAssumption("a.go:Foo", "func Foo() error", "r1")
	s.CreateCheckpoint(sessDir, repoRoot, "", nil)

	// Add more assumptions after checkpoint
	s.RecordAssumption("b.go:Bar", "func Bar() int", "r5")

	s.RestoreCheckpoint(sessDir, repoRoot, "cp_1", false, nil)

	assumptions := s.GetAssumptions()
	if len(assumptions) != 1 {
		t.Fatalf("expected 1 assumption after restore, got %d", len(assumptions))
	}
	if _, ok := assumptions["a.go:Foo"]; !ok {
		t.Error("expected a.go:Foo assumption to be restored")
	}
}

// --- ListCheckpoints ---

func TestListCheckpoints_Empty(t *testing.T) {
	dir := t.TempDir()
	infos := ListCheckpoints(dir)
	if len(infos) != 0 {
		t.Errorf("expected 0 checkpoints, got %d", len(infos))
	}
}

func TestListCheckpoints_SortedByTime(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "first", nil)
	s.RecordOp("edit", "a.go", "", "replace_text", "text_replaced", true)
	s.CreateCheckpoint(sessDir, repoRoot, "second", nil)

	infos := ListCheckpoints(sessDir)
	if len(infos) != 2 {
		t.Fatalf("expected 2 checkpoints, got %d", len(infos))
	}
	if infos[0].Label != "first" || infos[1].Label != "second" {
		t.Errorf("order: %s, %s", infos[0].Label, infos[1].Label)
	}
}

// --- DropCheckpoint ---

func TestDropCheckpoint(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "", nil)

	if err := DropCheckpoint(sessDir, "cp_1"); err != nil {
		t.Fatal(err)
	}
	infos := ListCheckpoints(sessDir)
	if len(infos) != 0 {
		t.Errorf("expected 0 after drop, got %d", len(infos))
	}
}

func TestDropCheckpoint_NotFound(t *testing.T) {
	dir := t.TempDir()
	if err := DropCheckpoint(dir, "cp_999"); err == nil {
		t.Error("expected error for nonexistent checkpoint")
	}
}

// --- DiffCheckpoint ---

func TestDiffCheckpoint_NoChanges(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	os.WriteFile(filepath.Join(repoRoot, "a.go"), []byte("package main"), 0644)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "", []string{"a.go"})

	diffs, err := DiffCheckpoint(sessDir, repoRoot, "cp_1", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 0 {
		t.Errorf("expected no diffs, got %v", diffs)
	}
}

func TestDiffCheckpoint_Modified(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	filePath := filepath.Join(repoRoot, "a.go")
	os.WriteFile(filePath, []byte("v1"), 0644)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "", []string{"a.go"})

	os.WriteFile(filePath, []byte("v2"), 0644)

	diffs, err := DiffCheckpoint(sessDir, repoRoot, "cp_1", []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Status != "modified" {
		t.Errorf("diffs = %v", diffs)
	}
}

func TestDiffCheckpoint_CreatedFile(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "", nil)

	diffs, err := DiffCheckpoint(sessDir, repoRoot, "cp_1", []string{"new.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Status != "created" {
		t.Errorf("diffs = %v", diffs)
	}
}

// --- Restore creates pre-restore checkpoint ---

func TestRestoreCheckpoint_PreRestoreIsRestorable(t *testing.T) {
	dir := t.TempDir()
	sessDir := filepath.Join(dir, "sessions")
	repoRoot := filepath.Join(dir, "repo")
	os.MkdirAll(repoRoot, 0755)

	filePath := filepath.Join(repoRoot, "a.go")
	os.WriteFile(filePath, []byte("v1"), 0644)

	s := New()
	s.CreateCheckpoint(sessDir, repoRoot, "", []string{"a.go"})

	// Modify to v2
	os.WriteFile(filePath, []byte("v2"), 0644)
	s.SetFocus("v2 focus")

	// Restore to cp_1 (saves v2 state as pre-restore)
	_, _, preRestoreID, err := s.RestoreCheckpoint(sessDir, repoRoot, "cp_1", true, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}

	// Verify we're at v1
	content, _ := os.ReadFile(filePath)
	if string(content) != "v1" {
		t.Fatalf("expected v1, got %q", string(content))
	}

	// Now restore the pre-restore checkpoint to get back to v2
	_, _, _, err = s.RestoreCheckpoint(sessDir, repoRoot, preRestoreID, false, []string{"a.go"})
	if err != nil {
		t.Fatal(err)
	}

	content, _ = os.ReadFile(filePath)
	if string(content) != "v2" {
		t.Errorf("expected v2 after restoring pre-restore, got %q", string(content))
	}
}
