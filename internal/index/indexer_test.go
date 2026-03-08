package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShouldIgnoreClaudeWorktrees(t *testing.T) {
	tmp := t.TempDir()

	// Create a Go file at the root.
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(`package main
func Hello() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Create a .claude/worktrees/agent-xxx/main.go file that should be excluded.
	worktreeDir := filepath.Join(tmp, ".claude", "worktrees", "agent-abc123")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, "main.go"), []byte(`package main
func HelloDuplicate() {}
`), 0644); err != nil {
		t.Fatal(err)
	}

	db, err := OpenDB(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx := context.Background()
	filesIndexed, _, err := IndexRepo(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	// Only the root main.go should be indexed, not the one under .claude/
	if filesIndexed != 1 {
		t.Errorf("expected 1 file indexed, got %d", filesIndexed)
	}

	out, err := RepoMap(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "HelloDuplicate") {
		t.Error(".claude/worktrees/ files should be excluded from indexing")
	}
	if !strings.Contains(out, "Hello") {
		t.Error("root main.go should be indexed")
	}
}

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
