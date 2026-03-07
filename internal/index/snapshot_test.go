package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexRepoWritesSnapshotAndHasStaleFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mainFile := filepath.Join(root, "main.go")
	writeSnapshotTestFile(t, mainFile, "package main\n\nfunc main() {}\n")

	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	if _, ok, err := ReadIndexedSnapshot(root); err != nil {
		t.Fatalf("ReadIndexedSnapshot: %v", err)
	} else if !ok {
		t.Fatal("expected snapshot to be written after indexing")
	}

	stale, err := HasStaleFiles(ctx, db)
	if err != nil {
		t.Fatalf("HasStaleFiles: %v", err)
	}
	if stale {
		t.Fatal("expected snapshot to match immediately after indexing")
	}

	writeSnapshotTestFile(t, mainFile, "package main\n\nfunc main() { println(\"hi\") }\n")
	bumpMtime(t, mainFile)

	stale, err = HasStaleFiles(ctx, db)
	if err != nil {
		t.Fatalf("HasStaleFiles after edit: %v", err)
	}
	if !stale {
		t.Fatal("expected supported file edit to mark snapshot stale")
	}
}

func TestHasStaleFilesIgnoresUnsupportedFiles(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mainFile := filepath.Join(root, "main.go")
	readme := filepath.Join(root, "README.md")
	writeSnapshotTestFile(t, mainFile, "package main\n\nfunc main() {}\n")
	writeSnapshotTestFile(t, readme, "hello\n")

	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	writeSnapshotTestFile(t, readme, "updated\n")
	bumpMtime(t, readme)

	stale, err := HasStaleFiles(ctx, db)
	if err != nil {
		t.Fatalf("HasStaleFiles: %v", err)
	}
	if stale {
		t.Fatal("expected unsupported file edit to leave snapshot fresh")
	}
}

func TestHasStaleFilesTracksGitIgnoreChanges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mainFile := filepath.Join(root, "main.go")
	gitignore := filepath.Join(root, ".gitignore")
	writeSnapshotTestFile(t, mainFile, "package main\n\nfunc main() {}\n")
	writeSnapshotTestFile(t, gitignore, "*.tmp\n")

	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	writeSnapshotTestFile(t, gitignore, "*.log\n")
	bumpMtime(t, gitignore)

	stale, err := HasStaleFiles(ctx, db)
	if err != nil {
		t.Fatalf("HasStaleFiles: %v", err)
	}
	if !stale {
		t.Fatal("expected .gitignore edit to mark snapshot stale")
	}
}

func TestHasStaleFilesIgnoresTouchOnlyChanges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mainFile := filepath.Join(root, "main.go")
	writeSnapshotTestFile(t, mainFile, "package main\n\nfunc main() {}\n")

	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	bumpMtime(t, mainFile)

	stale, err := HasStaleFiles(ctx, db)
	if err != nil {
		t.Fatalf("HasStaleFiles after touch: %v", err)
	}
	if stale {
		t.Fatal("expected touch-only mtime change to leave snapshot fresh")
	}
}

func TestIndexFileInvalidatesSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mainFile := filepath.Join(root, "main.go")
	writeSnapshotTestFile(t, mainFile, "package main\n\nfunc main() {}\n")

	db, err := OpenDB(root)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.Close()

	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatalf("IndexRepo: %v", err)
	}

	if _, ok, err := ReadIndexedSnapshot(root); err != nil {
		t.Fatalf("ReadIndexedSnapshot before IndexFile: %v", err)
	} else if !ok {
		t.Fatal("expected snapshot before IndexFile")
	}

	writeSnapshotTestFile(t, mainFile, "package main\n\nfunc main() { println(\"hi\") }\n")
	bumpMtime(t, mainFile)

	if err := IndexFile(ctx, db, mainFile); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	if _, ok, err := ReadIndexedSnapshot(root); err != nil {
		t.Fatalf("ReadIndexedSnapshot after IndexFile: %v", err)
	} else if ok {
		t.Fatal("expected IndexFile to invalidate the snapshot")
	}

	stale, err := HasStaleFiles(ctx, db)
	if err != nil {
		t.Fatalf("HasStaleFiles after IndexFile: %v", err)
	}
	if stale {
		t.Fatal("expected mtime fallback to treat freshly indexed file as current")
	}
}

func writeSnapshotTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func bumpMtime(t *testing.T, path string) {
	t.Helper()
	ts := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, ts, ts); err != nil {
		t.Fatalf("Chtimes(%s): %v", path, err)
	}
}
