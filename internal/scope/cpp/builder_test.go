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
	// `cout` is emitted via `::` qualification; the builder stamps
	// Reason="scope_qualified_access" (distinct from `.`/`->` access
	// which uses "property_access"). Same-file resolution doesn't
	// fire here (no `namespace std` decl in the file), so the ref
	// stays probable with that reason.
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
	if coutRef.Binding.Reason != "scope_qualified_access" {
		t.Errorf("cout: reason=%q want scope_qualified_access", coutRef.Binding.Reason)
	}
}

func TestParse_Templates(t *testing.T) {
	src := []byte(`template<typename T>
T max(T a, T b) {
	return a > b ? a : b;
}
`)
	r := Parse("a.cpp", src)
	if findDecl(r, "max") == nil {
		t.Errorf("max decl missing; decls=%v", declNames(r))
	}
}

// TestParse_TemplateFunctionParam: template<typename T> void f(T x) {}
// emits T as a KindType decl inside the function scope; refs to T in
// the body resolve to it (not missing_import).
func TestParse_TemplateFunctionParam(t *testing.T) {
	src := []byte(`template<typename T>
void foo(T x) {
	T local;
}
`)
	r := Parse("a.cpp", src)
	tdecl := findDecl(r, "T")
	if tdecl == nil {
		t.Fatalf("template param T not emitted as decl; decls=%v", declNames(r))
	}
	if tdecl.Kind != scope.KindType {
		t.Errorf("T: kind=%s want type", tdecl.Kind)
	}
	foo := findDecl(r, "foo")
	if foo == nil {
		t.Fatalf("foo decl missing")
	}
	// T decl should live in the function scope (child of file scope),
	// not in file scope directly.
	if tdecl.Scope == foo.Scope {
		t.Errorf("T should be in function body scope, not same scope as foo (%d)", foo.Scope)
	}
	// The body ref `T local` — the T ref should resolve to the template
	// param.
	var bodyTRef *scope.Ref
	for i := range r.Refs {
		if r.Refs[i].Name == "T" && r.Refs[i].Span.StartByte > foo.Span.EndByte {
			bodyTRef = &r.Refs[i]
			break
		}
	}
	if bodyTRef == nil {
		t.Fatalf("body T ref missing; refs=%+v", r.Refs)
	}
	if bodyTRef.Binding.Kind != scope.BindResolved {
		t.Errorf("body T ref unresolved: %+v", bodyTRef.Binding)
	}
	if bodyTRef.Binding.Decl != tdecl.ID {
		t.Errorf("body T ref bound to %x, want template param %x", bodyTRef.Binding.Decl, tdecl.ID)
	}
}

// TestParse_TemplateClassParams: multiple template params become
// KindType decls inside the class scope.
func TestParse_TemplateClassParams(t *testing.T) {
	src := []byte(`template<typename K, typename V>
class Map {
	K key() const;
};
`)
	r := Parse("a.cpp", src)
	for _, name := range []string{"K", "V"} {
		d := findDecl(r, name)
		if d == nil {
			t.Errorf("template param %q not emitted as decl; decls=%v", name, declNames(r))
			continue
		}
		if d.Kind != scope.KindType {
			t.Errorf("%q: kind=%s want type", name, d.Kind)
		}
	}
	mapCls := findDecl(r, "Map")
	if mapCls == nil {
		t.Fatalf("Map class missing")
	}
	// The K ref in `K key()` inside the class body should resolve.
	var kRef *scope.Ref
	for i := range r.Refs {
		if r.Refs[i].Name == "K" {
			kRef = &r.Refs[i]
			break
		}
	}
	if kRef == nil {
		t.Fatalf("K ref missing")
	}
	if kRef.Binding.Kind != scope.BindResolved {
		t.Errorf("K ref should resolve to template param; got %+v", kRef.Binding)
	}
}

// TestParse_ClassFields: `int x; double y;` inside a class body emit
// as KindField in NSField.
func TestParse_ClassFields(t *testing.T) {
	src := []byte(`class Point {
public:
	int x;
	double y;
};
`)
	r := Parse("a.cpp", src)
	for _, name := range []string{"x", "y"} {
		d := findDecl(r, name)
		if d == nil {
			t.Errorf("field %q not emitted; decls=%v", name, declNames(r))
			continue
		}
		if d.Kind != scope.KindField {
			t.Errorf("%q: kind=%s want field", name, d.Kind)
		}
		if d.Namespace != scope.NSField {
			t.Errorf("%q: namespace=%s want field", name, d.Namespace)
		}
	}
}

// TestParse_ClassCommaFields: `int a, b, c;` inside a class emits all
// three as KindField.
func TestParse_ClassCommaFields(t *testing.T) {
	src := []byte(`class C {
	int a, b, c;
};
`)
	r := Parse("a.cpp", src)
	for _, name := range []string{"a", "b", "c"} {
		d := findDecl(r, name)
		if d == nil {
			t.Errorf("field %q not emitted; decls=%v", name, declNames(r))
			continue
		}
		if d.Kind != scope.KindField {
			t.Errorf("%q: kind=%s want field", name, d.Kind)
		}
	}
}

// TestParse_ClassMethodVsField: a method and a field in the same class
// each get the right DeclKind.
func TestParse_ClassMethodVsField(t *testing.T) {
	src := []byte(`class C {
	int f();
	int x;
};
`)
	r := Parse("a.cpp", src)
	f := findDecl(r, "f")
	if f == nil {
		t.Fatalf("f decl missing; decls=%v", declNames(r))
	}
	if f.Kind != scope.KindMethod {
		t.Errorf("f: kind=%s want method", f.Kind)
	}
	x := findDecl(r, "x")
	if x == nil {
		t.Fatalf("x decl missing; decls=%v", declNames(r))
	}
	if x.Kind != scope.KindField {
		t.Errorf("x: kind=%s want field", x.Kind)
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

// TestParse_Builtins: std types under `using namespace std` bind as
// builtins rather than BindUnresolved missing_import.
func TestParse_Builtins(t *testing.T) {
	src := []byte(`using namespace std;
void f() {
    vector<int> v;
    cout << v.size();
}
`)
	r := Parse("a.cpp", src)
	for _, name := range []string{"vector", "cout"} {
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

// TestParse_IncludeQuoted: `#include "foo.hpp"` emits a KindImport
// decl whose Signature is `<path>\x00"` (quote-style suffix). The
// Phase 1 resolver (internal/scope/store/imports_cpp.go) uses the
// quote-style marker to decide whether to resolve against repo files
// vs treat the include as a system header.
func TestParse_IncludeQuoted(t *testing.T) {
	src := []byte(`#include "foo.hpp"
`)
	r := Parse("a.cpp", src)
	imp := findDecl(r, "foo.hpp")
	if imp == nil {
		t.Fatalf("no foo.hpp import decl; names=%v", declNames(r))
	}
	if imp.Kind != scope.KindImport {
		t.Errorf("Kind=%s, want import", imp.Kind)
	}
	want := "foo.hpp\x00\""
	if imp.Signature != want {
		t.Errorf("Signature=%q, want %q", imp.Signature, want)
	}
}

// TestParse_IncludeAngle: `#include <vector>` is a system include;
// Signature must carry the `<>` marker so the resolver skips it.
func TestParse_IncludeAngle(t *testing.T) {
	src := []byte(`#include <vector>
`)
	r := Parse("a.cpp", src)
	imp := findDecl(r, "vector")
	if imp == nil {
		t.Fatalf("no vector import decl; names=%v", declNames(r))
	}
	want := "vector\x00<>"
	if imp.Signature != want {
		t.Errorf("Signature=%q, want %q", imp.Signature, want)
	}
}

// TestParse_UsingNamespace: `using namespace Foo;` emits a KindImport
// decl named Foo with Signature=`Foo\x00*` — the `*` marker signals a
// widening directive to the resolver.
func TestParse_UsingNamespace(t *testing.T) {
	src := []byte(`using namespace Foo;
int main() { return 0; }
`)
	r := Parse("a.cpp", src)
	imp := findDecl(r, "Foo")
	if imp == nil {
		t.Fatalf("no Foo using-directive import decl; names=%v", declNames(r))
	}
	if imp.Kind != scope.KindImport {
		t.Errorf("Kind=%s, want import", imp.Kind)
	}
	want := "Foo\x00*"
	if imp.Signature != want {
		t.Errorf("Signature=%q, want %q", imp.Signature, want)
	}
}

// TestParse_UsingDeclaration: `using Foo::Bar;` emits a KindImport
// decl named Bar with Signature=`Foo\x00Bar`. The resolver uses the
// qualifier Foo to look up Bar in the repo's namespace index.
func TestParse_UsingDeclaration(t *testing.T) {
	src := []byte(`using Foo::Bar;
int main() { return 0; }
`)
	r := Parse("a.cpp", src)
	imp := findDecl(r, "Bar")
	if imp == nil {
		t.Fatalf("no Bar using-declaration import decl; names=%v", declNames(r))
	}
	if imp.Kind != scope.KindImport {
		t.Errorf("Kind=%s, want import", imp.Kind)
	}
	want := "Foo\x00Bar"
	if imp.Signature != want {
		t.Errorf("Signature=%q, want %q", imp.Signature, want)
	}
}

// TestParse_NamespaceContainsClass: a class declared inside a
// namespace has a reachable FQN — the builder records the namespace
// as a scope ancestor of the class. (The resolver joins namespace
// names via Span containment; we verify both decls exist with
// appropriate Exported flags and spans.)
func TestParse_NamespaceContainsClass(t *testing.T) {
	src := []byte(`namespace Foo {
	class X {};
}
`)
	r := Parse("a.cpp", src)
	foo := findDecl(r, "Foo")
	x := findDecl(r, "X")
	if foo == nil || foo.Kind != scope.KindNamespace {
		t.Fatalf("Foo namespace missing/wrong kind: %+v", foo)
	}
	if x == nil || x.Kind != scope.KindClass {
		t.Fatalf("X class missing/wrong kind: %+v", x)
	}
	if !foo.Exported {
		t.Errorf("Foo namespace: Exported=false, want true")
	}
	if !x.Exported {
		t.Errorf("X class inside Foo: Exported=false, want true")
	}
	// Span containment: X's Span must lie inside Foo's FullSpan so
	// the store resolver can compute X's FQN as "Foo::X".
	if !(x.Span.StartByte >= foo.FullSpan.StartByte && x.Span.EndByte <= foo.FullSpan.EndByte) {
		t.Errorf("X span %v not inside Foo.FullSpan %v", x.Span, foo.FullSpan)
	}
}

// TestParse_ExportedStaticVsExternal: a file-scope function with
// `static` storage-class is NOT Exported; a plain file-scope function
// IS Exported.
func TestParse_ExportedStaticVsExternal(t *testing.T) {
	src := []byte(`static int internal(int x) { return x; }
int external(int x) { return x; }
class Bar {};
`)
	r := Parse("a.cpp", src)
	internalFn := findDecl(r, "internal")
	externalFn := findDecl(r, "external")
	bar := findDecl(r, "Bar")
	if internalFn == nil || externalFn == nil || bar == nil {
		t.Fatalf("missing decls; names=%v", declNames(r))
	}
	if internalFn.Exported {
		t.Errorf("static function %q: Exported=true, want false", internalFn.Name)
	}
	if !externalFn.Exported {
		t.Errorf("non-static function %q: Exported=false, want true", externalFn.Name)
	}
	if !bar.Exported {
		t.Errorf("class %q: Exported=false, want true", bar.Name)
	}
}

// TestParse_UsingTypeAliasFallsThrough: `using T = Expr;` must still
// emit T as a KindType decl (the builder falls through to the typedef
// path when it detects `=`).
func TestParse_UsingTypeAliasFallsThrough(t *testing.T) {
	src := []byte(`using MyInt = int;
`)
	r := Parse("a.cpp", src)
	t1 := findDecl(r, "MyInt")
	if t1 == nil {
		t.Fatalf("MyInt alias missing; names=%v", declNames(r))
	}
	if t1.Kind != scope.KindType {
		t.Errorf("MyInt kind=%s, want type", t1.Kind)
	}
}
