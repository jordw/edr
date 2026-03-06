package edit

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func tmpFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.go")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func hashOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:8]
}

// --- FileHash ---

func TestFileHash(t *testing.T) {
	content := "hello world"
	path := tmpFile(t, content)

	got, err := FileHash(path)
	if err != nil {
		t.Fatal(err)
	}
	want := hashOf(content)
	if got != want {
		t.Errorf("FileHash = %q, want %q", got, want)
	}
}

func TestFileHash_NotFound(t *testing.T) {
	_, err := FileHash("/nonexistent/file.go")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// --- ReplaceSpan ---

func TestReplaceSpan_Basic(t *testing.T) {
	path := tmpFile(t, "aaabbbccc")

	err := ReplaceSpan(path, 3, 6, "BBB", "")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "aaaBBBccc" {
		t.Errorf("got %q, want %q", got, "aaaBBBccc")
	}
}

func TestReplaceSpan_Grow(t *testing.T) {
	path := tmpFile(t, "abcdef")

	err := ReplaceSpan(path, 2, 4, "XXXXX", "")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "abXXXXXef" {
		t.Errorf("got %q, want %q", got, "abXXXXXef")
	}
}

func TestReplaceSpan_Shrink(t *testing.T) {
	path := tmpFile(t, "abcdef")

	err := ReplaceSpan(path, 1, 5, "X", "")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "aXf" {
		t.Errorf("got %q, want %q", got, "aXf")
	}
}

func TestReplaceSpan_Delete(t *testing.T) {
	path := tmpFile(t, "abcdef")

	err := ReplaceSpan(path, 2, 4, "", "")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "abef" {
		t.Errorf("got %q, want %q", got, "abef")
	}
}

func TestReplaceSpan_FullReplace(t *testing.T) {
	path := tmpFile(t, "old content")

	err := ReplaceSpan(path, 0, 11, "new content", "")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "new content" {
		t.Errorf("got %q, want %q", got, "new content")
	}
}

func TestReplaceSpan_HashMatch(t *testing.T) {
	content := "abcdef"
	path := tmpFile(t, content)

	err := ReplaceSpan(path, 0, 3, "XXX", hashOf(content))
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "XXXdef" {
		t.Errorf("got %q, want %q", got, "XXXdef")
	}
}

func TestReplaceSpan_HashMismatch(t *testing.T) {
	path := tmpFile(t, "abcdef")

	err := ReplaceSpan(path, 0, 3, "XXX", "deadbeef")
	if err == nil {
		t.Error("expected error for hash mismatch")
	}

	// File should be unchanged
	got := readFile(t, path)
	if got != "abcdef" {
		t.Errorf("file was modified despite hash mismatch: %q", got)
	}
}

func TestReplaceSpan_InvalidRange(t *testing.T) {
	path := tmpFile(t, "abc")

	tests := []struct {
		name       string
		start, end uint32
	}{
		{"start beyond EOF", 10, 11},
		{"end beyond EOF", 0, 100},
		{"start > end", 3, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ReplaceSpan(path, tt.start, tt.end, "x", "")
			if err == nil {
				t.Error("expected error for invalid range")
			}
		})
	}
}

func TestReplaceSpan_NotFound(t *testing.T) {
	err := ReplaceSpan("/nonexistent/path", 0, 1, "x", "")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

// --- InsertAfterSpan ---

func TestInsertAfterSpan_Middle(t *testing.T) {
	path := tmpFile(t, "abcdef")

	err := InsertAfterSpan(path, 3, "XXX")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "abcXXXdef" {
		t.Errorf("got %q, want %q", got, "abcXXXdef")
	}
}

func TestInsertAfterSpan_Start(t *testing.T) {
	path := tmpFile(t, "abc")

	err := InsertAfterSpan(path, 0, "XXX")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "XXXabc" {
		t.Errorf("got %q, want %q", got, "XXXabc")
	}
}

func TestInsertAfterSpan_End(t *testing.T) {
	path := tmpFile(t, "abc")

	err := InsertAfterSpan(path, 3, "XXX")
	if err != nil {
		t.Fatal(err)
	}

	got := readFile(t, path)
	if got != "abcXXX" {
		t.Errorf("got %q, want %q", got, "abcXXX")
	}
}

func TestInsertAfterSpan_BeyondEOF(t *testing.T) {
	path := tmpFile(t, "abc")

	err := InsertAfterSpan(path, 100, "XXX")
	if err == nil {
		t.Error("expected error for position beyond EOF")
	}
}
