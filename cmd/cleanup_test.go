package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanEdrDir_StaleSessionsRemoved(t *testing.T) {
	edrDir := t.TempDir()
	sessDir := filepath.Join(edrDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// Fresh session — should survive
	fresh := filepath.Join(sessDir, "fresh.json")
	os.WriteFile(fresh, []byte(`{}`), 0644)

	// Stale session — should be removed
	stale := filepath.Join(sessDir, "stale.json")
	os.WriteFile(stale, []byte(`{}`), 0644)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(stale, old, old)

	cleanEdrDir(edrDir)

	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh session should not be removed")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale session should be removed")
	}
}

func TestCleanEdrDir_DeadPpidRemoved(t *testing.T) {
	edrDir := t.TempDir()
	sessDir := filepath.Join(edrDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// PPID for a process that almost certainly doesn't exist
	dead := filepath.Join(sessDir, "ppid_9999999")
	os.WriteFile(dead, []byte("abc123"), 0644)

	// PPID for PID 1 (init/launchd — always alive)
	alive := filepath.Join(sessDir, "ppid_1")
	os.WriteFile(alive, []byte("def456"), 0644)

	cleanEdrDir(edrDir)

	if _, err := os.Stat(dead); err == nil {
		t.Error("dead ppid mapping should be removed")
	}
	if _, err := os.Stat(alive); err != nil {
		t.Error("alive ppid mapping should not be removed")
	}
}

func TestCleanEdrDir_StaleRunBaselinesRemoved(t *testing.T) {
	edrDir := t.TempDir()
	runDir := filepath.Join(edrDir, "delta")
	os.MkdirAll(runDir, 0755)

	// Fresh baseline — should survive
	fresh := filepath.Join(runDir, "abc123.last")
	os.WriteFile(fresh, []byte("output"), 0644)

	// Stale baseline — should be removed
	stale := filepath.Join(runDir, "def456.last")
	os.WriteFile(stale, []byte("old output"), 0644)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(stale, old, old)

	cleanEdrDir(edrDir)

	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh run baseline should not be removed")
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale run baseline should be removed")
	}
}

func TestMaybeCleanEdrDir_RateLimited(t *testing.T) {
	edrDir := t.TempDir()
	sessDir := filepath.Join(edrDir, "sessions")
	os.MkdirAll(sessDir, 0755)

	// First call should run cleanup
	stale := filepath.Join(sessDir, "stale.json")
	os.WriteFile(stale, []byte(`{}`), 0644)
	old := time.Now().Add(-8 * 24 * time.Hour)
	os.Chtimes(stale, old, old)

	maybeCleanEdrDir(edrDir)

	if _, err := os.Stat(stale); err == nil {
		t.Error("first call should have cleaned stale file")
	}

	// Second call should be rate-limited (marker is fresh)
	stale2 := filepath.Join(sessDir, "stale2.json")
	os.WriteFile(stale2, []byte(`{}`), 0644)
	os.Chtimes(stale2, old, old)

	maybeCleanEdrDir(edrDir)

	if _, err := os.Stat(stale2); err != nil {
		t.Error("second call should be rate-limited; stale2 should still exist")
	}
}
