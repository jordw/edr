package c

import (
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

func findDecl(r *scope.Result, name string) *scope.Decl {
	for i := range r.Decls {
		if r.Decls[i].Name == name {
			return &r.Decls[i]
		}
	}
	return nil
}

func findDeclKind(r *scope.Result, name string, kind scope.DeclKind) *scope.Decl {
	for i := range r.Decls {
		if r.Decls[i].Name == name && r.Decls[i].Kind == kind {
			return &r.Decls[i]
		}
	}
	return nil
}

func refsNamed(r *scope.Result, name string) []scope.Ref {
	var out []scope.Ref
	for _, ref := range r.Refs {
		if ref.Name == name {
			out = append(out, ref)
		}
	}
	return out
}

func declNames(r *scope.Result) []string {
	out := make([]string, 0, len(r.Decls))
	for _, d := range r.Decls {
		out = append(out, d.Name)
	}
	return out
}

func TestParse_FunctionDef(t *testing.T) {
	src := []byte(`int add(int a, int b) {
	return a + b;
}
`)
	r := Parse("a.c", src)
	d := findDecl(r, "add")
	if d == nil {
		t.Fatalf("add not found; decls=%v", declNames(r))
	}
	if d.Kind != scope.KindFunction {
		t.Errorf("add kind = %v, want function", d.Kind)
	}
	// Params a and b should be KindParam.
	for _, n := range []string{"a", "b"} {
		p := findDecl(r, n)
		if p == nil {
			t.Errorf("param %q missing; decls=%v", n, declNames(r))
			continue
		}
		if p.Kind != scope.KindParam {
			t.Errorf("%q kind = %v, want param", n, p.Kind)
		}
	}
	// A function scope should exist.
	hasFuncScope := false
	for _, sc := range r.Scopes {
		if sc.Kind == scope.ScopeFunction {
			hasFuncScope = true
			break
		}
	}
	if !hasFuncScope {
		t.Errorf("no function scope pushed for add")
	}
}

func TestParse_FunctionDeclOnly(t *testing.T) {
	src := []byte(`void bar(int x);
`)
	r := Parse("a.c", src)
	d := findDecl(r, "bar")
	if d == nil {
		t.Fatalf("bar not found; decls=%v", declNames(r))
	}
	if d.Kind != scope.KindFunction {
		t.Errorf("bar kind = %v, want function", d.Kind)
	}
	// No function scope should have been pushed (only the file scope).
	for _, sc := range r.Scopes {
		if sc.Kind == scope.ScopeFunction {
			t.Errorf("function scope pushed for declaration-only bar")
		}
	}
}

func TestParse_StructAndAccess(t *testing.T) {
	src := []byte(`struct Point {
	int x;
	int y;
};

void use(struct Point *p) {
	p->x = 5;
	int val = p->y;
}
`)
	r := Parse("a.c", src)
	pt := findDeclKind(r, "Point", scope.KindType)
	if pt == nil {
		t.Fatalf("Point (type) not found; decls=%v", declNames(r))
	}
	for _, n := range []string{"x", "y"} {
		d := findDeclKind(r, n, scope.KindField)
		if d == nil {
			t.Errorf("field %q missing; decls=%v", n, declNames(r))
		}
	}
	// p->x : x should be a property_access probable ref.
	xRefs := refsNamed(r, "x")
	foundProp := false
	for _, ref := range xRefs {
		if ref.Binding.Kind == scope.BindProbable && ref.Binding.Reason == "property_access" {
			foundProp = true
			break
		}
	}
	if !foundProp {
		t.Errorf("no property_access ref for x; refs=%+v", xRefs)
	}
}

func TestParse_TypedefStruct(t *testing.T) {
	src := []byte(`typedef struct {
	int val;
} Wrapper;

Wrapper w;
`)
	r := Parse("a.c", src)
	// Wrapper should be a KindType decl.
	wr := findDeclKind(r, "Wrapper", scope.KindType)
	if wr == nil {
		t.Fatalf("Wrapper (type) not found; decls=%v", declNames(r))
	}
	// val should be a KindField.
	v := findDeclKind(r, "val", scope.KindField)
	if v == nil {
		t.Errorf("val (field) not found; decls=%v", declNames(r))
	}
	// `Wrapper w;` — w should be a KindVar.
	w := findDeclKind(r, "w", scope.KindVar)
	if w == nil {
		t.Errorf("w (var) not found; decls=%v", declNames(r))
	}
}

func TestParse_TypedefSimple(t *testing.T) {
	src := []byte(`typedef int MyInt;
MyInt x;
`)
	r := Parse("a.c", src)
	mi := findDeclKind(r, "MyInt", scope.KindType)
	if mi == nil {
		t.Fatalf("MyInt (type) not found; decls=%v", declNames(r))
	}
	x := findDeclKind(r, "x", scope.KindVar)
	if x == nil {
		t.Errorf("x (var) not found; decls=%v", declNames(r))
	}
}

func TestParse_Enum(t *testing.T) {
	src := []byte(`enum Color { RED, GREEN, BLUE };
`)
	r := Parse("a.c", src)
	c := findDeclKind(r, "Color", scope.KindType)
	if c == nil {
		t.Fatalf("Color (type) not found; decls=%v", declNames(r))
	}
	for _, n := range []string{"RED", "GREEN", "BLUE"} {
		d := findDeclKind(r, n, scope.KindConst)
		if d == nil {
			t.Errorf("enum constant %q not found; decls=%v", n, declNames(r))
			continue
		}
		// Should live in file scope (ScopeID 1), since C enum constants leak.
		if d.Scope != 1 {
			t.Errorf("%q scope = %d, want 1 (file)", n, d.Scope)
		}
	}
}

func TestParse_Define(t *testing.T) {
	src := []byte(`#define PI 3.14
#define MAX(a, b) ((a) > (b) ? (a) : (b))
`)
	r := Parse("a.c", src)
	pi := findDecl(r, "PI")
	if pi == nil {
		t.Fatalf("PI macro not found; decls=%v", declNames(r))
	}
	if pi.Kind != scope.KindConst {
		t.Errorf("PI kind = %v, want const (using KindConst for macros)", pi.Kind)
	}
	mx := findDecl(r, "MAX")
	if mx == nil {
		t.Errorf("MAX function-like macro not found; decls=%v", declNames(r))
	}
}

func TestParse_Include(t *testing.T) {
	src := []byte(`#include "header.h"
#include <stdio.h>
`)
	r := Parse("a.c", src)
	h := findDecl(r, "header.h")
	if h == nil {
		t.Fatalf("header.h include not found; decls=%v", declNames(r))
	}
	if h.Kind != scope.KindImport {
		t.Errorf("header.h kind = %v, want import", h.Kind)
	}
	// Quoted include: Signature = `header.h\x00"`.
	if h.Signature != "header.h\x00\"" {
		t.Errorf("header.h Signature = %q, want %q", h.Signature, "header.h\x00\"")
	}
	// Imports are never marked Exported (filtered by the resolver).
	if h.Exported {
		t.Errorf("header.h Exported = true; imports should not be Exported")
	}
	sys := findDecl(r, "stdio.h")
	if sys == nil {
		t.Fatalf("stdio.h system include not found; decls=%v", declNames(r))
	}
	// Angle-bracket include: Signature = `stdio.h\x00<>`.
	if sys.Signature != "stdio.h\x00<>" {
		t.Errorf("stdio.h Signature = %q, want %q", sys.Signature, "stdio.h\x00<>")
	}
}

// TestParse_ExportedFileScope: non-static decls at file scope have
// external linkage (Exported=true); `static` ones are internal.
func TestParse_ExportedFileScope(t *testing.T) {
	src := []byte(`extern int x;
static int y;
int f(void) { return 0; }
static int g(void) { return 0; }
`)
	r := Parse("a.c", src)

	x := findDecl(r, "x")
	if x == nil {
		t.Fatalf("x not found; decls=%v", declNames(r))
	}
	if !x.Exported {
		t.Errorf("extern int x: Exported=false, want true")
	}

	y := findDecl(r, "y")
	if y == nil {
		t.Fatalf("y not found; decls=%v", declNames(r))
	}
	if y.Exported {
		t.Errorf("static int y: Exported=true, want false")
	}

	f := findDecl(r, "f")
	if f == nil {
		t.Fatalf("f not found; decls=%v", declNames(r))
	}
	if !f.Exported {
		t.Errorf("int f(...): Exported=false, want true")
	}

	g := findDecl(r, "g")
	if g == nil {
		t.Fatalf("g not found; decls=%v", declNames(r))
	}
	if g.Exported {
		t.Errorf("static int g(...): Exported=true, want false")
	}
}

func TestParse_ForLoop(t *testing.T) {
	src := []byte(`void f(void) {
	for (int i = 0; i < 10; i = i + 1) {
		int j = i;
	}
}
`)
	r := Parse("a.c", src)
	i := findDeclKind(r, "i", scope.KindVar)
	if i == nil {
		t.Fatalf("loop var i not found; decls=%v", declNames(r))
	}
	// i should live in a ScopeFor (or a scope whose chain includes a ScopeFor).
	// At minimum: i's scope should be one whose kind is ScopeFor, OR
	// a child-block (we close for scope when the body closes).
	if i.Scope == 0 {
		t.Errorf("i has scope 0 (stack underflow)")
	}
	sc := r.Scopes[int(i.Scope)-1]
	if sc.Kind != scope.ScopeFor {
		t.Errorf("i scope kind = %v, want for", sc.Kind)
	}
	// Check that a for scope was pushed.
	hasFor := false
	for _, sc := range r.Scopes {
		if sc.Kind == scope.ScopeFor {
			hasFor = true
			break
		}
	}
	if !hasFor {
		t.Errorf("no ScopeFor pushed")
	}
}

func TestParse_FullSpanFunctionDef(t *testing.T) {
	src := []byte(`int add(int a, int b) {
	return a + b;
}
`)
	r := Parse("a.c", src)
	d := findDecl(r, "add")
	if d == nil {
		t.Fatalf("add not found")
	}
	// FullSpan should cover "int add(int a, int b) {\n\treturn a + b;\n}".
	got := string(src[d.FullSpan.StartByte:d.FullSpan.EndByte])
	if !strings.HasPrefix(got, "int add") {
		t.Errorf("FullSpan start: got %q, want to start with 'int add'", got)
	}
	if !strings.HasSuffix(got, "}") {
		t.Errorf("FullSpan end: got %q, want to end with '}'", got)
	}
}

func TestParse_FullSpanStruct(t *testing.T) {
	src := []byte(`struct Point {
	int x;
	int y;
};
`)
	r := Parse("a.c", src)
	d := findDecl(r, "Point")
	if d == nil {
		t.Fatalf("Point not found")
	}
	got := string(src[d.FullSpan.StartByte:d.FullSpan.EndByte])
	if !strings.HasPrefix(got, "struct Point") {
		t.Errorf("FullSpan start: got %q, want to start with 'struct Point'", got)
	}
	if !strings.Contains(got, "}") {
		t.Errorf("FullSpan content should include '}': got %q", got)
	}
}

func TestParse_LocalVar(t *testing.T) {
	src := []byte(`void f(void) {
	int x = 5;
	const int y = 3;
	static int z;
}
`)
	r := Parse("a.c", src)
	for _, n := range []string{"x", "y", "z"} {
		d := findDeclKind(r, n, scope.KindVar)
		if d == nil {
			t.Errorf("local var %q not found; decls=%v", n, declNames(r))
		}
	}
}

func TestParse_HeaderFile(t *testing.T) {
	// Verify Parse works identically for .h files — dispatcher will
	// route both here, so Parse must not care about the extension.
	src := []byte(`#ifndef FOO_H
#define FOO_H

struct Point { int x, y; };
void foo(int n);

#endif
`)
	r := Parse("foo.h", src)
	if findDeclKind(r, "Point", scope.KindType) == nil {
		t.Errorf("Point type not found in .h file; decls=%v", declNames(r))
	}
	if findDecl(r, "foo") == nil {
		t.Errorf("foo func decl not found in .h file; decls=%v", declNames(r))
	}
	if findDecl(r, "FOO_H") == nil {
		t.Errorf("FOO_H macro not found in .h file; decls=%v", declNames(r))
	}
}

func TestParse_NoPanicOnMalformed(t *testing.T) {
	// Truncated / malformed input should not panic.
	srcs := [][]byte{
		[]byte(`int foo(`),
		[]byte(`struct {`),
		[]byte(`#define`),
		[]byte(`#include`),
		[]byte(`typedef`),
		[]byte(`enum X { A, B,`),
		[]byte(`void f() { if (x`),
	}
	for i, src := range srcs {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("case %d: PANIC on %q: %v", i, string(src), rec)
				}
			}()
			_ = Parse("x.c", src)
		}()
	}
}

func TestParse_BlockScope(t *testing.T) {
	src := []byte(`void f(void) {
	int x = 1;
	{
		int x = 2;
	}
}
`)
	r := Parse("a.c", src)
	// We should have two separate x decls, in different scopes.
	var xs []*scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "x" && r.Decls[i].Kind == scope.KindVar {
			xs = append(xs, &r.Decls[i])
		}
	}
	if len(xs) < 2 {
		t.Errorf("expected at least 2 x decls (nested blocks), got %d", len(xs))
	} else if xs[0].Scope == xs[1].Scope {
		t.Errorf("nested x decls share scope %d (expected different scopes)", xs[0].Scope)
	}
	// Verify at least one ScopeBlock exists (the inner {}).
	var blocks int
	for _, sc := range r.Scopes {
		if sc.Kind == scope.ScopeBlock {
			blocks++
		}
	}
	if blocks < 1 {
		t.Errorf("expected at least one ScopeBlock, got %d", blocks)
	}
}

func TestParse_FunctionPointerParsesThrough(t *testing.T) {
	// We deliberately do NOT try to extract fp from `(*fp)(int)`.
	// Just verify we don't crash and produce some output.
	src := []byte(`typedef int (*Fn)(int);
void use(Fn f) { int r = f(1); }
`)
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("panic: %v", rec)
		}
	}()
	r := Parse("a.c", src)
	if findDecl(r, "use") == nil {
		t.Errorf("use function missing; decls=%v", declNames(r))
	}
}

// TestParse_Builtins: libc functions and types referenced by name bind
// as builtins, not as BindUnresolved missing_import.
func TestParse_Builtins(t *testing.T) {
	src := []byte(`int greet(const char *name) {
    printf("hello %s\n", name);
    return strlen(name);
}
`)
	r := Parse("a.c", src)
	for _, name := range []string{"printf", "strlen"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("no ref to builtin %q; refs=%+v", name, r.Refs)
			continue
		}
		if refs[0].Binding.Kind != scope.BindResolved {
			t.Errorf("%q not resolved: %+v", name, refs[0].Binding)
			continue
		}
		if refs[0].Binding.Reason != "builtin" {
			t.Errorf("%q reason = %q, want \"builtin\"", name, refs[0].Binding.Reason)
		}
	}
}
