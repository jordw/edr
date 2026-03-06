package edit

import (
	"strings"
	"testing"
)

func TestDiffPreview_Basic(t *testing.T) {
	content := "line1\nline2\nline3\nline4\nline5\n"
	path := tmpFile(t, content)

	// Replace "line3" (bytes 12-17) with "LINE_THREE"
	diff, err := DiffPreview(path, 12, 17, "LINE_THREE")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(diff, "-line3") {
		t.Error("diff should contain removed line")
	}
	if !strings.Contains(diff, "+LINE_THREE") {
		t.Error("diff should contain added line")
	}
	if !strings.Contains(diff, "@@") {
		t.Error("diff should contain hunk header")
	}
}

func TestDiffPreview_NoChange(t *testing.T) {
	content := "abc"
	path := tmpFile(t, content)

	diff, err := DiffPreview(path, 0, 3, "abc")
	if err != nil {
		t.Fatal(err)
	}

	// Should produce a diff even for "same" content (it's a span replacement)
	if diff == "" {
		t.Error("expected non-empty diff output")
	}
}

func TestDiffPreview_InvalidRange(t *testing.T) {
	path := tmpFile(t, "abc")

	_, err := DiffPreview(path, 10, 20, "x")
	if err == nil {
		t.Error("expected error for invalid range")
	}
}

func TestDiffPreview_FileNotFound(t *testing.T) {
	_, err := DiffPreview("/nonexistent/file.go", 0, 1, "x")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestDiffPreview_HasHeaders(t *testing.T) {
	path := tmpFile(t, "hello\nworld\n")

	diff, err := DiffPreview(path, 0, 5, "HELLO")
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(diff, "--- a/") {
		t.Error("diff should start with --- a/ header")
	}
	if !strings.Contains(diff, "+++ b/") {
		t.Error("diff should contain +++ b/ header")
	}
}
