package edit

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTransaction_SingleEdit(t *testing.T) {
	path := tmpFile(t, "aaabbbccc")

	tx := NewTransaction()
	tx.Add(path, 3, 6, "XXX", "")

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "aaaXXXccc" {
		t.Errorf("got %q, want %q", got, "aaaXXXccc")
	}
}

func TestTransaction_MultipleEditsReverseOrder(t *testing.T) {
	// Two edits on the same file: both should apply correctly
	// because the transaction sorts in reverse byte order.
	path := tmpFile(t, "aaa bbb ccc")

	tx := NewTransaction()
	tx.Add(path, 0, 3, "AAA", "")
	tx.Add(path, 8, 11, "CCC", "")

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "AAA bbb CCC" {
		t.Errorf("got %q, want %q", got, "AAA bbb CCC")
	}
}

func TestTransaction_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.txt")
	path2 := filepath.Join(dir, "b.txt")
	os.WriteFile(path1, []byte("hello"), 0644)
	os.WriteFile(path2, []byte("world"), 0644)

	tx := NewTransaction()
	tx.Add(path1, 0, 5, "HELLO", "")
	tx.Add(path2, 0, 5, "WORLD", "")

	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	got1 := readFile(t, path1)
	got2 := readFile(t, path2)
	if got1 != "HELLO" {
		t.Errorf("file1: got %q, want %q", got1, "HELLO")
	}
	if got2 != "WORLD" {
		t.Errorf("file2: got %q, want %q", got2, "WORLD")
	}
}

func TestTransaction_HashCheckOnFirstEdit(t *testing.T) {
	content := "abcdef"
	path := tmpFile(t, content)

	tx := NewTransaction()
	tx.Add(path, 0, 3, "XXX", "deadbeef") // wrong hash

	err := tx.Commit()
	if err == nil {
		t.Error("expected error for hash mismatch")
	}

	// File should be unchanged
	got := readFile(t, path)
	if got != content {
		t.Errorf("file modified despite hash error: %q", got)
	}
}

func TestTransaction_Preview(t *testing.T) {
	tx := NewTransaction()
	tx.Add("file.go", 0, 10, "new code", "abc123")
	tx.Add("file.go", 20, 30, "more code", "")

	edits := tx.Preview()
	if len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %d", len(edits))
	}
}
