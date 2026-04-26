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
		"go.mod": "module example.com/foo\n",
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

// TestRename_FlagsUnrewrittenCodeMentions verifies that when the
// rename leaves word-bounded mentions of OldName in code (e.g. inside a
// string literal that the resolver correctly didn't pick up), the result
// reports code_mentions > code_occurrences and emits a warning. This
// catches the receiver-bug class: the resolver missing real refs.
func TestRename_FlagsUnrewrittenCodeMentions(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"go.mod": "module example.com/foo\n",
		"a.go": `package foo

func Foo(x int) int { return x * 2 }

func Use() int {
	s := "Foo bar" // string literal — not a ref; should NOT be rewritten
	_ = s
	return Foo(1)
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.go:Foo"},
		map[string]any{"new_name": "Bar"})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rr, ok := res.(*output.RenameResult)
	if !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	if rr.CodeOccurrences != 2 {
		t.Errorf("CodeOccurrences: want 2 (decl + call), got %d", rr.CodeOccurrences)
	}
	if rr.CodeMentions <= rr.CodeOccurrences {
		t.Errorf("CodeMentions (%d) should exceed CodeOccurrences (%d) due to string-literal Foo", rr.CodeMentions, rr.CodeOccurrences)
	}
	hasWarn := false
	for _, w := range rr.Warnings {
		if strings.Contains(w, "unrewritten") {
			hasWarn = true
			break
		}
	}
	if !hasWarn {
		t.Errorf("want warning mentioning 'unrewritten'; got %v", rr.Warnings)
	}
}

// TestRename_UpdateCommentsRewritesProseMentions verifies the
// --update-comments flag picks up word-bounded mentions of the renamed
// symbol in arbitrary comments (not just the leading doc), and that
// without the flag those mentions are preserved untouched.
func TestRename_UpdateCommentsRewritesProseMentions(t *testing.T) {
	src := `package uc

// Counter holds a count.
type Counter struct{ n int }

// Inc increments the Counter — methods on a Counter are documented here.
func (c *Counter) Inc() { c.n++ }

func use() {
	// build a Counter and increment it.
	c := &Counter{}
	c.Inc()
}
`
	t.Run("without_flag_leaves_inline_comments", func(t *testing.T) {
		db, dir := setupRefsRepo(t, map[string]string{
			"go.mod": "module example.com/uc\n",
			"a.go":   src,
		})
		_, err := dispatch.Dispatch(context.Background(), db, "rename",
			[]string{"a.go:Counter"},
			map[string]any{"new_name": "Tally"})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		body, _ := os.ReadFile(filepath.Join(dir, "a.go"))
		s := string(body)
		// Method-doc and inline mentions must remain "Counter" without the flag.
		if !strings.Contains(s, "methods on a Counter are documented") {
			t.Errorf("inline-doc mention should be preserved without flag; got:\n%s", s)
		}
		if !strings.Contains(s, "// build a Counter and increment it.") {
			t.Errorf("inline comment should be preserved without flag; got:\n%s", s)
		}
	})

	t.Run("with_flag_rewrites_prose", func(t *testing.T) {
		db, dir := setupRefsRepo(t, map[string]string{
			"go.mod": "module example.com/uc\n",
			"a.go":   src,
		})
		_, err := dispatch.Dispatch(context.Background(), db, "rename",
			[]string{"a.go:Counter"},
			map[string]any{"new_name": "Tally", "update_comments": true})
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		body, _ := os.ReadFile(filepath.Join(dir, "a.go"))
		s := string(body)
		if strings.Contains(s, "Counter") {
			t.Errorf("--update-comments should rewrite all word-bounded mentions; got:\n%s", s)
		}
		if !strings.Contains(s, "methods on a Tally are documented") {
			t.Errorf("inline-doc mention should be rewritten; got:\n%s", s)
		}
		if !strings.Contains(s, "// build a Tally and increment it.") {
			t.Errorf("inline comment should be rewritten; got:\n%s", s)
		}
	})
}

// TestRename_GoSamePackageShadowNotRewritten verifies rename does NOT
// rewrite a shadowed local `Compute := 42` in the same caller file.
func TestRename_GoSamePackageShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"go.mod": "module example.com/foo\n",
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

// TestRefsTo_JavaCrossFile: a class defined in lib.java is referenced
// via `new Helper()` in caller.java. The caller's ref is unresolved at
// the builder level (no import-path resolution), and cross-file walking
// surfaces it as cross_file_unresolved.
func TestRefsTo_JavaCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"lib.java": `public class Helper {
    public int compute(int x) { return x * 2; }
}
`,
		"caller.java": `public class Caller {
    public int run() { return new Helper().compute(5); }
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"lib.java:Helper"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "caller.java") {
			gotCaller = true
			if r["reason"] != "cross_file_unresolved" {
				t.Errorf("caller.java ref reason = %v, want cross_file_unresolved", r["reason"])
			}
		}
	}
	if !gotCaller {
		t.Errorf("expected caller.java ref; got %+v", refs)
	}
}

// TestRefsTo_RubyCrossFile: a top-level method defined in a.rb is called
// as a bare ident in b.rb. With the fixed BindingKind contract the caller
// ref is BindUnresolved reason="method_call"; cross-file walking surfaces
// it as cross_file_unresolved.
func TestRefsTo_RubyCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"a.rb": `def compute(x)
  x * 2
end
`,
		"b.rb": `def run
  compute(5)
end
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"a.rb:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotB bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "b.rb") {
			gotB = true
			if r["reason"] != "cross_file_unresolved" {
				t.Errorf("b.rb ref reason = %v, want cross_file_unresolved", r["reason"])
			}
		}
	}
	if !gotB {
		t.Errorf("expected b.rb ref; got %+v", refs)
	}
}

// TestRefsTo_RubyClassMethod: Rails-style. A class method defined in
// app/models/foo.rb is called as Foo.method(...) from app/controllers/.
// No `require_relative` (Rails autoloading). Class-scope decl must
// not block cross-file ref walking.
func TestRefsTo_RubyClassMethod(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"models/mentor_invitation.rb": `class MentorInvitation
  def self.invite(mentor)
    'token'
  end
end
`,
		"controllers/mentors_controller.rb": `class MentorsController
  def create
    MentorInvitation.invite(@mentor)
  end
end
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"models/mentor_invitation.rb:invite"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "mentors_controller.rb") {
			gotCaller = true
		}
	}
	if !gotCaller {
		t.Errorf("expected controller ref for class-method invite; got %+v", refs)
	}
}

// TestRefsTo_JavaStaticMethod: a static method on a class is called
// as Foo.method() from another file. The method decl lives in class
// scope, not file scope; cross-file ref walking must still run.
func TestRefsTo_JavaStaticMethod(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"Foo.java": `public class Foo {
    public static String invite() {
        return "token";
    }
}
`,
		"Caller.java": `public class Caller {
    public void run() {
        String t = Foo.invite();
    }
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"Foo.java:invite"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "Caller.java") {
			gotCaller = true
		}
	}
	if !gotCaller {
		t.Errorf("expected Caller.java ref for static method; got %+v", refs)
	}
}

// TestRefsTo_PythonClassMethod: a @classmethod defined inside a class
// is called as Cls.method() from another file. Class-scope decl must
// not block cross-file ref walking.
func TestRefsTo_PythonClassMethod(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"models.py": `class MentorInvitation:
    @classmethod
    def invite(cls, mentor):
        return 'token'
`,
		"app.py": `from models import MentorInvitation

def run():
    return MentorInvitation.invite('mentor')
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"models.py:invite"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "app.py") {
			gotCaller = true
		}
	}
	if !gotCaller {
		t.Errorf("expected app.py ref for classmethod; got %+v", refs)
	}
}

// TestRefsTo_RustCrossFile: a top-level fn defined in lib.rs is called
// as a bare ident in caller.rs (no `use` — Rust's builder won't resolve
// it). Cross-file walking surfaces the call as cross_file_unresolved.
func TestRefsTo_RustCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"lib.rs": `pub fn compute(x: i32) -> i32 { x * 2 }
`,
		"caller.rs": `pub fn run() -> i32 { compute(5) }
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"lib.rs:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "caller.rs") {
			gotCaller = true
			if r["reason"] != "cross_file_unresolved" {
				t.Errorf("caller.rs ref reason = %v, want cross_file_unresolved", r["reason"])
			}
		}
	}
	if !gotCaller {
		t.Errorf("expected caller.rs ref; got %+v", refs)
	}
}

// TestRefsTo_KotlinCrossFile: top-level fn in lib.kt is called from
// caller.kt as a bare ident. Cross-file walk surfaces the call.
func TestRefsTo_KotlinCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"lib.kt": `fun compute(x: Int): Int = x * 2
`,
		"caller.kt": `fun run(): Int = compute(5)
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"lib.kt:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "caller.kt") {
			gotCaller = true
			reason, _ := r["reason"].(string)
			if !strings.HasPrefix(reason, "cross_file_") {
				t.Errorf("caller.kt ref reason = %q, want cross_file_*", reason)
			}
		}
	}
	if !gotCaller {
		t.Errorf("expected caller.kt ref; got %+v", refs)
	}
}

// TestRefsTo_SwiftCrossFile: top-level fn in lib.swift is called from
// caller.swift as a bare ident.
func TestRefsTo_SwiftCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"lib.swift": `func compute(_ x: Int) -> Int {
    return x * 2
}
`,
		"caller.swift": `func run() -> Int {
    return compute(5)
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"lib.swift:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "caller.swift") {
			gotCaller = true
			reason, _ := r["reason"].(string)
			if !strings.HasPrefix(reason, "cross_file_") {
				t.Errorf("caller.swift ref reason = %q, want cross_file_*", reason)
			}
		}
	}
	if !gotCaller {
		t.Errorf("expected caller.swift ref; got %+v", refs)
	}
}

// TestRefsTo_PHPCrossFile: top-level function in lib.php is called
// from caller.php as a bare ident.
func TestRefsTo_PHPCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"lib.php": `<?php
function compute($x) { return $x * 2; }
`,
		"caller.php": `<?php
function run() { return compute(5); }
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"lib.php:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "caller.php") {
			gotCaller = true
			reason, _ := r["reason"].(string)
			if !strings.HasPrefix(reason, "cross_file_") {
				t.Errorf("caller.php ref reason = %q, want cross_file_*", reason)
			}
		}
	}
	if !gotCaller {
		t.Errorf("expected caller.php ref; got %+v", refs)
	}
}

// TestRefsTo_CCrossFile: top-level function in lib.c is called from
// caller.c with no #include — cross-file walk picks it up by name.
func TestRefsTo_CCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"lib.c": `int compute(int x) { return x * 2; }
`,
		"caller.c": `int run(void) { return compute(5); }
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"lib.c:compute"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "caller.c") {
			gotCaller = true
			reason, _ := r["reason"].(string)
			if !strings.HasPrefix(reason, "cross_file_") {
				t.Errorf("caller.c ref reason = %q, want cross_file_*", reason)
			}
		}
	}
	if !gotCaller {
		t.Errorf("expected caller.c ref; got %+v", refs)
	}
}

// TestRefsTo_CSharpCrossFile: class defined in lib.cs is instantiated
// in caller.cs — cross-file walk surfaces the `new Helper()` site.
func TestRefsTo_CSharpCrossFile(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"lib.cs": `public class Helper {
    public int Compute(int x) { return x * 2; }
}
`,
		"caller.cs": `public class Caller {
    public int Run() { return new Helper().Compute(5); }
}
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "refs-to",
		[]string{"lib.cs:Helper"}, map[string]any{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := res.(map[string]any)
	refs, _ := m["refs"].([]map[string]any)
	var gotCaller bool
	for _, r := range refs {
		file, _ := r["file"].(string)
		if strings.HasSuffix(file, "caller.cs") {
			gotCaller = true
			reason, _ := r["reason"].(string)
			if !strings.HasPrefix(reason, "cross_file_") {
				t.Errorf("caller.cs ref reason = %q, want cross_file_*", reason)
			}
		}
	}
	if !gotCaller {
		t.Errorf("expected caller.cs ref; got %+v", refs)
	}
}

// TestRename_JavaShadowNotRewritten verifies that renaming a public
// method across package-level Java files does not touch a local
// variable with the same name in a sibling file. Same safety contract
// as TestRename_GoSamePackageShadowNotRewritten.
func TestRename_JavaShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Lib.java": `package foo;

public class Lib {
    public static int compute(int x) { return x * 2; }
}
`,
		"Caller.java": `package foo;

public class Caller {
    public int callIt() { return Lib.compute(5); }

    public int shadowed() {
        int compute = 42;
        return compute + 1;
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Lib.java:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	cData, _ := os.ReadFile(filepath.Join(dir, "Caller.java"))
	body := string(cData)
	if !strings.Contains(body, "Lib.calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "int compute = 42") {
		t.Errorf("shadowed local MUST remain as `int compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "return compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `compute + 1`; got:\n%s", body)
	}
}

// TestChangeSig_JavaShadowSafe: same guarantee as TestChangeSig_
// GoSamePackageShadowSafe — changesig must not rewrite call-site-like
// tokens that bind to a shadowed local, only genuine call sites of
// the target method.
// TestRename_KotlinShadowNotRewritten: cross-file Kotlin rename must
// rewrite the class-qualified call and leave same-named locals alone.
// Same safety contract as TestRename_JavaShadowNotRewritten.
func TestRename_KotlinShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Lib.kt": `package foo

object Lib {
    fun compute(x: Int): Int = x * 2
}
`,
		"Caller.kt": `package foo

class Caller {
    fun callIt(): Int = Lib.compute(5)

    fun shadowed(): Int {
        val compute = 42
        return compute + 1
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Lib.kt:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	cData, _ := os.ReadFile(filepath.Join(dir, "Caller.kt"))
	body := string(cData)
	if !strings.Contains(body, "Lib.calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "val compute = 42") {
		t.Errorf("shadowed local MUST remain as `val compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "return compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `compute + 1`; got:\n%s", body)
	}
}

// TestRename_RustShadowNotRewritten: cross-file rename rewrites the
// module-qualified call and preserves a same-named local.
func TestRename_RustShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"lib.rs": `pub fn compute(x: i32) -> i32 { x * 2 }
`,
		"main.rs": `mod lib;

fn run() -> i32 { lib::compute(5) }

fn shadowed() -> i32 {
    let compute = 42;
    compute + 1
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"lib.rs:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	mData, _ := os.ReadFile(filepath.Join(dir, "main.rs"))
	body := string(mData)
	if !strings.Contains(body, "lib::calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "let compute = 42") {
		t.Errorf("shadowed local MUST remain as `let compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `compute + 1`; got:\n%s", body)
	}
}

// TestRename_CShadowNotRewritten: cross-file C rename rewrites the
// call in a sibling .c file AND the declaration in the matching .h
// file, but leaves a same-named local variable alone.
func TestRename_CShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"foo.h": `int compute(int x);
`,
		"foo.c": `#include "foo.h"

int compute(int x) { return x * 2; }
`,
		"caller.c": `#include "foo.h"

int run() { return compute(5); }

int shadowed() {
    int compute = 42;
    return compute + 1;
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"foo.c:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hData, _ := os.ReadFile(filepath.Join(dir, "foo.h"))
	if !strings.Contains(string(hData), "int calculate(int x);") {
		t.Errorf("header declaration not rewritten; got:\n%s", hData)
	}
	cData, _ := os.ReadFile(filepath.Join(dir, "caller.c"))
	body := string(cData)
	if !strings.Contains(body, "return compute(5)") && !strings.Contains(body, "return calculate(5)") {
		t.Errorf("caller.c unexpected state; got:\n%s", body)
	}
	if !strings.Contains(body, "return calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "int compute = 42") {
		t.Errorf("shadowed local MUST remain as `int compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "return compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `compute + 1`; got:\n%s", body)
	}
}

// TestRename_CppShadowNotRewritten: cross-file C++ rename rewrites the
// call in main.cpp AND the prototype in the matching .hpp file, but
// leaves a same-named local variable alone. Mirrors the C version;
// gates the C++ promotion in scopeSupported.
func TestRename_CppShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"foo.hpp": `#ifndef FOO_HPP
#define FOO_HPP
int compute(int x);
#endif
`,
		"foo.cpp": `#include "foo.hpp"

int compute(int x) { return x * 2; }
`,
		"main.cpp": `#include "foo.hpp"

int run() { return compute(5); }

int shadowed() {
    int compute = 42;
    return compute + 1;
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"foo.cpp:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hData, _ := os.ReadFile(filepath.Join(dir, "foo.hpp"))
	if !strings.Contains(string(hData), "int calculate(int x);") {
		t.Errorf("header prototype not rewritten; got:\n%s", hData)
	}
	mData, _ := os.ReadFile(filepath.Join(dir, "main.cpp"))
	body := string(mData)
	if !strings.Contains(body, "return calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "int compute = 42") {
		t.Errorf("shadowed local MUST remain as `int compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "return compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `compute + 1`; got:\n%s", body)
	}
}

// TestRename_RubyShadowNotRewritten: cross-file Ruby rename rewrites
// the call in app.rb but leaves a same-named local variable alone.
// The ruby scope builder emits `var` decls for `name = expr` blocks
// and binds same-name refs to the local; this test guards the shadow
// contract and gates the Ruby promotion in scopeSupported.
func TestRename_RubyShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"lib.rb": `def compute(x)
  x * 2
end
`,
		"app.rb": `require_relative "./lib"

def run
  compute(5)
end

def shadowed
  compute = 42
  compute + 1
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"lib.rb:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	appData, _ := os.ReadFile(filepath.Join(dir, "app.rb"))
	body := string(appData)
	if !strings.Contains(body, "calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "compute = 42") {
		t.Errorf("shadowed local MUST remain as `compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `compute + 1`; got:\n%s", body)
	}
}

// TestRename_CSharpShadowNotRewritten: cross-file C# rename rewrites
// the static method def + Lib.Compute() call site but leaves a same-
// named local variable alone. The csharp scope builder emits `var`
// decls for local declarations and binds same-name refs to the local.
func TestRename_CSharpShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Lib.cs": `namespace App;

public static class Lib {
    public static int Compute(int x) { return x * 2; }
}
`,
		"Caller.cs": `namespace App;

public static class Caller {
    public static int Run() { return Lib.Compute(5); }
    public static int Shadowed() {
        int Compute = 42;
        return Compute + 1;
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Lib.cs:Compute"},
		map[string]any{"new_name": "Calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	cData, _ := os.ReadFile(filepath.Join(dir, "Caller.cs"))
	body := string(cData)
	if !strings.Contains(body, "Lib.Calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "int Compute = 42") {
		t.Errorf("shadowed local MUST remain as `int Compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "return Compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `Compute + 1`; got:\n%s", body)
	}
}

// TestRename_SwiftShadowNotRewritten: cross-file Swift rename rewrites
// the free function def + cross-file call site but leaves a same-
// named `let` local alone. The swift scope builder emits `let`/`var`
// decls for local bindings and binds same-name refs to the local.
func TestRename_SwiftShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"Lib.swift": `func compute(_ x: Int) -> Int { return x * 2 }
`,
		"App.swift": `func run() -> Int { return compute(5) }

func shadowed() -> Int {
    let compute = 42
    return compute + 1
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"Lib.swift:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	aData, _ := os.ReadFile(filepath.Join(dir, "App.swift"))
	body := string(aData)
	if !strings.Contains(body, "return calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "let compute = 42") {
		t.Errorf("shadowed local MUST remain as `let compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "return compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `compute + 1`; got:\n%s", body)
	}
}

// TestRename_PHPShadowNotRewritten: cross-file PHP rename rewrites the
// free function def + cross-file call site but leaves a same-named
// local `$compute` alone. The php scope builder distinguishes function
// refs (`compute`) from variable refs (`$compute`) by the leading `$`,
// so binding correctness is structurally easy; this test guards
// against regex-fallback over-rewrite of `$compute` (where `\bcompute\b`
// matches inside the variable token).
func TestRename_PHPShadowNotRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"lib.php": `<?php

function compute($x) {
    return $x * 2;
}
`,
		"app.php": `<?php

require_once "lib.php";

function run() {
    return compute(5);
}

function shadowed() {
    $compute = 42;
    return $compute + 1;
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"lib.php:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	aData, _ := os.ReadFile(filepath.Join(dir, "app.php"))
	body := string(aData)
	if !strings.Contains(body, "return calculate(5)") {
		t.Errorf("cross-file call not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "$compute = 42") {
		t.Errorf("shadowed local MUST remain as `$compute = 42`; got:\n%s", body)
	}
	if !strings.Contains(body, "return $compute + 1") {
		t.Errorf("use of shadowed local MUST remain as `$compute + 1`; got:\n%s", body)
	}
}

// ----------------------------------------------------------------------
// Cross-class same-name FP guards
//
// Each test sets up two unrelated types/classes that both happen to
// have a method named `compute`. Renaming one's method must NOT touch
// the other — the most common cross-file false positive shape that
// regex fallback would otherwise rewrite both of.
// ----------------------------------------------------------------------

func TestRename_GoCrossTypeFP(t *testing.T) {
	// FP guard: A.compute and B.compute are unrelated methods; renaming
	// A.compute MUST rewrite a.compute() call sites and MUST NOT touch
	// B.compute or its call sites.
	db, dir := setupRefsRepo(t, map[string]string{
		"go.mod":  "module test\ngo 1.21\n",
		"a.go":    "package main\n\ntype A struct{}\n\nfunc (a A) compute() int { return 1 }\n",
		"b.go":    "package main\n\ntype B struct{}\n\nfunc (b B) compute() int { return 2 }\n",
		"main.go": "package main\n\nfunc main() {\n\ta := A{}\n\tb := B{}\n\t_ = a.compute()\n\t_ = b.compute()\n}\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.go:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "a.go")), "func (a A) calculate()") {
		t.Errorf("A.compute should be renamed; got:\n%s", mustRead(t, dir, "a.go"))
	}
	if !strings.Contains(string(mustRead(t, dir, "b.go")), "func (b B) compute()") {
		t.Errorf("B.compute MUST remain; got:\n%s", mustRead(t, dir, "b.go"))
	}
	body := string(mustRead(t, dir, "main.go"))
	if !strings.Contains(body, "a.calculate()") {
		t.Errorf("a.compute() call should be renamed to a.calculate(); got:\n%s", body)
	}
	if !strings.Contains(body, "b.compute()") {
		t.Errorf("b.compute() call MUST remain; got:\n%s", body)
	}
}

func TestRename_TSCrossClassFP(t *testing.T) {
	// FP guard: B.compute MUST NOT be touched.
	// Like the Go test, call-site rewriting for `a.compute()` in a
	// sibling file is a known under-rewrite gap — varTypes inference
	// doesn't thread through. The cross-class FP contract is the
	// safety-critical assertion.
	db, dir := setupRefsRepo(t, map[string]string{
		"a.ts":    "export class A { compute(): number { return 1; } }\n",
		"b.ts":    "export class B { compute(): number { return 2; } }\n",
		"main.ts": "import { A } from './a';\nimport { B } from './b';\nconst a = new A();\nconst b = new B();\nconsole.log(a.compute(), b.compute());\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.ts:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "a.ts")), "calculate()") {
		t.Errorf("A.compute should be renamed; got:\n%s", mustRead(t, dir, "a.ts"))
	}
	if !strings.Contains(string(mustRead(t, dir, "b.ts")), "compute()") {
		t.Errorf("B.compute MUST remain; got:\n%s", mustRead(t, dir, "b.ts"))
	}
	body := string(mustRead(t, dir, "main.ts"))
	if !strings.Contains(body, "b.compute()") {
		t.Errorf("b.compute() call MUST remain; got:\n%s", body)
	}
}

func TestRename_PythonCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.py":    "class A:\n    def compute(self):\n        return 1\n",
		"b.py":    "class B:\n    def compute(self):\n        return 2\n",
		"main.py": "from a import A\nfrom b import B\na = A()\nb = B()\nprint(a.compute(), b.compute())\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.py:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "a.py")), "def calculate(self)") {
		t.Errorf("A.compute should be renamed; got:\n%s", mustRead(t, dir, "a.py"))
	}
	if !strings.Contains(string(mustRead(t, dir, "b.py")), "def compute(self)") {
		t.Errorf("B.compute MUST remain; got:\n%s", mustRead(t, dir, "b.py"))
	}
	body := string(mustRead(t, dir, "main.py"))
	if !strings.Contains(body, "a.calculate()") || !strings.Contains(body, "b.compute()") {
		t.Errorf("expected a.calculate() + b.compute() preserved; got:\n%s", body)
	}
}

func TestRename_JavaCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.java":    "package app;\npublic class A { public int compute() { return 1; } }\n",
		"B.java":    "package app;\npublic class B { public int compute() { return 2; } }\n",
		"Main.java": "package app;\npublic class Main {\n    public static void main(String[] args) {\n        A a = new A();\n        B b = new B();\n        System.out.println(a.compute() + b.compute());\n    }\n}\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"A.java:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "A.java")), "int calculate()") {
		t.Errorf("A.compute should be renamed; got:\n%s", mustRead(t, dir, "A.java"))
	}
	if !strings.Contains(string(mustRead(t, dir, "B.java")), "int compute()") {
		t.Errorf("B.compute MUST remain; got:\n%s", mustRead(t, dir, "B.java"))
	}
	body := string(mustRead(t, dir, "Main.java"))
	if !strings.Contains(body, "a.calculate()") || !strings.Contains(body, "b.compute()") {
		t.Errorf("expected a.calculate() + b.compute() preserved; got:\n%s", body)
	}
}

func TestRename_KotlinCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.kt":    "package app\n\nclass A { fun compute(): Int = 1 }\n",
		"B.kt":    "package app\n\nclass B { fun compute(): Int = 2 }\n",
		"Main.kt": "package app\n\nfun main() {\n    val a = A()\n    val b = B()\n    println(a.compute() + b.compute())\n}\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"A.kt:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "A.kt")), "fun calculate()") {
		t.Errorf("A.compute should be renamed; got:\n%s", mustRead(t, dir, "A.kt"))
	}
	if !strings.Contains(string(mustRead(t, dir, "B.kt")), "fun compute()") {
		t.Errorf("B.compute MUST remain; got:\n%s", mustRead(t, dir, "B.kt"))
	}
	body := string(mustRead(t, dir, "Main.kt"))
	if !strings.Contains(body, "a.calculate()") || !strings.Contains(body, "b.compute()") {
		t.Errorf("expected a.calculate() + b.compute() preserved; got:\n%s", body)
	}
}

func TestRename_RustCrossImplFP(t *testing.T) {
	// Cargo.toml is required for the Rust namespace path to engage —
	// without it the safety valve fires and the rename falls back to
	// regex (mode:name-match) which over-rewrites both impls. With a
	// crate root present, the canonical-DeclID path keeps them apart.
	db, dir := setupRefsRepo(t, map[string]string{
		"Cargo.toml": "[package]\nname = \"test\"\nversion = \"0.1.0\"\nedition = \"2021\"\n\n[lib]\npath = \"src/lib.rs\"\n",
		"src/lib.rs": "mod a;\nmod b;\n",
		"src/a.rs":   "pub struct A;\nimpl A { pub fn compute(&self) -> i32 { 1 } }\n",
		"src/b.rs":   "pub struct B;\nimpl B { pub fn compute(&self) -> i32 { 2 } }\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"src/a.rs:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "src/a.rs")), "fn calculate(&self)") {
		t.Errorf("A.compute should be renamed; got:\n%s", mustRead(t, dir, "src/a.rs"))
	}
	if !strings.Contains(string(mustRead(t, dir, "src/b.rs")), "fn compute(&self)") {
		t.Errorf("B.compute MUST remain; got:\n%s", mustRead(t, dir, "src/b.rs"))
	}
}

func TestRename_CppCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.hpp": "#ifndef A_HPP\n#define A_HPP\nclass A { public: int compute() const; };\n#endif\n",
		"a.cpp": "#include \"a.hpp\"\nint A::compute() const { return 1; }\n",
		"b.hpp": "#ifndef B_HPP\n#define B_HPP\nclass B { public: int compute() const; };\n#endif\n",
		"b.cpp": "#include \"b.hpp\"\nint B::compute() const { return 2; }\n",
		"main.cpp": "#include \"a.hpp\"\n#include \"b.hpp\"\nint main() {\n    A a; B b;\n    return a.compute() + b.compute();\n}\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.hpp:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "a.hpp")), "int calculate()") {
		t.Errorf("A.compute decl should be renamed; got:\n%s", mustRead(t, dir, "a.hpp"))
	}
	if !strings.Contains(string(mustRead(t, dir, "a.cpp")), "A::calculate()") {
		t.Errorf("A.compute def should be renamed; got:\n%s", mustRead(t, dir, "a.cpp"))
	}
	if !strings.Contains(string(mustRead(t, dir, "b.hpp")), "int compute()") {
		t.Errorf("B.compute decl MUST remain; got:\n%s", mustRead(t, dir, "b.hpp"))
	}
	if !strings.Contains(string(mustRead(t, dir, "b.cpp")), "B::compute()") {
		t.Errorf("B.compute def MUST remain; got:\n%s", mustRead(t, dir, "b.cpp"))
	}
	body := string(mustRead(t, dir, "main.cpp"))
	if !strings.Contains(body, "a.calculate()") || !strings.Contains(body, "b.compute()") {
		t.Errorf("expected a.calculate() + b.compute() preserved; got:\n%s", body)
	}
}

func TestRename_RubyCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"a.rb":   "class A\n  def compute\n    1\n  end\nend\n",
		"b.rb":   "class B\n  def compute\n    2\n  end\nend\n",
		"app.rb": "require_relative 'a'\nrequire_relative 'b'\na = A.new\nb = B.new\nputs a.compute + b.compute\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.rb:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "a.rb")), "def calculate") {
		t.Errorf("A#compute should be renamed; got:\n%s", mustRead(t, dir, "a.rb"))
	}
	if !strings.Contains(string(mustRead(t, dir, "b.rb")), "def compute") {
		t.Errorf("B#compute MUST remain; got:\n%s", mustRead(t, dir, "b.rb"))
	}
	body := string(mustRead(t, dir, "app.rb"))
	if !strings.Contains(body, "a.calculate") || !strings.Contains(body, "b.compute") {
		t.Errorf("expected a.calculate + b.compute preserved; got:\n%s", body)
	}
}

func TestRename_CSharpCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.cs":    "namespace App;\npublic class A { public int Compute() { return 1; } }\n",
		"B.cs":    "namespace App;\npublic class B { public int Compute() { return 2; } }\n",
		"Main.cs": "namespace App;\npublic static class Main {\n    public static int Run() {\n        A a = new A();\n        B b = new B();\n        return a.Compute() + b.Compute();\n    }\n}\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"A.cs:Compute"},
		map[string]any{"new_name": "Calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "A.cs")), "int Calculate()") {
		t.Errorf("A.Compute should be renamed; got:\n%s", mustRead(t, dir, "A.cs"))
	}
	if !strings.Contains(string(mustRead(t, dir, "B.cs")), "int Compute()") {
		t.Errorf("B.Compute MUST remain; got:\n%s", mustRead(t, dir, "B.cs"))
	}
	body := string(mustRead(t, dir, "Main.cs"))
	if !strings.Contains(body, "a.Calculate()") || !strings.Contains(body, "b.Compute()") {
		t.Errorf("expected a.Calculate() + b.Compute() preserved; got:\n%s", body)
	}
}

func TestRename_SwiftCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.swift":    "public class A { public func compute() -> Int { return 1 } }\n",
		"B.swift":    "public class B { public func compute() -> Int { return 2 } }\n",
		"Main.swift": "public func run() -> Int {\n    let a = A()\n    let b = B()\n    return a.compute() + b.compute()\n}\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"A.swift:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "A.swift")), "func calculate()") {
		t.Errorf("A.compute should be renamed; got:\n%s", mustRead(t, dir, "A.swift"))
	}
	if !strings.Contains(string(mustRead(t, dir, "B.swift")), "func compute()") {
		t.Errorf("B.compute MUST remain; got:\n%s", mustRead(t, dir, "B.swift"))
	}
	body := string(mustRead(t, dir, "Main.swift"))
	if !strings.Contains(body, "a.calculate()") || !strings.Contains(body, "b.compute()") {
		t.Errorf("expected a.calculate() + b.compute() preserved; got:\n%s", body)
	}
}

func TestRename_PHPCrossClassFP(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"A.php":   "<?php\nclass A { public function compute() { return 1; } }\n",
		"B.php":   "<?php\nclass B { public function compute() { return 2; } }\n",
		"app.php": "<?php\nrequire_once 'A.php';\nrequire_once 'B.php';\n$a = new A();\n$b = new B();\necho $a->compute() + $b->compute();\n",
	})
	if _, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"A.php:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true}); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !strings.Contains(string(mustRead(t, dir, "A.php")), "function calculate()") {
		t.Errorf("A::compute should be renamed; got:\n%s", mustRead(t, dir, "A.php"))
	}
	if !strings.Contains(string(mustRead(t, dir, "B.php")), "function compute()") {
		t.Errorf("B::compute MUST remain; got:\n%s", mustRead(t, dir, "B.php"))
	}
	body := string(mustRead(t, dir, "app.php"))
	if !strings.Contains(body, "$a->calculate()") || !strings.Contains(body, "$b->compute()") {
		t.Errorf("expected $a->calculate() + $b->compute() preserved; got:\n%s", body)
	}
}

func mustRead(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return data
}

// ----------------------------------------------------------------------
// Kotlin oracle-equivalent tests
//
// kotlinc is not available in our dev environment. These tests parallel
// the Java oracle cases in scripts/eval/rename_correctness.sh and assert
// the expected byte patterns after each rename — same accuracy coverage
// minus the compile-pass signal.
//
// The three cases mirror the Java fixture:
//   1. Static method call via object qualifier (Lib.compute)
//   2. Instance method call via local variable (lib.process)
//   3. Interface implementation rename (ServiceImpl.run)
// ----------------------------------------------------------------------

// TestRename_KotlinStaticMethodOracle — object-qualified call; the
// namespace-driven disambiguation via canonical class DeclID should
// rewrite Lib.compute across package-aware imports.
func TestRename_KotlinStaticMethodOracle(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/lib/Lib.kt": `package com.example.lib

object Lib {
    fun compute(x: Int): Int = x * 2
}
`,
		"com/example/other/Other.kt": `package com.example.other

object Other {
    fun compute(s: String): String = s + s
}
`,
		"com/example/caller/Caller.kt": `package com.example.caller

import com.example.lib.Lib
import com.example.other.Other

class Caller {
    fun useStatic(): Int = Lib.compute(5) + Other.compute("x").length
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/lib/Lib.kt:compute"},
		map[string]any{"new_name": "compute2", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "com/example/caller/Caller.kt"))
	body := string(got)
	if !strings.Contains(body, "Lib.compute2(5)") {
		t.Errorf("Lib.compute should become Lib.compute2; got:\n%s", body)
	}
	if strings.Contains(body, "Other.compute2") {
		t.Errorf("Other.compute must NOT be rewritten; got:\n%s", body)
	}
}

// TestRename_KotlinInstanceMethodOracle — instance method called via
// a locally-typed variable (`val lib: Lib = ...; lib.process(...)`).
// Without receiver-type hints this would FAIL; with them the var's
// type annotation resolves through the namespace to our target class.
//
// Kotlin's annotation syntax is `val x: Type` — the type comes AFTER
// the name separated by `:`. Our buildVarTypes currently pairs types
// BEFORE the name. This test documents that gap: it will FAIL until
// we add Kotlin-style trailing-type pairing. When it starts passing,
// the Kotlin equivalent of Java's lib-instance-method oracle is green.
func TestRename_KotlinInstanceMethodOracle(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/lib/Lib.kt": `package com.example.lib

class Lib {
    fun process(s: String): Int = s.length
}
`,
		"com/example/caller/Caller.kt": `package com.example.caller

import com.example.lib.Lib

class Caller {
    fun useInstance(): Int {
        val lib: Lib = Lib()
        return lib.process("hello")
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/lib/Lib.kt:process"},
		map[string]any{"new_name": "process2", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "com/example/caller/Caller.kt"))
	body := string(got)
	if !strings.Contains(body, "lib.process2(") {
		t.Errorf("lib.process should become lib.process2; got:\n%s", body)
	}
}

// TestRename_JavaVarConstructorInfer — Java 10+ `var lib = new Lib()`
// has no explicit type annotation; type must be inferred from the
// `new Lib()` RHS. Exercises findTypeFromConstructorInit.
func TestRename_JavaVarConstructorInfer(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/lib/Lib.java": `package com.example.lib;

public class Lib {
    public int process(String s) { return s.length(); }
}
`,
		"com/example/caller/Caller.java": `package com.example.caller;

import com.example.lib.Lib;

public class Caller {
    public int useInstance() {
        var lib = new Lib();
        return lib.process("hello");
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/lib/Lib.java:process"},
		map[string]any{"new_name": "process2", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "com/example/caller/Caller.java"))
	body := string(got)
	if !strings.Contains(body, "lib.process2(") {
		t.Errorf("var-inferred lib.process should become lib.process2; got:\n%s", body)
	}
}

// TestRename_KotlinValConstructorInfer — Kotlin `val lib = Lib()` has
// no explicit type annotation; type must be inferred from the `Lib()`
// constructor call. Same mechanism as Java var, no `new` keyword.
func TestRename_KotlinValConstructorInfer(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/lib/Lib.kt": `package com.example.lib

class Lib {
    fun process(s: String): Int = s.length
}
`,
		"com/example/caller/Caller.kt": `package com.example.caller

import com.example.lib.Lib

class Caller {
    fun useInstance(): Int {
        val lib = Lib()
        return lib.process("hello")
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/lib/Lib.kt:process"},
		map[string]any{"new_name": "process2", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "com/example/caller/Caller.kt"))
	body := string(got)
	if !strings.Contains(body, "lib.process2(") {
		t.Errorf("val-inferred lib.process should become lib.process2; got:\n%s", body)
	}
}

// TestRename_KotlinInterfaceImplOracle — renaming ServiceImpl.run
// should ALSO rename Service.run on the interface, but we do not
// (hierarchy index is a separate phase). This test documents the
// current behavior: ServiceImpl.run IS rewritten, Service.run is NOT.
// When the hierarchy index lands, update the assertion.
func TestRename_KotlinInterfaceImplOracle(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"com/example/iface/Service.kt": `package com.example.iface

interface Service {
    fun run(input: String): String
}
`,
		"com/example/iface/ServiceImpl.kt": `package com.example.iface

class ServiceImpl : Service {
    override fun run(input: String): String = input.uppercase()
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"com/example/iface/ServiceImpl.kt:run"},
		map[string]any{"new_name": "runImpl", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	impl, _ := os.ReadFile(filepath.Join(dir, "com/example/iface/ServiceImpl.kt"))
	svc, _ := os.ReadFile(filepath.Join(dir, "com/example/iface/Service.kt"))
	if !strings.Contains(string(impl), "fun runImpl(") {
		t.Errorf("ServiceImpl.run should have been rewritten; got:\n%s", impl)
	}
	// Document the current limitation: interface decl is NOT updated.
	// When Phase 8 ships, flip this to Contains("runImpl") and drop the
	// NotContains check.
	if strings.Contains(string(svc), "fun runImpl(") {
		t.Errorf("Phase 8 not yet landed — Service.run should still read `fun run`; got:\n%s", svc)
	}
}


func TestRename_RustUseImportRewritten(t *testing.T) {
	// With a Cargo.toml present, the namespace path should resolve
	// `use crate::runtime::Handle` and rewrite both the definition
	// in runtime.rs and the import + call sites in task.rs.
	db, dir := setupRefsRepo(t, map[string]string{
		"Cargo.toml": "[package]\nname = \"demo\"\n",
		"src/lib.rs": "",
		"src/runtime.rs": `pub fn spawn() {}
`,
		"src/task.rs": `use crate::runtime::spawn;

pub fn run() { spawn(); }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"src/runtime.rs:spawn"},
		map[string]any{"new_name": "launch", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	runtimeData, _ := os.ReadFile(filepath.Join(dir, "src", "runtime.rs"))
	if !strings.Contains(string(runtimeData), "pub fn launch") {
		t.Errorf("runtime.rs def not renamed; got:\n%s", runtimeData)
	}
	taskData, _ := os.ReadFile(filepath.Join(dir, "src", "task.rs"))
	body := string(taskData)
	if !strings.Contains(body, "use crate::runtime::launch") {
		t.Errorf("task.rs use-import not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "launch();") {
		t.Errorf("task.rs call site not rewritten; got:\n%s", body)
	}
}


func TestRename_CHeaderProtoAndCallerRewritten(t *testing.T) {
	// Rename a function in foo.c → expect prototype in foo.h and
	// caller reference in main.c (which #includes foo.h) to be
	// rewritten in lockstep.
	db, dir := setupRefsRepo(t, map[string]string{
		"foo.h": `int compute(int x);
`,
		"foo.c": `int compute(int x) { return x * 2; }
`,
		"main.c": `#include "foo.h"

int run(void) { return compute(5); }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"foo.c:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hData, _ := os.ReadFile(filepath.Join(dir, "foo.h"))
	if !strings.Contains(string(hData), "int calculate(int x);") {
		t.Errorf("foo.h prototype not rewritten; got:\n%s", hData)
	}
	cData, _ := os.ReadFile(filepath.Join(dir, "foo.c"))
	if !strings.Contains(string(cData), "int calculate(int x)") {
		t.Errorf("foo.c definition not rewritten; got:\n%s", cData)
	}
	mData, _ := os.ReadFile(filepath.Join(dir, "main.c"))
	if !strings.Contains(string(mData), "return calculate(5)") {
		t.Errorf("main.c caller not rewritten; got:\n%s", mData)
	}
}

func TestRename_CStaticDoesNotCrossFiles(t *testing.T) {
	// A static helper in a.c and an unrelated same-name static in
	// b.c must NOT collide. Renaming a.c's `helper` leaves b.c alone.
	db, dir := setupRefsRepo(t, map[string]string{
		"a.c": `static int helper(void) { return 1; }
int use_a(void) { return helper(); }
`,
		"b.c": `static int helper(void) { return 2; }
int use_b(void) { return helper(); }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.c:helper"},
		map[string]any{"new_name": "worker", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	aData, _ := os.ReadFile(filepath.Join(dir, "a.c"))
	if !strings.Contains(string(aData), "static int worker(void)") {
		t.Errorf("a.c def not renamed; got:\n%s", aData)
	}
	if !strings.Contains(string(aData), "return worker()") {
		t.Errorf("a.c caller not renamed; got:\n%s", aData)
	}
	bData, _ := os.ReadFile(filepath.Join(dir, "b.c"))
	if !strings.Contains(string(bData), "static int helper(void)") {
		t.Errorf("b.c static MUST remain `helper`; got:\n%s", bData)
	}
	if !strings.Contains(string(bData), "return helper()") {
		t.Errorf("b.c caller MUST remain `helper`; got:\n%s", bData)
	}
}


func TestRename_TSImportRewritten(t *testing.T) {
	// ES-module import + call. Renaming `compute` in lib.ts should
	// rewrite the import ident in app.ts and the call site.
	db, dir := setupRefsRepo(t, map[string]string{
		"src/lib.ts": `export function compute(x: number): number { return x * 2; }
`,
		"src/app.ts": `import { compute } from "./lib";

export function run(): number { return compute(5); }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"src/lib.ts:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	libData, _ := os.ReadFile(filepath.Join(dir, "src", "lib.ts"))
	if !strings.Contains(string(libData), "export function calculate") {
		t.Errorf("lib.ts def not renamed; got:\n%s", libData)
	}
	appData, _ := os.ReadFile(filepath.Join(dir, "src", "app.ts"))
	body := string(appData)
	if !strings.Contains(body, "import { calculate } from \"./lib\"") {
		t.Errorf("app.ts import not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "return calculate(5)") {
		t.Errorf("app.ts call not rewritten; got:\n%s", body)
	}
}

func TestRename_PythonFromImportRewritten(t *testing.T) {
	// `from pkg.lib import compute` plus a call. Rename propagates
	// the def + the import ident + the call site.
	db, dir := setupRefsRepo(t, map[string]string{
		"pkg/__init__.py": "",
		"pkg/lib.py": `def compute(x):
    return x * 2
`,
		"pkg/app.py": `from pkg.lib import compute

def run():
    return compute(5)
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"pkg/lib.py:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	libData, _ := os.ReadFile(filepath.Join(dir, "pkg", "lib.py"))
	if !strings.Contains(string(libData), "def calculate") {
		t.Errorf("lib.py def not renamed; got:\n%s", libData)
	}
	appData, _ := os.ReadFile(filepath.Join(dir, "pkg", "app.py"))
	body := string(appData)
	if !strings.Contains(body, "from pkg.lib import calculate") {
		t.Errorf("app.py import not rewritten; got:\n%s", body)
	}
	if !strings.Contains(body, "return calculate(5)") {
		t.Errorf("app.py call not rewritten; got:\n%s", body)
	}
}


func TestRename_RubyRequireRelativeRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"lib.rb": "def compute(x)\n  x * 2\nend\n",
		"app.rb": "require_relative \"./lib\"\n\ndef run\n  compute(5)\nend\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"lib.rb:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	libData, _ := os.ReadFile(filepath.Join(dir, "lib.rb"))
	if !strings.Contains(string(libData), "def calculate") {
		t.Errorf("lib.rb def not renamed; got:\n%s", libData)
	}
	appData, _ := os.ReadFile(filepath.Join(dir, "app.rb"))
	if !strings.Contains(string(appData), "calculate(5)") {
		t.Errorf("app.rb caller not renamed; got:\n%s", appData)
	}
}

// TestRename_RubyAutoloadClassMethod: Rails-style autoload. Caller does
// not `require_relative` the def file, files live in different dirs.
// Method has a receiver, so candidate selection widens to all .rb files
// and per-ref disambiguation (acceptableTypes) keeps it precise.
func TestRename_RubyAutoloadClassMethod(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"models/mentor_invitation.rb": `class MentorInvitation
  def self.invite(mentor)
    'token'
  end
end
`,
		"controllers/mentors_controller.rb": `class MentorsController
  def create
    MentorInvitation.invite(@mentor)
  end
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"models/mentor_invitation.rb:invite"},
		map[string]any{"new_name": "issue_token", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	defData, _ := os.ReadFile(filepath.Join(dir, "models/mentor_invitation.rb"))
	if !strings.Contains(string(defData), "def self.issue_token") {
		t.Errorf("def not renamed; got:\n%s", defData)
	}
	callerData, _ := os.ReadFile(filepath.Join(dir, "controllers/mentors_controller.rb"))
	if !strings.Contains(string(callerData), "MentorInvitation.issue_token") {
		t.Errorf("autoloaded caller not renamed; got:\n%s", callerData)
	}
}

// TestRename_PHPAutoloadStaticMethod: Laravel-style PSR-4 autoload.
// Caller uses fully-qualified name without a `use` import. Static method
// has a receiver class, so the autoload widening kicks in.
func TestRename_PHPAutoloadStaticMethod(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"app/Models/MentorInvitation.php": `<?php
namespace App\Models;

class MentorInvitation {
    public static function invite($mentor) {
        return "token";
    }
}
`,
		"app/Http/Controllers/MentorsController.php": `<?php
namespace App\Http\Controllers;

class MentorsController {
    public function create() {
        return \App\Models\MentorInvitation::invite($this->mentor);
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"app/Models/MentorInvitation.php:invite"},
		map[string]any{"new_name": "issueToken", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	defData, _ := os.ReadFile(filepath.Join(dir, "app/Models/MentorInvitation.php"))
	if !strings.Contains(string(defData), "function issueToken") {
		t.Errorf("def not renamed; got:\n%s", defData)
	}
	callerData, _ := os.ReadFile(filepath.Join(dir, "app/Http/Controllers/MentorsController.php"))
	if !strings.Contains(string(callerData), "MentorInvitation::issueToken") {
		t.Errorf("autoloaded caller not renamed; got:\n%s", callerData)
	}
}

// TestRename_RubyInstanceMethodLooseReceiver: Session#editable? renamed
// from app/controllers via @session.editable? and session.editable?.
// Ruby has no static type info for ivars/locals, so the strict
// receiver-type filter would skip these. Loose mode kicks in because
// no other class defines editable?, so all obj.editable? sites are
// rewritten. Also exercises the predicate (?) suffix.
func TestRename_RubyInstanceMethodLooseReceiver(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"models/session.rb": `class Session
  def editable?
    locked == false
  end
end
`,
		"controllers/sessions_controller.rb": `class SessionsController
  def edit
    if @session.editable?
      head :ok
    end
  end

  def update
    if session.editable?
      head :ok
    end
  end
end
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"models/session.rb:editable?"},
		map[string]any{"new_name": "writable?", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	defData, _ := os.ReadFile(filepath.Join(dir, "models/session.rb"))
	if !strings.Contains(string(defData), "def writable?") {
		t.Errorf("def not renamed; got:\n%s", defData)
	}
	callerData, _ := os.ReadFile(filepath.Join(dir, "controllers/sessions_controller.rb"))
	if strings.Contains(string(callerData), "editable?") {
		t.Errorf("controller still references editable?; got:\n%s", callerData)
	}
	if !strings.Contains(string(callerData), "@session.writable?") ||
		!strings.Contains(string(callerData), "session.writable?") {
		t.Errorf("controller callers not rewritten; got:\n%s", callerData)
	}
}

// TestRename_RubyMethodCollisionWarning: Session#amount_cents shares its
// name with Payment#amount_cents. Loose receiver mode is unsafe — we'd
// rewrite payment.amount_cents call sites against intent. Strict mode
// holds, but a warning must surface so the user knows obj.amount_cents
// sites were intentionally skipped pending manual review.
func TestRename_RubyMethodCollisionWarning(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"models/session.rb": `class Session
  def amount_cents
    50
  end
end
`,
		"models/payment.rb": `class Payment
  def amount_cents
    100
  end
end
`,
		"controllers/sessions_controller.rb": `class SessionsController
  def show
    [@session.amount_cents, payment.amount_cents]
  end
end
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"models/session.rb:amount_cents"},
		map[string]any{"new_name": "total_cents", "cross_file": true, "dry_run": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rr := res.(*output.RenameResult)
	var found bool
	for _, w := range rr.Warnings {
		if strings.Contains(w, "Payment") && strings.Contains(w, "amount_cents") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected collision warning naming Payment; got warnings=%v", rr.Warnings)
	}
	// Strict mode: only the def is rewritten, no obj.method calls.
	if rr.CodeOccurrences != 1 {
		t.Errorf("expected 1 code occurrence (just the def), got %d", rr.CodeOccurrences)
	}
}

// TestRename_RubyERBMentionWarning: a Ruby method rename succeeds in
// .rb files, but the same name appears in an .erb template. Rename
// can't rewrite ERB safely (receiver type can't be inferred from
// `@var.method` without runtime info), so it must surface a warning
// rather than silently leaving template callers stale.
func TestRename_RubyERBMentionWarning(t *testing.T) {
	db, _ := setupRefsRepo(t, map[string]string{
		"models/mentor_invitation.rb": `class MentorInvitation
  def self.invite(mentor)
    'token'
  end
end
`,
		"controllers/mentors_controller.rb": `class MentorsController
  def create
    MentorInvitation.invite(@mentor)
  end
end
`,
		"app/views/admin/show.html.erb": `<h1>Welcome</h1>
<p><%= MentorInvitation.invite(@mentor) %></p>
<p><%= obj.invite %></p>
`,
	})
	res, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"models/mentor_invitation.rb:invite"},
		map[string]any{"new_name": "issue_token", "cross_file": true, "dry_run": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	rr, ok := res.(*output.RenameResult)
	if !ok {
		t.Fatalf("expected *output.RenameResult, got %T", res)
	}
	var found bool
	for _, w := range rr.Warnings {
		if strings.Contains(w, "show.html.erb") && strings.Contains(w, "template/companion") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ERB-mention warning; got warnings=%v", rr.Warnings)
	}
}

func TestRename_CppHeaderProtoRewritten(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"foo.hpp": "int compute(int x);\n",
		"foo.cpp": "int compute(int x) { return x * 2; }\n",
		"main.cpp": "#include \"foo.hpp\"\n\nint run(void) { return compute(5); }\n",
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"foo.cpp:compute"},
		map[string]any{"new_name": "calculate", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	hData, _ := os.ReadFile(filepath.Join(dir, "foo.hpp"))
	if !strings.Contains(string(hData), "int calculate(int x);") {
		t.Errorf("foo.hpp proto not renamed; got:\n%s", hData)
	}
	mData, _ := os.ReadFile(filepath.Join(dir, "main.cpp"))
	if !strings.Contains(string(mData), "return calculate(5)") {
		t.Errorf("main.cpp caller not renamed; got:\n%s", mData)
	}
}

// TestRename_GoSiblingImplementerPropagation guards hard-blocker 3:
// when renaming YAMLReader.Read (a method on a type that satisfies a
// SAME-PACKAGE interface Reader), every other type in the package
// that ALSO satisfies Reader must rewrite its method in lockstep.
// Without this, the interface decl + the rename target are updated
// but the sibling implementer's method name no longer matches the
// interface — silent compile break. Mirrors the kubernetes
// yaml.Reader / YAMLReader / LineReader shape from dogfood.
func TestRename_GoSiblingImplementerPropagation(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"go.mod": "module example.com/yaml\n",
		"a.go": `package yaml

type Reader interface {
	Read() ([]byte, error)
}

type YAMLReader struct{}

func (r *YAMLReader) Read() ([]byte, error) { return nil, nil }

type LineReader struct{}

func (r *LineReader) Read() ([]byte, error) { return nil, nil }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.go:Read"},
		map[string]any{"new_name": "ReadX", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	body := string(got)
	for _, want := range []string{
		"ReadX() ([]byte, error)",                          // Reader interface decl
		"func (r *YAMLReader) ReadX() ([]byte, error)",     // sym.Receiver
		"func (r *LineReader) ReadX() ([]byte, error)",     // sibling implementer
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q after rename; got:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Read()") && !strings.Contains(body, "ReadX()") {
		t.Errorf("rename did not apply; body:\n%s", body)
	}
}

// TestRename_GoSiblingImplementerArityFilter guards the false-positive
// fix that came with hard-blocker 3: a sibling type whose same-named
// method has a DIFFERENT arity does NOT satisfy the same interface and
// must NOT be propagated. From the kubernetes shape: YAMLDecoder.Read
// takes 1 param (it's an io.Reader, not a yaml.Reader); renaming
// yaml.Reader.Read should leave YAMLDecoder.Read untouched.
func TestRename_GoSiblingImplementerArityFilter(t *testing.T) {
	db, dir := setupRefsRepo(t, map[string]string{
		"go.mod": "module example.com/yaml\n",
		"a.go": `package yaml

type Reader interface {
	Read() ([]byte, error)
}

type YAMLReader struct{}

func (r *YAMLReader) Read() ([]byte, error) { return nil, nil }

// YAMLDecoder satisfies io.Reader (different signature than yaml.Reader).
type YAMLDecoder struct{}

func (d *YAMLDecoder) Read(data []byte) (n int, err error) { return 0, nil }
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"a.go:Read"},
		map[string]any{"new_name": "ReadX", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "a.go"))
	body := string(got)
	if !strings.Contains(body, "func (r *YAMLReader) ReadX() ([]byte, error)") {
		t.Errorf("YAMLReader.Read not renamed; got:\n%s", body)
	}
	// YAMLDecoder.Read has a different arity — must NOT be renamed.
	if !strings.Contains(body, "func (d *YAMLDecoder) Read(data []byte)") {
		t.Errorf("YAMLDecoder.Read was incorrectly renamed despite arity mismatch; got:\n%s", body)
	}
}
