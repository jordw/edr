package warnings

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jordw/edr/internal/session"
)

func TestCheckExternalMods_NoTrackedFiles(t *testing.T) {
	sess := session.New()
	w := Check(sess, "/tmp")
	if len(w) != 0 {
		t.Errorf("expected 0 warnings, got %d", len(w))
	}
}

func TestCheckExternalMods_UnchangedFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.go")
	os.WriteFile(f, []byte("package main"), 0644)

	info, _ := os.Stat(f)
	sess := session.New()
	sess.RecordFileMtime("test.go", info.ModTime().UnixMicro(), fileHash(f), "r1")

	w := Check(sess, dir)
	if len(w) != 0 {
		t.Errorf("expected 0 warnings for unchanged file, got %d", len(w))
	}
}

func TestCheckExternalMods_ContentChanged(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.go")
	os.WriteFile(f, []byte("package main"), 0644)

	info, _ := os.Stat(f)
	sess := session.New()
	sess.RecordFileMtime("test.go", info.ModTime().UnixMicro(), fileHash(f), "r1")

	// Modify the file externally
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(f, []byte("package main\n\nfunc hello() {}"), 0644)

	w := Check(sess, dir)
	if len(w) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(w))
	}
	if w[0].Key == "" {
		t.Error("warning key should not be empty")
	}
	if w[0].Message == "" {
		t.Error("warning message should not be empty")
	}
}

func TestCheckExternalMods_TouchWithoutChange(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.go")
	os.WriteFile(f, []byte("package main"), 0644)

	info, _ := os.Stat(f)
	sess := session.New()
	sess.RecordFileMtime("test.go", info.ModTime().UnixMicro(), fileHash(f), "r1")

	// Touch the file (same content, new mtime)
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(f, []byte("package main"), 0644)

	w := Check(sess, dir)
	if len(w) != 0 {
		t.Errorf("expected 0 warnings for touch-only, got %d", len(w))
	}

	// Verify mtime was silently updated
	mtimes := sess.GetFileMtimes()
	newInfo, _ := os.Stat(f)
	if mtimes["test.go"].Mtime != newInfo.ModTime().UnixMicro() {
		t.Error("mtime should have been updated after touch-only")
	}
}

func TestCheckExternalMods_DeletedFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.go")
	os.WriteFile(f, []byte("package main"), 0644)

	info, _ := os.Stat(f)
	sess := session.New()
	sess.RecordFileMtime("test.go", info.ModTime().UnixMicro(), fileHash(f), "r1")

	// Delete the file
	os.Remove(f)

	w := Check(sess, dir)
	if len(w) != 1 {
		t.Fatalf("expected 1 warning for deleted file, got %d", len(w))
	}
	if w[0].Key != "ext_del:test.go" {
		t.Errorf("unexpected key: %s", w[0].Key)
	}
}

func TestCheckExternalMods_StructuredFields(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "test.go")
	os.WriteFile(f, []byte("package main"), 0644)

	info, _ := os.Stat(f)
	sess := session.New()
	sess.RecordFileMtime("test.go", info.ModTime().UnixMicro(), fileHash(f), "r3")

	// Modify externally
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(f, []byte("package changed"), 0644)

	w := Check(sess, dir)
	if len(w) != 1 {
		t.Fatalf("expected 1 warning, got %d", len(w))
	}
	if w[0].File != "test.go" {
		t.Errorf("File = %q, want test.go", w[0].File)
	}
	if w[0].Kind != "modified" {
		t.Errorf("Kind = %q, want modified", w[0].Kind)
	}
	if w[0].OpID != "r3" {
		t.Errorf("OpID = %q, want r3", w[0].OpID)
	}
}
