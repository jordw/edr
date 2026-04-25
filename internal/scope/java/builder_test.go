package java

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

func declsOfKind(r *scope.Result, kind scope.DeclKind) []scope.Decl {
	var out []scope.Decl
	for _, d := range r.Decls {
		if d.Kind == kind {
			out = append(out, d)
		}
	}
	return out
}

func TestParse_BasicClassMethodFieldConstructor(t *testing.T) {
	src := []byte(`package com.example;

public class Counter {
    int value;
    String label;

    public Counter(int initial) {
        this.value = initial;
    }

    public void increment(int by) {
        value = value + by;
    }
}
`)
	r := Parse("Counter.java", src)

	// Class decl.
	classDecl := findDeclKind(r, "Counter", scope.KindClass)
	if classDecl == nil {
		t.Fatalf("class Counter missing; decls=%v", declNames(r))
	}

	// Field decls `value` and `label` in class scope, NSField.
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

	// Method decls: Counter (constructor) and increment.
	// The constructor and class share the same name; both decls should
	// exist and have distinct kinds.
	methodCount := 0
	for _, d := range r.Decls {
		if d.Kind == scope.KindMethod {
			methodCount++
		}
	}
	if methodCount < 2 {
		t.Errorf("expected at least 2 method decls (Counter ctor + increment), got %d; decls=%v",
			methodCount, declNames(r))
	}
	incDecl := findDeclKind(r, "increment", scope.KindMethod)
	if incDecl == nil {
		t.Errorf("method `increment` missing")
	}
	// Constructor: method decl named `Counter` (same as class) with NSField.
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

	// Params: `initial` (ctor) and `by` (increment).
	initialDecl := findDeclKind(r, "initial", scope.KindParam)
	if initialDecl == nil {
		t.Errorf("param `initial` missing")
	}
	byDecl := findDeclKind(r, "by", scope.KindParam)
	if byDecl == nil {
		t.Errorf("param `by` missing")
	}
}

func TestParse_ThisAndSuperField(t *testing.T) {
	src := []byte(`class Foo {
    int x;
    int y;

    void set(int x) {
        this.x = x;
        this.y = super.y;
    }
}
`)
	r := Parse("Foo.java", src)

	xDecl := findDeclKind(r, "x", scope.KindField)
	yDecl := findDeclKind(r, "y", scope.KindField)
	if xDecl == nil || yDecl == nil {
		t.Fatalf("field decls missing; decls=%v", declNames(r))
	}

	thisXResolved := 0
	superYResolved := 0
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
				superYResolved++
			}
		}
	}
	if thisXResolved < 1 {
		t.Errorf("expected this.x to resolve to field x at least once, got %d", thisXResolved)
	}
	if superYResolved < 1 {
		// We have both `this.y = ...` and `super.y` — at least one should resolve.
		t.Errorf("expected super.y / this.y to resolve at least once, got %d", superYResolved)
	}
}

// TestParse_ExtendsGenericArgs verifies that generic *arguments* in the
// supertype list (class C extends Base<Item>) are emitted as REFS, not
// as phantom type-param decls on C. Pre-fix the parser left
// genericParamsExpected=true after the class name and the '<' inside
// 'Base<Item>' consumed it as if it were a type-param list, so 'Item'
// became a KindType decl on C — wrong, and rename/refs-to undercount.
func TestParse_ExtendsGenericArgs(t *testing.T) {
	src := []byte(`public class Counter extends Base<Item> implements Sized { }
`)
	r := Parse("Counter.java", src)
	for _, d := range r.Decls {
		if (d.Name == "Item" || d.Name == "Base" || d.Name == "Sized") && d.Kind == scope.KindType {
			t.Errorf("phantom type decl %q (kind=%s) — should be a ref", d.Name, d.Kind)
		}
	}
	hasItemRef := false
	for _, ref := range r.Refs {
		if ref.Name == "Item" {
			hasItemRef = true
		}
	}
	if !hasItemRef {
		t.Errorf("want Item as ref; refs=%+v", r.Refs)
	}
}

func TestParse_GenericTypeParams(t *testing.T) {
	src := []byte(`class Box<T> {
    T value;
}
`)
	r := Parse("Box.java", src)
	if findDeclKind(r, "Box", scope.KindClass) == nil {
		t.Fatalf("class Box missing; decls=%v", declNames(r))
	}
	tDecl := findDeclKind(r, "T", scope.KindType)
	if tDecl == nil {
		t.Fatalf("generic T decl missing; decls=%v", declNames(r))
	}
	// T should be scoped to the class, not file scope.
	if tDecl.Scope == 0 {
		t.Errorf("T should be scoped to class, got scope=0")
	}
	// The `T value` reference to T in the field decl should resolve to
	// the type param.
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

func TestParse_LocalVarDoesNotLeak(t *testing.T) {
	src := []byte(`class A {
    void f() {
        int localOnly = 5;
    }
}
`)
	r := Parse("A.java", src)
	localDecl := findDecl(r, "localOnly")
	if localDecl == nil {
		t.Fatalf("local var `localOnly` missing; decls=%v", declNames(r))
	}
	if localDecl.Kind != scope.KindVar {
		t.Errorf("localOnly kind = %v, want KindVar", localDecl.Kind)
	}
	// The local's scope must not be the file scope or class scope — it
	// should be nested inside the function scope.
	if localDecl.Scope == 0 {
		t.Errorf("localOnly scope is 0 (file); should be function scope")
	}
	// The scope for localOnly should have ScopeFunction kind.
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

func TestParse_ForLoopVar(t *testing.T) {
	src := []byte(`class A {
    void f() {
        for (int x : coll) {
            use(x);
        }
    }
}
`)
	r := Parse("A.java", src)
	xDecl := findDeclKind(r, "x", scope.KindVar)
	if xDecl == nil {
		t.Errorf("for-each var `x` missing; decls=%v", declNames(r))
	}
}

func TestParse_ImportDecl(t *testing.T) {
	src := []byte(`import java.util.List;
import static com.foo.Bar.baz;

class A {
    List items;
}
`)
	r := Parse("A.java", src)

	// `List` should be a KindImport decl.
	listDecl := findDeclKind(r, "List", scope.KindImport)
	if listDecl == nil {
		t.Fatalf("import decl `List` missing; decls=%v", declNames(r))
	}
	bazDecl := findDeclKind(r, "baz", scope.KindImport)
	if bazDecl == nil {
		t.Errorf("static import decl `baz` missing; decls=%v", declNames(r))
	}

	// The `List` ref inside the class body should resolve to the import.
	found := false
	for _, ref := range r.Refs {
		if ref.Name == "List" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == listDecl.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ref to List did not resolve to import decl; refs=%+v", refsNamed(r, "List"))
	}
}

func TestParse_MethodOverloading(t *testing.T) {
	src := []byte(`class Calc {
    int add(int a, int b) { return a + b; }
    double add(double a, double b) { return a + b; }
}
`)
	r := Parse("Calc.java", src)

	addDecls := 0
	for _, d := range r.Decls {
		if d.Name == "add" && d.Kind == scope.KindMethod {
			addDecls++
		}
	}
	if addDecls < 2 {
		t.Errorf("expected 2 overloaded `add` method decls, got %d; decls=%v",
			addDecls, declNames(r))
	}
}

func TestParse_FullSpanOnScopeOwningDecls(t *testing.T) {
	src := []byte(`class Box {
    int value;
    void unwrap() {
        return;
    }
}

interface Shape {
    int area();
}
`)
	r := Parse("Box.java", src)

	cases := []struct {
		name       string
		wantPrefix string
	}{
		{"Box", "class Box"},
		{"Shape", "interface Shape"},
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

	// Method unwrap: scope-owning decl, FullSpan covers body.
	unwrap := findDecl(r, "unwrap")
	if unwrap == nil {
		t.Fatal("method unwrap missing")
	}
	if unwrap.FullSpan.EndByte <= unwrap.Span.EndByte {
		t.Errorf("unwrap: FullSpan.EndByte=%d should cover body past Span.EndByte=%d",
			unwrap.FullSpan.EndByte, unwrap.Span.EndByte)
	}
}

func TestParse_EnumAndRecord(t *testing.T) {
	src := []byte(`enum Color {
    RED, GREEN, BLUE
}

record Point(int x, int y) {
}
`)
	r := Parse("E.java", src)
	if findDeclKind(r, "Color", scope.KindEnum) == nil {
		t.Errorf("enum decl `Color` missing; decls=%v", declNames(r))
	}
	if findDeclKind(r, "Point", scope.KindClass) == nil {
		t.Errorf("record decl `Point` missing (expected KindClass)")
	}
}

func TestParse_AnnotationType(t *testing.T) {
	src := []byte(`@interface MyAnno {
    String value();
}
`)
	r := Parse("MyAnno.java", src)
	d := findDeclKind(r, "MyAnno", scope.KindInterface)
	if d == nil {
		t.Fatalf("@interface decl `MyAnno` missing; decls=%v", declNames(r))
	}
}

func TestParse_AnonymousInnerClassPushesScope(t *testing.T) {
	src := []byte(`class A {
    void f() {
        Runnable r = new Runnable() {
            public void run() {
                int innerX = 0;
            }
        };
    }
}
`)
	r := Parse("A.java", src)
	// The anonymous inner class's member 'run' should be a method decl.
	// The inner var 'innerX' should not leak to file scope.
	innerXDecl := findDecl(r, "innerX")
	if innerXDecl == nil {
		t.Fatalf("innerX missing; decls=%v", declNames(r))
	}
	if innerXDecl.Scope == 0 {
		t.Errorf("innerX should not be at file scope")
	}
	// Parse should not panic on anonymous inner class syntax. Already
	// verified by reaching this point without a panic.
}

func TestParse_ParseDoesNotPanicOnEmptyInput(t *testing.T) {
	_ = Parse("empty.java", []byte{})
	_ = Parse("empty.java", []byte("   \n\n  "))
}

// findFieldDeclInScope returns the NSField decl named `name` whose
// enclosing class body matches the first declared class of that name.
func findFieldDeclInClass(r *scope.Result, class, name string) *scope.Decl {
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Kind != scope.KindClass || d.Name != class {
			continue
		}
		// Find the class body scope: the ScopeClass whose parent is
		// d.Scope and whose span starts inside d.FullSpan.
		for _, s := range r.Scopes {
			if s.Kind != scope.ScopeClass && s.Kind != scope.ScopeInterface {
				continue
			}
			if s.Parent != d.Scope {
				continue
			}
			if s.Span.StartByte < d.FullSpan.StartByte || s.Span.StartByte > d.FullSpan.EndByte {
				continue
			}
			// Look up NSField decl in this scope.
			for j := range r.Decls {
				dd := &r.Decls[j]
				if dd.Scope == s.ID && dd.Namespace == scope.NSField && dd.Name == name {
					return dd
				}
			}
		}
	}
	return nil
}

func TestParse_InheritedFieldUnqualified(t *testing.T) {
	// Bare `x` inside Child.use() should resolve to P.x via one-level
	// same-file inheritance.
	src := []byte(`class P {
    int x;
}
class C extends P {
    int use() { return x; }
}
`)
	r := Parse("Inh.java", src)

	px := findFieldDeclInClass(r, "P", "x")
	if px == nil {
		t.Fatalf("expected P.x field decl; decls=%v", declNames(r))
	}

	got := 0
	for _, ref := range r.Refs {
		if ref.Name != "x" {
			continue
		}
		if ref.Binding.Reason != "inherited_field" {
			continue
		}
		if ref.Binding.Decl != px.ID {
			t.Errorf("inherited_field ref for x resolves to %v, want P.x=%v", ref.Binding.Decl, px.ID)
			continue
		}
		got++
	}
	if got < 1 {
		t.Errorf("expected at least one inherited_field ref for x, got %d; refs:\n%s", got, dumpRefs(r, "x"))
	}
}

func TestParse_InheritedFieldThisDot(t *testing.T) {
	// `this.x` inside Child.use() should resolve to P.x via one-level
	// same-file inheritance and carry Reason "inherited_field".
	src := []byte(`class P {
    int x;
}
class C extends P {
    int use() { return this.x; }
}
`)
	r := Parse("Inh.java", src)

	px := findFieldDeclInClass(r, "P", "x")
	if px == nil {
		t.Fatalf("expected P.x field decl; decls=%v", declNames(r))
	}

	got := 0
	for _, ref := range r.Refs {
		if ref.Name != "x" {
			continue
		}
		if ref.Binding.Reason == "inherited_field" && ref.Binding.Decl == px.ID {
			got++
		}
	}
	if got < 1 {
		t.Errorf("expected this.x to resolve to P.x via inherited_field, got %d;\n%s", got, dumpRefs(r, "x"))
	}
}

func TestParse_InheritedFieldMissingSupertype(t *testing.T) {
	// `class C extends Missing { ... x ... }` — Missing is not declared
	// in this file. `x` must NOT resolve to any random same-name decl.
	src := []byte(`class Unrelated {
    int x;
}
class C extends Missing {
    int use() { return x; }
}
`)
	r := Parse("Inh.java", src)

	for _, ref := range r.Refs {
		if ref.Name != "x" {
			continue
		}
		if ref.Scope == 0 {
			continue
		}
		// The `x` inside C.use() must not be resolved at all (neither
		// inherited_field nor direct_scope nor implicit_this_field).
		// We identify "the ref inside C.use()" by checking it's not
		// the field decl of Unrelated — Decls are separate from Refs,
		// so simply scan all refs: none should be "inherited_field".
		if ref.Binding.Reason == "inherited_field" {
			t.Errorf("x falsely resolved as inherited_field when supertype Missing is not in file")
		}
	}
	// Positive check: the x ref should end up unresolved.
	foundUnresolved := false
	for _, ref := range r.Refs {
		if ref.Name != "x" {
			continue
		}
		if ref.Binding.Kind == scope.BindUnresolved {
			foundUnresolved = true
			break
		}
	}
	if !foundUnresolved {
		t.Errorf("expected x ref inside C.use() to be unresolved; refs:\n%s", dumpRefs(r, "x"))
	}
}

func TestParse_InheritedFieldOneLevelOnly(t *testing.T) {
	// Multi-level chain: Grand -> Parent -> Child. `x` lives on Grand.
	// v1 punts on multi-level: the ref inside Child.use() should NOT
	// resolve to Grand.x.
	src := []byte(`class Grand {
    int x;
}
class Parent extends Grand {
}
class Child extends Parent {
    int use() { return x; }
}
`)
	r := Parse("Inh.java", src)

	gx := findFieldDeclInClass(r, "Grand", "x")
	if gx == nil {
		t.Fatalf("expected Grand.x field decl; decls=%v", declNames(r))
	}

	for _, ref := range r.Refs {
		if ref.Name != "x" {
			continue
		}
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == gx.ID {
			t.Errorf("x ref falsely resolved to Grand.x across two inheritance levels (v1 punt); reason=%s", ref.Binding.Reason)
		}
	}
}

func dumpRefs(r *scope.Result, name string) string {
	var lines []string
	for _, ref := range r.Refs {
		if ref.Name != name {
			continue
		}
		lines = append(lines, "  "+ref.Name+
			" scope="+itoa(int(ref.Scope))+
			" reason="+ref.Binding.Reason+
			" kind="+bindingKindName(ref.Binding.Kind))
	}
	return strings.Join(lines, "\n")
}

func bindingKindName(k scope.BindingKind) string {
	switch k {
	case scope.BindResolved:
		return "resolved"
	case scope.BindAmbiguous:
		return "ambiguous"
	case scope.BindProbable:
		return "probable"
	case scope.BindUnresolved:
		return "unresolved"
	}
	return "unknown"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
