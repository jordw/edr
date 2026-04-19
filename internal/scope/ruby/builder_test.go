package ruby

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

func findDeclOfKind(r *scope.Result, name string, kind scope.DeclKind) *scope.Decl {
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

// TestParse_ClassWithMethod covers the core requirement:
// `class Foo; def bar(x); x * 2; end; end`.
func TestParse_ClassWithMethod(t *testing.T) {
	src := []byte(`class Foo
  def bar(x)
    x * 2
  end
end
`)
	r := Parse("a.rb", src)
	foo := findDecl(r, "Foo")
	if foo == nil {
		t.Fatalf("class Foo missing; decls=%v", declNames(r))
	}
	if foo.Kind != scope.KindClass {
		t.Errorf("Foo kind = %v, want %v", foo.Kind, scope.KindClass)
	}
	bar := findDecl(r, "bar")
	if bar == nil {
		t.Fatalf("method bar missing; decls=%v", declNames(r))
	}
	if bar.Kind != scope.KindMethod {
		t.Errorf("bar kind = %v, want method", bar.Kind)
	}
	if bar.Namespace != scope.NSField {
		t.Errorf("bar namespace = %v, want %v", bar.Namespace, scope.NSField)
	}
	xDecl := findDeclOfKind(r, "x", scope.KindParam)
	if xDecl == nil {
		t.Fatalf("param x missing; decls=%v", declNames(r))
	}
	// `x * 2` should reference x.
	refs := refsNamed(r, "x")
	resolved := false
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == xDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("x body ref did not bind to param x; refs=%+v", refs)
	}
}

// TestParse_SelfMethod handles `def self.foo(x)` class-method variant.
func TestParse_SelfMethod(t *testing.T) {
	src := []byte(`class Foo
  def self.bar(x)
    x
  end
end
`)
	r := Parse("a.rb", src)
	if findDecl(r, "bar") == nil {
		t.Fatalf("class-method bar missing; decls=%v", declNames(r))
	}
	if findDeclOfKind(r, "x", scope.KindParam) == nil {
		t.Errorf("param x missing")
	}
}

// TestParse_ClassInheritance covers `class Foo < Bar` — Bar should be a ref.
func TestParse_ClassInheritance(t *testing.T) {
	src := []byte(`class Child < Parent
end
`)
	r := Parse("a.rb", src)
	if findDecl(r, "Child") == nil {
		t.Fatal("Child class missing")
	}
	parentRefs := refsNamed(r, "Parent")
	if len(parentRefs) == 0 {
		t.Fatalf("Parent ref missing; refs=%+v", r.Refs)
	}
}

// TestParse_Module covers `module Foo` — ScopeNamespace.
func TestParse_Module(t *testing.T) {
	src := []byte(`module Foo
  def bar
  end
end
`)
	r := Parse("a.rb", src)
	foo := findDecl(r, "Foo")
	if foo == nil {
		t.Fatalf("module Foo missing; decls=%v", declNames(r))
	}
	if foo.Kind != scope.KindNamespace {
		t.Errorf("Foo kind = %v, want namespace", foo.Kind)
	}
	// Check that the scope created was ScopeNamespace.
	if int(foo.Scope) >= 0 {
		// Its body (bar) lives in a ScopeNamespace child; find it.
		bar := findDecl(r, "bar")
		if bar == nil {
			t.Fatal("bar missing")
		}
		barScope := r.Scopes[int(bar.Scope)-1]
		if barScope.Kind != scope.ScopeNamespace && barScope.Kind != scope.ScopeFunction {
			// bar itself is in the function scope (its own body); its
			// parent is the module scope.
			parent := r.Scopes[int(barScope.Parent)-1]
			if parent.Kind != scope.ScopeNamespace {
				t.Errorf("module scope kind = %v, want namespace", parent.Kind)
			}
		}
	}
}

// TestParse_AttrAccessor covers `attr_accessor :name, :age` inside a class.
func TestParse_AttrAccessor(t *testing.T) {
	src := []byte(`class Foo
  attr_accessor :name, :age
  attr_reader :ro
  attr_writer :wo
end
`)
	r := Parse("a.rb", src)
	for _, n := range []string{"name", "age", "ro", "wo"} {
		d := findDeclOfKind(r, n, scope.KindField)
		if d == nil {
			t.Errorf("field %q missing; decls=%v", n, declNames(r))
			continue
		}
		if d.Namespace != scope.NSField {
			t.Errorf("%q namespace = %v, want %v", n, d.Namespace, scope.NSField)
		}
	}
}

// TestParse_InstanceVar covers `@count = 0; @count` — single decl in
// class scope and refs bind to it.
func TestParse_InstanceVar(t *testing.T) {
	src := []byte(`class Foo
  def init
    @count = 0
    @count
  end
end
`)
	r := Parse("a.rb", src)
	var count *scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "@count" {
			count = &r.Decls[i]
			break
		}
	}
	if count == nil {
		t.Fatalf("@count decl missing; decls=%v", declNames(r))
	}
	if count.Kind != scope.KindField || count.Namespace != scope.NSField {
		t.Errorf("@count kind=%v ns=%v, want field/NSField", count.Kind, count.Namespace)
	}
	// Ensure exactly one decl for @count (first assignment creates it).
	n := 0
	for i := range r.Decls {
		if r.Decls[i].Name == "@count" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("@count emitted %d times, want 1", n)
	}
	// Refs to @count should bind to the decl.
	refs := refsNamed(r, "@count")
	if len(refs) < 2 {
		t.Errorf("expected at least 2 refs to @count, got %d", len(refs))
	}
	for _, ref := range refs {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("@count ref at %d not resolved: %+v", ref.Span.StartByte, ref.Binding)
		}
	}
}

// TestParse_DoBlock covers `[1,2,3].each do |x| puts x end`.
func TestParse_DoBlock(t *testing.T) {
	src := []byte(`[1, 2, 3].each do |x|
  puts x
end
`)
	r := Parse("a.rb", src)
	x := findDeclOfKind(r, "x", scope.KindParam)
	if x == nil {
		t.Fatalf("block param x missing; decls=%v", declNames(r))
	}
	// The block scope for x should be ScopeBlock.
	xScope := r.Scopes[int(x.Scope)-1]
	if xScope.Kind != scope.ScopeBlock {
		t.Errorf("x's scope kind = %v, want block", xScope.Kind)
	}
	// puts x — x should resolve to the param.
	refs := refsNamed(r, "x")
	resolved := false
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == x.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("x inside block should resolve to param; refs=%+v", refs)
	}
}

// TestParse_BraceBlock covers `[1,2,3].map { |x| x * 2 }`.
func TestParse_BraceBlock(t *testing.T) {
	src := []byte(`[1, 2, 3].map { |x| x * 2 }
`)
	r := Parse("a.rb", src)
	x := findDeclOfKind(r, "x", scope.KindParam)
	if x == nil {
		t.Fatalf("brace-block param x missing; decls=%v", declNames(r))
	}
	xScope := r.Scopes[int(x.Scope)-1]
	if xScope.Kind != scope.ScopeBlock {
		t.Errorf("brace-block scope kind = %v, want block", xScope.Kind)
	}
	refs := refsNamed(r, "x")
	resolved := false
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == x.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("x inside brace block should resolve to param; refs=%+v", refs)
	}
}

// TestParse_Require covers `require 'json'`.
func TestParse_Require(t *testing.T) {
	src := []byte(`require 'json'
require_relative "./foo"
`)
	r := Parse("a.rb", src)
	for _, want := range []string{"json", "./foo"} {
		d := findDeclOfKind(r, want, scope.KindImport)
		if d == nil {
			t.Errorf("import %q missing; decls=%v", want, declNames(r))
		}
	}
}

// TestParse_SameClassMethodCall: `def bar; baz; end` inside class Foo —
// baz should emit a ref. The binding is probable (method_call).
func TestParse_SameClassMethodCall(t *testing.T) {
	src := []byte(`class Foo
  def bar
    baz
  end
end
`)
	r := Parse("a.rb", src)
	refs := refsNamed(r, "baz")
	if len(refs) == 0 {
		t.Fatalf("no ref to baz; refs=%+v", r.Refs)
	}
	// Either probable (method_call) or unresolved is acceptable.
	ref := refs[0]
	if ref.Binding.Kind == scope.BindResolved {
		t.Errorf("baz should not resolve to anything (no decl); got resolved decl=%d", ref.Binding.Decl)
	}
}

// TestParse_LocalVarShadowing: outer `x = 1` is at file scope; inner
// `def foo; x = 2; x; end` has its own x in the method scope.
func TestParse_LocalVarShadowing(t *testing.T) {
	src := []byte(`x = 1
def foo
  x = 2
  x
end
`)
	r := Parse("a.rb", src)
	// Collect all x decls.
	var xs []*scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "x" {
			xs = append(xs, &r.Decls[i])
		}
	}
	if len(xs) < 2 {
		t.Fatalf("expected at least 2 x decls, got %d; decls=%v", len(xs), declNames(r))
	}
	// The two x decls should be in different scopes.
	if xs[0].Scope == xs[1].Scope {
		t.Errorf("two x decls should be in different scopes, both in %d", xs[0].Scope)
	}
	// Find the ref that reads x inside foo (the `x` on its own line in the body).
	xRefs := refsNamed(r, "x")
	if len(xRefs) == 0 {
		t.Fatalf("no refs to x")
	}
	// The last x ref should bind to an x in the inner scope.
	last := xRefs[len(xRefs)-1]
	if last.Binding.Kind != scope.BindResolved {
		t.Errorf("inner x ref unresolved: %+v", last.Binding)
	}
}

// TestParse_StringInterpolation should not let `"#{x}"` break scope scanning.
func TestParse_StringInterpolation(t *testing.T) {
	src := []byte(`def greet(name)
  "hi #{name} from #{Time.now}"
end
`)
	r := Parse("a.rb", src)
	// Scope must have closed (file scope is the only one open).
	if findDecl(r, "greet") == nil {
		t.Fatalf("greet missing; decls=%v", declNames(r))
	}
	if findDeclOfKind(r, "name", scope.KindParam) == nil {
		t.Error("param name missing")
	}
	// name inside the interpolation should resolve.
	refs := refsNamed(r, "name")
	found := false
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("name ref inside interpolation did not resolve; refs=%+v", refs)
	}
}

// TestParse_FullSpan_ScopeOwningDecls covers def/class/module FullSpan
// covering keyword through `end`.
func TestParse_FullSpan_ScopeOwningDecls(t *testing.T) {
	src := []byte(`class Foo
  def bar
    1
  end
end

module Baz
  def qux
  end
end
`)
	r := Parse("a.rb", src)

	check := func(name, wantPrefix string) {
		t.Helper()
		d := findDecl(r, name)
		if d == nil {
			t.Fatalf("decl %q missing", name)
		}
		if d.FullSpan.StartByte >= d.Span.StartByte {
			t.Errorf("%s: FullSpan.StartByte=%d should cover keyword before Span.StartByte=%d",
				name, d.FullSpan.StartByte, d.Span.StartByte)
		}
		if d.FullSpan.EndByte <= d.Span.EndByte {
			t.Errorf("%s: FullSpan.EndByte=%d should cover body past Span.EndByte=%d",
				name, d.FullSpan.EndByte, d.Span.EndByte)
		}
		end := int(d.FullSpan.EndByte)
		if end > len(src) {
			end = len(src)
		}
		got := string(src[d.FullSpan.StartByte:end])
		if !strings.HasPrefix(got, wantPrefix) {
			t.Errorf("%s: FullSpan content starts %q, want prefix %q", name, got, wantPrefix)
		}
		trimmed := strings.TrimRight(got, "\r\n \t")
		if !strings.HasSuffix(trimmed, "end") {
			t.Errorf("%s: FullSpan content should end with 'end'; got %q", name, got)
		}
	}
	check("Foo", "class Foo")
	check("bar", "def bar")
	check("Baz", "module Baz")
	check("qux", "def qux")
}

// TestParse_NestedIfInsideMethod: scope-wise if/unless/etc. open scopes
// that close on `end`. Make sure they don't accidentally close the
// enclosing method scope early.
func TestParse_NestedIfInsideMethod(t *testing.T) {
	src := []byte(`def foo(x)
  if x > 0
    y = 1
  else
    y = 2
  end
  y
end
`)
	r := Parse("a.rb", src)
	if findDecl(r, "foo") == nil {
		t.Fatal("foo missing")
	}
	if findDeclOfKind(r, "x", scope.KindParam) == nil {
		t.Error("param x missing")
	}
}

// TestParse_NoRefScopeZero ensures every ref has a non-zero scope (which
// implies the scope stack didn't underflow at emission time).
func TestParse_NoRefScopeZero(t *testing.T) {
	src := []byte(`class A
  def b
    c = 1
    c + 2
  end
end
`)
	r := Parse("a.rb", src)
	for _, ref := range r.Refs {
		if ref.Scope == 0 {
			t.Errorf("ref %q at %d has scope=0", ref.Name, ref.Span.StartByte)
		}
	}
}

// TestParse_Builtins: Kernel methods called as bare idents resolve as
// builtins, not as BindUnresolved method_call. Core classes referenced
// by name do the same.
func TestParse_Builtins(t *testing.T) {
	src := []byte(`def greet(name)
  puts "hello #{name}"
  raise ArgumentError if name.nil?
end
`)
	r := Parse("a.rb", src)
	for _, name := range []string{"puts", "ArgumentError"} {
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

// TestParse_ClassReopeningMerging: Ruby permits reopening a class in
// the same file; both `class Foo` blocks should share a DeclID.
func TestParse_ClassReopeningMerging(t *testing.T) {
	src := []byte(`class Foo
  def a; end
end

class Foo
  def b; end
end
`)
	r := Parse("a.rb", src)
	var foos []*scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "Foo" && r.Decls[i].Kind == scope.KindClass {
			foos = append(foos, &r.Decls[i])
		}
	}
	if len(foos) < 2 {
		t.Fatalf("expected >=2 Foo class decls; got %d; decls=%v", len(foos), declNames(r))
	}
	if foos[0].ID != foos[1].ID {
		t.Errorf("reopened class Foo has distinct IDs %d vs %d (should merge)",
			foos[0].ID, foos[1].ID)
	}
}

// TestParse_AliasBareword checks that 'alias new_name old_name' emits
// new_name as a method decl in the enclosing class scope.
func TestParse_AliasBareword(t *testing.T) {
	src := []byte("class Foo\n  def original_name; end\n  alias new_name original_name\nend\n")
	r := Parse("a.rb", src)
	d := findDeclOfKind(r, "new_name", scope.KindMethod)
	if d == nil {
		t.Fatalf("alias new_name missing as method decl; decls=%v", declNames(r))
	}
	if d.Namespace != scope.NSField {
		t.Errorf("new_name namespace = %v, want %v", d.Namespace, scope.NSField)
	}
	// old_name should still be present (def) and a ref to original_name
	// should exist.
	if findDeclOfKind(r, "original_name", scope.KindMethod) == nil {
		t.Fatalf("original_name method missing")
	}
	if len(refsNamed(r, "original_name")) == 0 {
		t.Errorf("expected a ref to original_name from alias; refs=%+v", r.Refs)
	}
}

// TestParse_AliasMethodSymbols checks 'alias_method :new_name, :old_name'.
func TestParse_AliasMethodSymbols(t *testing.T) {
	src := []byte("class Foo\n  def original_name; end\n  alias_method :another_name, :original_name\nend\n")
	r := Parse("a.rb", src)
	d := findDeclOfKind(r, "another_name", scope.KindMethod)
	if d == nil {
		t.Fatalf("alias_method another_name missing as method decl; decls=%v", declNames(r))
	}
	if d.Namespace != scope.NSField {
		t.Errorf("another_name namespace = %v, want %v", d.Namespace, scope.NSField)
	}
	if len(refsNamed(r, "original_name")) == 0 {
		t.Errorf("expected a ref to original_name from alias_method; refs=%+v", r.Refs)
	}
}

// TestParse_AliasMethodString covers the string-literal form
// 'alias_method :new_name, "old_name"'.
func TestParse_AliasMethodString(t *testing.T) {
	src := []byte("class Foo\n  def original_name; end\n  alias_method :sn, \"original_name\"\nend\n")
	r := Parse("a.rb", src)
	if findDeclOfKind(r, "sn", scope.KindMethod) == nil {
		t.Fatalf("alias_method sn missing as method decl; decls=%v", declNames(r))
	}
}

// TestParse_AliasNotBreaksDownstream makes sure aliasing doesn't prevent
// later 'def's in the same class from being picked up.
func TestParse_AliasNotBreaksDownstream(t *testing.T) {
	src := []byte("class Foo\n  def a; end\n  alias b a\n  def c; end\nend\n")
	r := Parse("a.rb", src)
	for _, n := range []string{"a", "b", "c"} {
		if findDeclOfKind(r, n, scope.KindMethod) == nil {
			t.Errorf("method %q missing; decls=%v", n, declNames(r))
		}
	}
}

