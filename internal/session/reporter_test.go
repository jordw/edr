package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReporter_EmptyDir(t *testing.T) {
	edrDir := t.TempDir()
	r := NewReporter(edrDir)
	got := r.Status()
	if got.Name != "session" {
		t.Errorf("Name: got %q, want session", got.Name)
	}
	if got.Exists {
		t.Error("Exists: want false when sessions dir is absent")
	}
	if got.Files != 0 || got.Bytes != 0 {
		t.Errorf("zero values expected, got Files=%d Bytes=%d", got.Files, got.Bytes)
	}
}

func TestReporter_OneSession(t *testing.T) {
	edrDir := t.TempDir()
	sessDir := filepath.Join(edrDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "s1.json"), []byte(`{"id":"s1"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := NewReporter(edrDir).Status()
	if !got.Exists {
		t.Error("Exists: want true with one session file")
	}
	if got.Files != 1 {
		t.Errorf("Files: got %d, want 1", got.Files)
	}
	if got.Bytes == 0 {
		t.Error("Bytes: want > 0")
	}
	if got.Extra != nil {
		t.Errorf("Extra: no checkpoints yet, want nil, got %v", got.Extra)
	}
}

func TestReporter_WithCheckpoints(t *testing.T) {
	edrDir := t.TempDir()
	sessDir := filepath.Join(edrDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"s1.json":              `{"id":"s1"}`,
		"cp_auto_1.json":       `{"id":"cp_auto_1"}`,
		"cp_auto_2.json":       `{"id":"cp_auto_2"}`,
		"non-json.txt":         `ignored`,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(sessDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got := NewReporter(edrDir).Status()
	if !got.Exists {
		t.Error("Exists: want true")
	}
	if got.Files != 1 {
		t.Errorf("Files (sessions only): got %d, want 1", got.Files)
	}
	cp, _ := got.Extra["checkpoints"].(int)
	if cp != 2 {
		t.Errorf("Extra.checkpoints: got %d, want 2", cp)
	}
}

func TestReporter_CheckpointsOnly(t *testing.T) {
	// If the session was abandoned but checkpoints linger, still report Exists.
	edrDir := t.TempDir()
	sessDir := filepath.Join(edrDir, "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "cp_manual_1.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := NewReporter(edrDir).Status()
	if !got.Exists {
		t.Error("Exists: want true with orphan checkpoint")
	}
	if got.Files != 0 {
		t.Errorf("Files: got %d, want 0 (checkpoints do not count)", got.Files)
	}
}
