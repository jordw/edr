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

// TestRefsTo_GoSamePackageCrossFile verifies that a ref in a sibling
// same-package .go file resolves to the target — previously marked as
// BindUnresolved/"missing_import" it now surfaces as a cross-file hit.
func TestRefsTo_GoSamePackageCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.go": `package foo

func Compute(x int) int { return x * 2 }
`,
		"b.go": `package foo

func Caller() int { return Compute(5) }
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.go:Compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	count, _ := m["count"].(int)
	if count != 1 {
		t.Errorf("expected 1 cross-file ref to Compute, got count=%d", count)
	}
	var gotB bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "b.go") {
			gotB = true
			if r["reason"] != "cross_file_same_package" {
				t.Errorf("b.go ref reason = %v, want cross_file_same_package", r["reason"])
			}
		}
	}
	if !gotB {
		t.Errorf("expected b.go ref in results; got %+v", refs)
	}
}

// TestRefsTo_GoSamePackageShadowIgnored verifies the shadow guard:
// a local `Compute := ...` inside a caller function must NOT be
// treated as a ref to the target top-level Compute.
func TestRefsTo_GoSamePackageShadowIgnored(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.go": `package foo

func Compute(x int) int { return x * 2 }
`,
		"b.go": `package foo

func Caller() int { return Compute(5) }

func Shadowed() int {
	Compute := 42
	_ = Compute
	return 0
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.go:Compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	for _, r := range refs {
		// Only lines 3 (the unshadowed call in Caller) should appear.
		// Lines 6-7 (the shadowed local and its use) must NOT.
		if ln, ok := r["line"].(int); ok {
			if ln == 6 || ln == 7 {
				t.Errorf("shadowed ref at b.go:%d should NOT be surfaced: %+v", ln, r)
			}
		}
	}
	// And Caller's call on b.go:3 should still be present.
	var gotCall bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "b.go") {
			if ln, _ := r["line"].(int); ln == 3 {
				gotCall = true
			}
		}
	}
	if !gotCall {
		t.Errorf("expected unshadowed call at b.go:3 in results; got %+v", refs)
	}
}

// TestRefsTo_GoSamePackageDifferentPackageDir guards against refs in
// a sibling file with a DIFFERENT `package X` clause being
// mis-attributed as cross-file same-package. (This is an invalid Go
// build but common during a half-finished refactor.)
func TestRefsTo_GoSamePackageDifferentPackageDir(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.go": `package foo

func Compute(x int) int { return x * 2 }
`,
		"b.go": `package bar

func Caller() int { return Compute(5) }
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.go:Compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	for _, r := range refs {
		if r["reason"] == "cross_file_same_package" {
			t.Errorf("b.go (package bar) must not be tagged cross_file_same_package: %+v", r)
		}
	}
}

// TestRename_GoSamePackageCrossFile verifies rename rewrites the call
// site in a sibling same-package file.
func TestRename_GoSamePackageCrossFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.go": `package foo

func Compute(x int) int { return x * 2 }
`,
		"b.go": `package foo

func Caller() int { return Compute(5) }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.go:Compute"},
		map[string]any{"new_name": "Calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	bData, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	if !strings.Contains(string(bData), "Calculate(5)") {
		t.Errorf("b.go missing Calculate(5); got:\n%s", bData)
	}
	if strings.Contains(string(bData), "Compute") {
		t.Errorf("b.go still contains Compute; got:\n%s", bData)
	}
}

// TestRename_GoSamePackageShadowNotRewritten verifies rename does NOT
// rewrite a shadowed local `Compute := 42` in the same caller file.
func TestRename_GoSamePackageShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.go": `package foo

func Compute(x int) int { return x * 2 }
`,
		"b.go": `package foo

func Caller() int { return Compute(5) }

func Shadowed() int {
	Compute := 42
	_ = Compute
	return 0
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.go:Compute"},
		map[string]any{"new_name": "Calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	bData, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	body := string(bData)
	if !strings.Contains(body, "Calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "Compute := 42") {
		t.Errorf("shadowed local MUST remain as `Compute := 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "_ = Compute") {
		t.Errorf("use of shadowed local MUST remain as `_ = Compute`; got:\n%s", body)
	}
}

// TestChangeSig_GoSamePackageCrossFile verifies changesig --cross-file
// rewrites a sibling same-package caller's call args.
func TestChangeSig_GoSamePackageCrossFile(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.go": `package foo

func Compute(x int) int { return x * 2 }
`,
		"b.go": `package foo

func Caller() int { return Compute(5) }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "changesig",
		[]string{"a.go:Compute"},
		map[string]any{"add": "extra int", "callarg": "0", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	bData, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	body := string(bData)
	if !strings.Contains(body, "Compute(5, 0)") {
		t.Errorf("cross-file call not rewritten with new arg; got:\n%s", body)
	}
}

// TestChangeSig_GoSamePackageShadowSafe verifies changesig does NOT
// touch a shadowed local in a caller file — even when the shadow and
// the real call are in the same file.
func TestChangeSig_GoSamePackageShadowSafe(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.go": `package foo

func Compute(x int) int { return x * 2 }
`,
		"b.go": `package foo

func Caller() int { return Compute(5) }

func Shadowed() int {
	Compute := 42
	_ = Compute
	return 0
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "changesig",
		[]string{"a.go:Compute"},
		map[string]any{"add": "extra int", "callarg": "0", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	bData, _ := os.ReadFile(filepath.Join(dir, "b.go"))
	body := string(bData)
	if !strings.Contains(body, "Compute(5, 0)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "Compute := 42") {
		t.Errorf("shadowed local MUST remain as `Compute := 42`; got:\n%s", body)
	}
}
