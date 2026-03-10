package index

import (
	"context"
	"fmt"
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

	out, _, err := RepoMap(ctx, db)
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
	out, _, err := RepoMap(ctx, db, WithGrep("dispatch|Handle"))
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
	out2, _, err := RepoMap(ctx, db, WithGrep("DISPATCH|handle"))
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

func TestRepoMapBudgetReportsTruncated(t *testing.T) {
	tmp := t.TempDir()
	// Create many files so early-stop (which fires between files) can trigger.
	for i := 0; i < 20; i++ {
		var src strings.Builder
		src.WriteString("package main\n\n")
		for j := 0; j < 5; j++ {
			fmt.Fprintf(&src, "func File%02d_Func%02d() {}\n", i, j)
		}
		name := fmt.Sprintf("f%02d.go", i)
		if err := os.WriteFile(filepath.Join(tmp, name), []byte(src.String()), 0644); err != nil {
			t.Fatal(err)
		}
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

	// Budget of 30 tokens (~120 chars) should truncate 20 files x 5 funcs.
	out, truncated, err := RepoMap(ctx, db, WithBudget(30))
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("expected RepoMap to report truncated=true with small budget")
	}
	// Output should be non-empty but not contain the last file's functions.
	if strings.Contains(out, "File19") {
		t.Error("expected budget to stop before the last file")
	}

	// Large budget should not truncate.
	_, truncated2, err := RepoMap(ctx, db, WithBudget(100000))
	if err != nil {
		t.Fatal(err)
	}
	if truncated2 {
		t.Error("expected large budget to not truncate")
	}
}

func TestRepoMapGrepLikeWildcards(t *testing.T) {
	tmp := t.TempDir()
	// Create symbols where LIKE wildcards (%, _) could cause overmatching.
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte(`package main

func Get_Item() {}
func GetBigItem() {}
func Percent100() {}
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

	// Grep for literal "_" should only match Get_Item, not GetBigItem.
	out, _, err := RepoMap(ctx, db, WithGrep("_"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Get_Item") {
		t.Error("grep '_' should match 'Get_Item'")
	}
	if strings.Contains(out, "GetBigItem") {
		t.Error("grep '_' should NOT match 'GetBigItem' (underscore is not a wildcard)")
	}

	// Grep for literal "%" should match nothing (no symbol contains %).
	out2, _, err := RepoMap(ctx, db, WithGrep("%"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2, "Percent100") {
		t.Error("grep '%%' should NOT match 'Percent100' (percent is not a wildcard)")
	}
}
