package csharp

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

func TestParse_BasicClassMethodFieldConstructor(t *testing.T) {
	src := []byte(`namespace MyApp;

public class Counter {
    int value;
    string label;

    public Counter(int initial) {
        this.value = initial;
    }

    public void Increment(int by) {
        value = value + by;
    }
}
`)
	r := Parse("Counter.cs", src)

	classDecl := findDeclKind(r, "Counter", scope.KindClass)
	if classDecl == nil {
		t.Fatalf("class Counter missing; decls=%v", declNames(r))
	}

	valueDecl := findDeclKind(r, "value", scope.KindField)
	if valueDecl == nil {
		t.Errorf("field `value` missing; decls=%v", declNames(r))
	} else if valueDecl.Namespace != scope.NSField {
		t.Errorf("field `value` namespace = %v, want NSField", valueDecl.Namespace)
	}
	labelDecl := findDeclKind(r, "label", scope.KindField)
	if labelDecl == nil {
		t.Errorf("field `label` missing")
	}

	methodCount := 0
	for _, d := range r.Decls {
		if d.Kind == scope.KindMethod {
			methodCount++
		}
	}
	if methodCount < 2 {
		t.Errorf("expected >=2 method decls (Counter ctor + Increment), got %d; decls=%v",
			methodCount, declNames(r))
	}
	incDecl := findDeclKind(r, "Increment", scope.KindMethod)
	if incDecl == nil {
		t.Errorf("method `Increment` missing")
	}
	var ctorDecl *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name == "Counter" && d.Kind == scope.KindMethod {
			ctorDecl = d
			break
		}
	}
	if ctorDecl == nil {
		t.Errorf("constructor decl (name=Counter, kind=method) missing")
	}

	initialDecl := findDeclKind(r, "initial", scope.KindParam)
	if initialDecl == nil {
		t.Errorf("param `initial` missing")
	}
	byDecl := findDeclKind(r, "by", scope.KindParam)
	if byDecl == nil {
		t.Errorf("param `by` missing")
	}
}

func TestParse_BlockNamespace(t *testing.T) {
	src := []byte(`namespace MyApp.Core {
    class Foo {
        int x;
    }
}
`)
	r := Parse("Foo.cs", src)
	nsDecl := findDeclKind(r, "MyApp", scope.KindNamespace)
	if nsDecl == nil {
		t.Fatalf("namespace decl missing; decls=%v", declNames(r))
	}
	// Find a namespace scope.
	var nsScope *scope.Scope
	for i := range r.Scopes {
		if r.Scopes[i].Kind == scope.ScopeNamespace {
			nsScope = &r.Scopes[i]
			break
		}
	}
	if nsScope == nil {
		t.Fatalf("no ScopeNamespace pushed; scopes=%+v", r.Scopes)
	}
	// The class Foo should live inside the namespace scope, not file scope.
	fooDecl := findDeclKind(r, "Foo", scope.KindClass)
	if fooDecl == nil {
		t.Fatalf("class Foo missing")
	}
	if fooDecl.Scope != nsScope.ID {
		t.Errorf("class Foo scope = %d, want namespace scope %d", fooDecl.Scope, nsScope.ID)
	}
}

func TestParse_FileScopedNamespace(t *testing.T) {
	src := []byte(`namespace MyApp.Core;

class Foo {
    int x;
}
`)
	r := Parse("Foo.cs", src)
	nsDecl := findDeclKind(r, "MyApp", scope.KindNamespace)
	if nsDecl == nil {
		t.Fatalf("namespace decl missing; decls=%v", declNames(r))
	}
	// A namespace scope should be open.
	var nsScope *scope.Scope
	for i := range r.Scopes {
		if r.Scopes[i].Kind == scope.ScopeNamespace {
			nsScope = &r.Scopes[i]
			break
		}
	}
	if nsScope == nil {
		t.Fatalf("file-scoped namespace did not push a ScopeNamespace; scopes=%+v", r.Scopes)
	}
	fooDecl := findDeclKind(r, "Foo", scope.KindClass)
	if fooDecl == nil {
		t.Fatalf("class Foo missing")
	}
	if fooDecl.Scope != nsScope.ID {
		t.Errorf("class Foo scope = %d, want namespace scope %d", fooDecl.Scope, nsScope.ID)
	}
}

func TestParse_PropertyAutoAndExprBodied(t *testing.T) {
	src := []byte(`class C {
    public int X { get; set; }
    public int Y => _y;
    private int _y;
}
`)
	r := Parse("C.cs", src)
	xDecl := findDeclKind(r, "X", scope.KindField)
	if xDecl == nil {
		t.Fatalf("property X (as KindField) missing; decls=%v", declNames(r))
	}
	if xDecl.Namespace != scope.NSField {
		t.Errorf("X namespace = %v, want NSField", xDecl.Namespace)
	}
	yDecl := findDeclKind(r, "Y", scope.KindField)
	if yDecl == nil {
		t.Errorf("property Y (as KindField) missing; decls=%v", declNames(r))
	}
	yField := findDeclKind(r, "_y", scope.KindField)
	if yField == nil {
		t.Errorf("field _y missing")
	}
}

func TestParse_ThisAndBaseField(t *testing.T) {
	src := []byte(`class Foo {
    int x;
    int y;

    void Set(int x) {
        this.x = x;
        this.y = base.y;
    }
}
`)
	r := Parse("Foo.cs", src)

	xDecl := findDeclKind(r, "x", scope.KindField)
	yDecl := findDeclKind(r, "y", scope.KindField)
	if xDecl == nil || yDecl == nil {
		t.Fatalf("field decls missing; decls=%v", declNames(r))
	}

	thisXResolved := 0
	baseYResolved := 0
	for _, ref := range r.Refs {
		if ref.Binding.Reason != "this_dot_field" {
			continue
		}
		switch ref.Name {
		case "x":
			if ref.Binding.Decl == xDecl.ID {
				thisXResolved++
			}
		case "y":
			if ref.Binding.Decl == yDecl.ID {
				baseYResolved++
			}
		}
	}
	if thisXResolved < 1 {
		t.Errorf("expected this.x to resolve to field x at least once, got %d", thisXResolved)
	}
	if baseYResolved < 1 {
		t.Errorf("expected base.y / this.y to resolve at least once, got %d", baseYResolved)
	}
}

func TestParse_GenericTypeParams(t *testing.T) {
	src := []byte(`class Box<T> {
    T Value;
}
`)
	r := Parse("Box.cs", src)
	if findDeclKind(r, "Box", scope.KindClass) == nil {
		t.Fatalf("class Box missing; decls=%v", declNames(r))
	}
	tDecl := findDeclKind(r, "T", scope.KindType)
	if tDecl == nil {
		t.Fatalf("generic T decl missing; decls=%v", declNames(r))
	}
	if tDecl.Scope == 0 {
		t.Errorf("T should be scoped to class, got scope=0")
	}
	// Body ref to T should resolve to type-param decl.
	found := false
	for _, ref := range r.Refs {
		if ref.Name == "T" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == tDecl.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ref to T did not resolve to type-param decl")
	}
}

func TestParse_UsingDirective(t *testing.T) {
	src := []byte(`using System;
using static System.Math;
using MyList = System.Collections.Generic.List;

class A {
    List items;
}
`)
	r := Parse("A.cs", src)

	sysDecl := findDeclKind(r, "System", scope.KindImport)
	if sysDecl == nil {
		t.Errorf("using System -> KindImport `System` missing; decls=%v", declNames(r))
	}
	mathDecl := findDeclKind(r, "Math", scope.KindImport)
	if mathDecl == nil {
		t.Errorf("static import decl `Math` missing; decls=%v", declNames(r))
	}
	aliasDecl := findDeclKind(r, "MyList", scope.KindImport)
	if aliasDecl == nil {
		t.Errorf("alias import `MyList` missing; decls=%v", declNames(r))
	}
}

func TestParse_LocalVarDoesNotLeak(t *testing.T) {
	src := []byte(`class A {
    void F() {
        int localOnly = 5;
    }
}
`)
	r := Parse("A.cs", src)
	localDecl := findDecl(r, "localOnly")
	if localDecl == nil {
		t.Fatalf("local var `localOnly` missing; decls=%v", declNames(r))
	}
	if localDecl.Kind != scope.KindVar {
		t.Errorf("localOnly kind = %v, want KindVar", localDecl.Kind)
	}
	if localDecl.Scope == 0 {
		t.Errorf("localOnly scope is 0 (file); should be function scope")
	}
	var hostScope *scope.Scope
	for i := range r.Scopes {
		if r.Scopes[i].ID == localDecl.Scope {
			hostScope = &r.Scopes[i]
			break
		}
	}
	if hostScope == nil {
		t.Fatalf("could not find scope %d for localOnly", localDecl.Scope)
	}
	if hostScope.Kind != scope.ScopeFunction {
		t.Errorf("localOnly scope kind = %v, want ScopeFunction", hostScope.Kind)
	}
}

func TestParse_ForeachVar(t *testing.T) {
	src := []byte(`class A {
    void F() {
        foreach (var item in coll) {
            Use(item);
        }
    }
}
`)
	r := Parse("A.cs", src)
	itemDecl := findDeclKind(r, "item", scope.KindVar)
	if itemDecl == nil {
		t.Errorf("foreach var `item` missing; decls=%v", declNames(r))
	}
}

func TestParse_UsingVar(t *testing.T) {
	src := []byte(`class A {
    void F() {
        using var resource = Open();
    }
}
`)
	r := Parse("A.cs", src)
	resDecl := findDeclKind(r, "resource", scope.KindVar)
	if resDecl == nil {
		t.Errorf("using var `resource` missing; decls=%v", declNames(r))
	}
}

func TestParse_FullSpanOnClassAndMethod(t *testing.T) {
	src := []byte(`class Box {
    int value;
    void Unwrap() {
        return;
    }
}

interface IShape {
    int Area();
}
`)
	r := Parse("Box.cs", src)

	cases := []struct {
		name       string
		wantPrefix string
	}{
		{"Box", "class Box"},
		{"IShape", "interface IShape"},
	}
	for _, c := range cases {
		d := findDecl(r, c.name)
		if d == nil {
			t.Errorf("%s: decl missing", c.name)
			continue
		}
		if d.FullSpan.StartByte >= d.Span.StartByte {
			t.Errorf("%s: FullSpan.StartByte=%d should be < Span.StartByte=%d",
				c.name, d.FullSpan.StartByte, d.Span.StartByte)
		}
		if d.FullSpan.EndByte <= d.Span.EndByte {
			t.Errorf("%s: FullSpan.EndByte=%d should be > Span.EndByte=%d",
				c.name, d.FullSpan.EndByte, d.Span.EndByte)
		}
		got := string(src[d.FullSpan.StartByte:d.FullSpan.EndByte])
		if !strings.HasPrefix(got, c.wantPrefix) {
			t.Errorf("%s: FullSpan starts %q, want prefix %q", c.name, got, c.wantPrefix)
		}
		if got[len(got)-1] != '}' {
			t.Errorf("%s: FullSpan does not end at }: %q", c.name, got)
		}
	}

	// Method Unwrap: FullSpan covers body.
	unwrap := findDecl(r, "Unwrap")
	if unwrap == nil {
		t.Fatal("method Unwrap missing")
	}
	if unwrap.FullSpan.EndByte <= unwrap.Span.EndByte {
		t.Errorf("Unwrap: FullSpan.EndByte=%d should cover body past Span.EndByte=%d",
			unwrap.FullSpan.EndByte, unwrap.Span.EndByte)
	}
}

func TestParse_RecordPrimaryConstructor(t *testing.T) {
	src := []byte(`record Point(int X, int Y);
`)
	r := Parse("Point.cs", src)
	if findDeclKind(r, "Point", scope.KindClass) == nil {
		t.Fatalf("record Point missing; decls=%v", declNames(r))
	}
	xDecl := findDeclKind(r, "X", scope.KindField)
	if xDecl == nil {
		t.Errorf("record primary field X missing; decls=%v", declNames(r))
	}
	yDecl := findDeclKind(r, "Y", scope.KindField)
	if yDecl == nil {
		t.Errorf("record primary field Y missing; decls=%v", declNames(r))
	}
}

func TestParse_RecordPrimaryConstructorWithBody(t *testing.T) {
	src := []byte(`record Point(int X, int Y) {
    int Z;
}
`)
	r := Parse("Point.cs", src)
	xDecl := findDeclKind(r, "X", scope.KindField)
	if xDecl == nil {
		t.Errorf("record primary field X missing; decls=%v", declNames(r))
	}
	yDecl := findDeclKind(r, "Y", scope.KindField)
	if yDecl == nil {
		t.Errorf("record primary field Y missing; decls=%v", declNames(r))
	}
	zDecl := findDeclKind(r, "Z", scope.KindField)
	if zDecl == nil {
		t.Errorf("body field Z missing; decls=%v", declNames(r))
	}
}

func TestParse_EnumAndStruct(t *testing.T) {
	src := []byte(`enum Color {
    Red, Green, Blue
}

struct Vec2 {
    int x;
    int y;
}
`)
	r := Parse("E.cs", src)
	if findDeclKind(r, "Color", scope.KindEnum) == nil {
		t.Errorf("enum decl `Color` missing; decls=%v", declNames(r))
	}
	if findDeclKind(r, "Vec2", scope.KindClass) == nil {
		t.Errorf("struct decl `Vec2` missing (expected KindClass)")
	}
}

func TestParse_AttributeIsSkipped(t *testing.T) {
	src := []byte(`[Serializable]
class A {
    int x;
}
`)
	r := Parse("A.cs", src)
	if findDeclKind(r, "A", scope.KindClass) == nil {
		t.Fatalf("class A missing; decls=%v", declNames(r))
	}
	// Ensure no ref named "Serializable" was emitted (attribute was skipped).
	for _, ref := range r.Refs {
		if ref.Name == "Serializable" {
			t.Errorf("attribute ident `Serializable` emitted as a ref; should be skipped")
		}
	}
}

func TestParse_ParseDoesNotPanicOnEmptyInput(t *testing.T) {
	_ = Parse("empty.cs", []byte{})
	_ = Parse("empty.cs", []byte("   \n\n  "))
}

// TestParse_Builtins: System.* types and primitive aliases bind as
// builtins, not as BindUnresolved missing_import.
func TestParse_Builtins(t *testing.T) {
	src := []byte(`public class Greeter {
    public string Greet(string name) {
        Console.WriteLine(name);
        throw new ArgumentNullException(name);
    }
}
`)
	r := Parse("a.cs", src)
	for _, name := range []string{"Console", "ArgumentNullException"} {
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

// TestParse_PartialClassSameFileMerging: two `partial class Foo` blocks
// in a single file should share a DeclID after within-file merging.
// Cross-file partials are handled by the store-level reconciliation.
func TestParse_PartialClassSameFileMerging(t *testing.T) {
	src := []byte(`partial class Foo {
    public int a;
}
partial class Foo {
    public int b;
}
`)
	r := Parse("a.cs", src)
	var foos []*scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "Foo" && r.Decls[i].Kind == scope.KindClass {
			foos = append(foos, &r.Decls[i])
		}
	}
	if len(foos) < 2 {
		t.Fatalf("expected >=2 Foo class decls; got %d", len(foos))
	}
	if foos[0].ID != foos[1].ID {
		t.Errorf("partial class Foo has distinct IDs %d vs %d (should merge)",
			foos[0].ID, foos[1].ID)
	}
}
