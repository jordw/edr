package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testStore(t *testing.T, root string) SymbolStore {
	t.Helper()
	return NewOnDemand(root)
}

func TestShouldIgnoreClaudeWorktrees(t *testing.T) {
	tmp := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\nfunc Hello() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	worktreeDir := filepath.Join(tmp, ".claude", "worktrees", "agent-abc123")
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreeDir, "main.go"), []byte("package main\nfunc HelloDuplicate() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db := testStore(t, tmp)
	defer db.Close()
	ctx := context.Background()

	out, _, err := RepoMap(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "HelloDuplicate") {
		t.Error(".claude/worktrees/ files should be excluded")
	}
	if !strings.Contains(out, "Hello") {
		t.Error("root main.go should be included")
	}
}

func TestRepoMapGrep_AlternationCaseInsensitive(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc dispatch() {}\nfunc Handle() {}\nfunc ProcessRequest() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db := testStore(t, tmp)
	defer db.Close()
	ctx := context.Background()

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

	out2, _, err := RepoMap(ctx, db, WithGrep("DISPATCH|handle"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, "dispatch") {
		t.Error("case-insensitive grep should match 'dispatch'")
	}
	if !strings.Contains(out2, "Handle") {
		t.Error("case-insensitive grep should match 'Handle'")
	}
}

func TestRepoMapBudgetReportsTruncated(t *testing.T) {
	tmp := t.TempDir()
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

	db := testStore(t, tmp)
	defer db.Close()
	ctx := context.Background()

	out, stats, err := RepoMap(ctx, db, WithBudget(30))
	if err != nil {
		t.Fatal(err)
	}
	if !stats.Truncated {
		t.Error("expected truncated=true with small budget")
	}
	if stats.ShownFiles >= stats.TotalFiles {
		t.Errorf("expected shown_files (%d) < total_files (%d)", stats.ShownFiles, stats.TotalFiles)
	}
	if stats.TotalFiles != 20 {
		t.Errorf("expected 20 total files, got %d", stats.TotalFiles)
	}
	if stats.TotalSymbols != 100 {
		t.Errorf("expected 100 total symbols, got %d", stats.TotalSymbols)
	}
	if strings.Contains(out, "File19") {
		t.Error("expected budget to stop before the last file")
	}

	_, stats2, err := RepoMap(ctx, db, WithBudget(100000))
	if err != nil {
		t.Fatal(err)
	}
	if stats2.Truncated {
		t.Error("expected large budget to not truncate")
	}
}

func TestRepoMapGrepLikeWildcards(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "main.go"), []byte("package main\n\nfunc Get_Item() {}\nfunc GetBigItem() {}\nfunc Percent100() {}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	db := testStore(t, tmp)
	defer db.Close()
	ctx := context.Background()

	out, _, err := RepoMap(ctx, db, WithGrep("_"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Get_Item") {
		t.Error("grep '_' should match 'Get_Item'")
	}
	if strings.Contains(out, "GetBigItem") {
		t.Error("grep '_' should NOT match 'GetBigItem'")
	}

	out2, _, err := RepoMap(ctx, db, WithGrep("%"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out2, "Percent100") {
		t.Error("grep '%%' should NOT match 'Percent100'")
	}
}
