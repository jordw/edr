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

func TestOpenDBPrunesOutOfRootEntries(t *testing.T) {
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
	if files != 0 || symbols != 0 {
		t.Fatalf("expected prune to remove outside entries, got files=%d symbols=%d", files, symbols)
	}
}
