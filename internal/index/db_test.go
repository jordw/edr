package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolvePathRejectsOutsideRoot(t *testing.T) {
	root := t.TempDir()

	got, err := ResolvePath(root, "internal/file.go")
	if err != nil {
		t.Fatalf("ResolvePath inside root: %v", err)
	}
	want := filepath.Join(root, "internal", "file.go")
	if got != want {
		t.Fatalf("ResolvePath mismatch: got %q want %q", got, want)
	}

	if _, err := ResolvePath(root, "../outside.go"); err == nil {
		t.Fatal("ResolvePath should reject paths outside the repo root")
	}
}

func TestOpenDBConcurrencySettings(t *testing.T) {
	root := t.TempDir()
	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	// Verify WAL mode is enabled
	var journalMode string
	if err := db.db.QueryRow("PRAGMA journal_mode").Scan(&journalMode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	// Verify busy_timeout is set (in-process hint; cross-process retry is in retryDB)
	var timeout int
	if err := db.db.QueryRow("PRAGMA busy_timeout").Scan(&timeout); err != nil {
		t.Fatalf("query busy_timeout: %v", err)
	}
	if timeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", timeout)
	}
}

func TestIndexRepoPrunesOutOfRootEntries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "edr_write_test.go")
	if err := os.WriteFile(outsideFile, []byte("package main\nfunc Outside() {}\n"), 0644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}

	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}

	if err := db.UpsertFile(ctx, outsideFile, "deadbeef", 1); err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	if err := db.InsertSymbol(ctx, SymbolInfo{
		Name:      "Outside",
		Type:      "function",
		File:      outsideFile,
		StartLine: 2,
		EndLine:   2,
		StartByte: 13,
		EndByte:   31,
	}); err != nil {
		t.Fatalf("InsertSymbol: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db, err = OpenDB(root)
	if err != nil {
		t.Fatalf("reopen OpenDB: %v", err)
	}
	defer db.Close()

	files, symbols, err := db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if files != 1 || symbols != 1 {
		t.Fatalf("expected reopen to preserve entries until indexing, got files=%d symbols=%d", files, symbols)
	}

	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	files, symbols, err = db.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats after IndexRepo: %v", err)
	}
	if files != 0 || symbols != 0 {
		t.Fatalf("expected IndexRepo prune to remove outside entries, got files=%d symbols=%d", files, symbols)
	}
}
