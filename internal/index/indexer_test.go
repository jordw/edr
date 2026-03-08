package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoMapGrep_AlternationCaseInsensitive(t *testing.T) {
	tmp := t.TempDir()
	// Create a Go file with mixed-case symbols that test alternation scoping.
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(`package main

func dispatch() {}
func Handle() {}
func ProcessRequest() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, _, err := IndexRepo(ctx, db); err != nil {
		t.Fatal(err)
	}

	// "dispatch|Handle" with (?i) should match both "dispatch" and "Handle".
	// Before the fix, (?i) only applied to "dispatch" (first alternative).
	out, err := RepoMap(ctx, db, WithGrep("dispatch|Handle"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "dispatch") {
		t.Error("grep should match 'dispatch'")
	}
	if !strings.Contains(out, "Handle") {
		t.Error("grep should match 'Handle'")
	}
	if strings.Contains(out, "ProcessRequest") {
		t.Error("grep should NOT match 'ProcessRequest'")
	}

	// Verify case-insensitivity applies to all alternatives:
	// "DISPATCH|handle" should still match both symbols.
	out2, err := RepoMap(ctx, db, WithGrep("DISPATCH|handle"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "dispatch") {
		t.Error("case-insensitive grep should match 'dispatch' via 'DISPATCH'")
	}
	if !strings.Contains(out2, "Handle") {
		t.Error("case-insensitive grep should match 'Handle' via 'handle'")
	}
}
