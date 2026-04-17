package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func setupRefsRepo(t *testing.T, files map[string]string) (index.SymbolStore, string) {
	t.Helper()
	tmp := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	t.Cleanup(func() { db.Close() })
	return db, tmp
}

func TestRefsTo_GoFunction(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"pkg.go": `package pkg

func helper(x int) int {
	return x * 2
}

func caller() int {
	a := helper(1)
	b := helper(2)
	return a + b
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"pkg.go:helper"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	count, _ := m["count"].(int)
	if count != 2 {
		t.Errorf("expected 2 refs to helper, got %d (%v)", count, m)
	}
}

func TestRefsTo_TSExportedFunction(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.ts": `export function compute(x: number): number {
  return x * 2
}
const a = compute(1)
const b = compute(2)
const c = compute(3)
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.ts:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	count, _ := m["count"].(int)
	if count != 3 {
		t.Errorf("expected 3 refs to compute, got %d", count)
	}
}

func TestRefsTo_SymbolNotFound(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.go": "package a\n\nfunc x() {}\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.go:NoSuchSymbol"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing symbol")
	}
	if !strings.Contains(err.Error(), "NoSuchSymbol") {
		t.Errorf("error should mention symbol name: %v", err)
	}
}

func TestRefsTo_BadArgument(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{"a.go": "package a\n"})
	_, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"not_file_colon_symbol"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for malformed argument")
	}
}

func TestRefsTo_UnsupportedLanguage(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"README.md": "# hello\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"README.md:foo"}, map[string]any{})
	if err == nil {
		t.Fatal("expected error for unsupported language")
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("error should mention unsupported language: %v", err)
	}
}

func TestRefsTo_BudgetTruncation(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.go": `package a

func h() {}

func f() {
	h()
	h()
	h()
	h()
	h()
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.go:h"}, map[string]any{"budget": 2})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	count, _ := m["count"].(int)
	if count != 2 {
		t.Errorf("budget=2 should truncate to 2 refs, got count=%d", count)
	}
	if trunc, _ := m["truncated"].(bool); !trunc {
		t.Errorf("expected truncated=true")
	}
}
