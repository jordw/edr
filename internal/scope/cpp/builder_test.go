package cpp

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

func TestParse_Function(t *testing.T) {
	src := []byte(`int add(int a, int b) {
	return a + b;
}
`)
	r := Parse("a.cpp", src)
	if findDecl(r, "add") == nil {
		t.Errorf("add decl missing; decls=%v", declNames(r))
	}
	a := findDecl(r, "a")
	b := findDecl(r, "b")
	if a == nil || b == nil {
		t.Errorf("params a,b missing; decls=%v", declNames(r))
	}
	if a != nil && a.Kind != scope.KindParam {
		t.Errorf("a: kind=%s want param", a.Kind)
	}
	// Both `a` refs in `a + b` should resolve to the param.
	for _, ref := range refsNamed(r, "a") {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("a ref at %d unresolved: %+v", ref.Span.StartByte, ref.Binding)
		}
	}
}

func TestParse_Class(t *testing.T) {
	src := []byte(`class Foo {
public:
	int getValue() {
		return 42;
	}
};
`)
	r := Parse("a.cpp", src)
	foo := findDecl(r, "Foo")
	if foo == nil {
		t.Fatalf("Foo class missing; decls=%v", declNames(r))
	}
	if foo.Kind != scope.KindClass {
		t.Errorf("Foo: kind=%s want class", foo.Kind)
	}
	getVal := findDecl(r, "getValue")
	if getVal == nil {
		t.Fatalf("getValue method missing; decls=%v", declNames(r))
	}
	if getVal.Kind != scope.KindMethod {
		t.Errorf("getValue: kind=%s want method", getVal.Kind)
	}
	if getVal.Namespace != scope.NSField {
		t.Errorf("getValue: namespace=%s want field", getVal.Namespace)
	}
}

func TestParse_Namespace(t *testing.T) {
	src := []byte(`namespace geom {
int square(int x) { return x * x; }
}
`)
	r := Parse("a.cpp", src)
	geom := findDecl(r, "geom")
	if geom == nil {
		t.Fatalf("geom namespace missing; decls=%v", declNames(r))
	}
	if geom.Kind != scope.KindNamespace {
		t.Errorf("geom: kind=%s want namespace", geom.Kind)
	}
	sq := findDecl(r, "square")
	if sq == nil {
		t.Fatalf("square missing")
	}
	// square should live in the namespace scope, not file scope.
	if sq.Scope == geom.Scope {
		t.Errorf("square should be in namespace scope, got same as namespace decl scope")
	}
}

func TestParse_Preprocessor(t *testing.T) {
	src := []byte(`#include <stdio.h>
#include "local.h"
#define PI 3.14
#define SQUARE(x) ((x) * (x))

int main() {
	return PI;
}
`)
	r := Parse("a.cpp", src)
	pi := findDecl(r, "PI")
	if pi == nil {
		t.Errorf("PI define missing; decls=%v", declNames(r))
	}
	// Includes emit as KindImport with the path as name.
	hasImport := false
	for _, d := range r.Decls {
		if d.Kind == scope.KindImport && strings.Contains(d.Name, "stdio.h") {
			hasImport = true
			break
		}
	}
	if !hasImport {
		t.Errorf("stdio.h include decl missing; decls=%v", declNames(r))
	}
	// `PI` ref inside main should resolve to the define.
	resolved := false
	for _, ref := range refsNamed(r, "PI") {
		if ref.Binding.Kind == scope.BindResolved {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("PI ref should resolve to the #define")
	}
}

func TestParse_ScopeResolution(t *testing.T) {
	src := []byte(`int main() {
	std::cout << 1;
	return 0;
}
`)
	r := Parse("a.cpp", src)
	// `std` is a ref; `cout` should be a property_access ref via `::`.
	var coutRef *scope.Ref
	for i := range r.Refs {
		if r.Refs[i].Name == "cout" {
			coutRef = &r.Refs[i]
			break
		}
	}
	if coutRef == nil {
		t.Fatalf("cout ref missing; refs=%+v", r.Refs)
	}
	if coutRef.Binding.Reason != "property_access" {
		t.Errorf("cout: reason=%q want property_access", coutRef.Binding.Reason)
	}
}

func TestParse_Templates(t *testing.T) {
	src := []byte(`template<typename T>
T max(T a, T b) {
	return a > b ? a : b;
}
`)
	r := Parse("a.cpp", src)
	// Inside template<...>, `T` is a template param — v1 does not emit
	// it as a decl. We at least expect `max` to emit and params to be
	// captured.
	if findDecl(r, "max") == nil {
		t.Errorf("max decl missing; decls=%v", declNames(r))
	}
}

func TestParse_FullSpan(t *testing.T) {
	src := []byte(`class Foo {
	int x;
};
`)
	r := Parse("a.cpp", src)
	foo := findDecl(r, "Foo")
	if foo == nil {
		t.Fatalf("Foo missing")
	}
	if foo.FullSpan.StartByte >= foo.Span.StartByte {
		t.Errorf("FullSpan.Start=%d should be < Span.Start=%d (covers `class` keyword)",
			foo.FullSpan.StartByte, foo.Span.StartByte)
	}
	if foo.FullSpan.EndByte <= foo.Span.EndByte {
		t.Errorf("FullSpan.End=%d should be > Span.End=%d (covers body)",
			foo.FullSpan.EndByte, foo.Span.EndByte)
	}
}
