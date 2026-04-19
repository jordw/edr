package python

import (
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

func TestParse_TopLevelDef(t *testing.T) {
	src := []byte(`def greet(name):
    return "hi " + name
`)
	r := Parse("a.py", src)
	if findDecl(r, "greet") == nil {
		t.Fatalf("function greet missing; decls=%v", declNames(r))
	}
	// Param name should be a KindParam decl inside greet's scope.
	nameDecl := findDecl(r, "name")
	if nameDecl == nil || nameDecl.Kind != scope.KindParam {
		t.Fatalf("param name missing; decls=%v", declNames(r))
	}
	// Ref to name in body should resolve to the param.
	refs := refsNamed(r, "name")
	if len(refs) == 0 || refs[0].Binding.Kind != scope.BindResolved ||
		refs[0].Binding.Decl != nameDecl.ID {
		t.Errorf("name ref did not bind to param: %+v", refs)
	}
}

func TestParse_Class(t *testing.T) {
	src := []byte(`class Point:
    def __init__(self, x, y):
        self.x = x
        self.y = y
`)
	r := Parse("a.py", src)
	if findDecl(r, "Point") == nil {
		t.Fatal("class Point missing")
	}
	if findDecl(r, "__init__") == nil {
		t.Fatalf("method __init__ missing; decls=%v", declNames(r))
	}
	// Params self, x, y in the method scope.
	for _, n := range []string{"self", "x", "y"} {
		if findDecl(r, n) == nil {
			t.Errorf("param %q missing", n)
		}
	}
}

func TestParse_Import(t *testing.T) {
	src := []byte(`import os
import sys as s
from typing import List, Dict as D

def f():
    return os.getcwd()
`)
	r := Parse("a.py", src)
	for _, n := range []string{"os", "s", "List", "D", "f"} {
		d := findDecl(r, n)
		if d == nil {
			t.Errorf("decl %q missing; decls=%v", n, declNames(r))
			continue
		}
		if n != "f" && d.Kind != scope.KindImport {
			t.Errorf("%q kind = %v, want import", n, d.Kind)
		}
	}
	// os.getcwd: os resolves, getcwd is property_access.
	osRefs := refsNamed(r, "os")
	if len(osRefs) == 0 || osRefs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("os ref not resolved: %+v", osRefs)
	}
}

func TestParse_Assignment(t *testing.T) {
	src := []byte(`x = 1
y = 2
z = x + y
`)
	r := Parse("a.py", src)
	for _, n := range []string{"x", "y", "z"} {
		if findDecl(r, n) == nil {
			t.Errorf("decl %q missing; decls=%v", n, declNames(r))
		}
	}
	// z = x + y — x, y refs should resolve.
	xRefs := refsNamed(r, "x")
	if len(xRefs) == 0 || xRefs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("x ref unresolved: %+v", xRefs)
	}
}

func TestParse_TupleAssignment(t *testing.T) {
	src := []byte(`a, b = 1, 2
use_b = b
`)
	r := Parse("a.py", src)
	for _, n := range []string{"a", "b"} {
		if findDecl(r, n) == nil {
			t.Errorf("tuple-assign decl %q missing; decls=%v", n, declNames(r))
		}
	}
	// Ref to b on line 2 should resolve to the LHS decl.
	bRefs := refsNamed(r, "b")
	if len(bRefs) == 0 {
		t.Fatalf("expected a ref to 'b'; got none")
	}
	resolved := false
	for _, ref := range bRefs {
		if ref.Binding.Kind == scope.BindResolved {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("ref to 'b' did not resolve to tuple-LHS decl")
	}
}

func TestParse_ParenthesizedTupleAssignment(t *testing.T) {
	src := []byte(`(a, b) = point
use_a = a
use_b = b
`)
	r := Parse("a.py", src)
	for _, n := range []string{"a", "b"} {
		if findDecl(r, n) == nil {
			t.Errorf("paren tuple-assign decl %q missing; decls=%v", n, declNames(r))
		}
	}
	// point should be a ref, not a decl.
	if findDecl(r, "point") != nil {
		t.Errorf("RHS 'point' should not be a decl")
	}
}

func TestParse_StarredTupleAssignment(t *testing.T) {
	src := []byte(`a, *rest = xs
`)
	r := Parse("a.py", src)
	for _, n := range []string{"a", "rest"} {
		if findDecl(r, n) == nil {
			t.Errorf("starred tuple-assign decl %q missing; decls=%v", n, declNames(r))
		}
	}
	if findDecl(r, "xs") != nil {
		t.Errorf("RHS 'xs' should not be a decl")
	}
}

func TestParse_NestedDestructuring(t *testing.T) {
	src := []byte(`(a, (b, c)) = point
`)
	r := Parse("a.py", src)
	for _, n := range []string{"a", "b", "c"} {
		if findDecl(r, n) == nil {
			t.Errorf("nested destructuring decl %q missing; decls=%v", n, declNames(r))
		}
	}
}

func TestParse_ForLoop(t *testing.T) {
	src := []byte(`items = [1, 2, 3]
for item in items:
    print(item)
`)
	r := Parse("a.py", src)
	if findDecl(r, "item") == nil {
		t.Errorf("for-loop var item missing; decls=%v", declNames(r))
	}
	// item inside body should resolve.
	itemRefs := refsNamed(r, "item")
	resolved := false
	for _, ref := range itemRefs {
		if ref.Binding.Kind == scope.BindResolved {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("item ref did not resolve to for-loop var")
	}
}

func TestParse_ForLoopMultiTarget(t *testing.T) {
	src := []byte(`for a, b in pairs:
    print(a)
    print(b)
`)
	r := Parse("a.py", src)
	for _, n := range []string{"a", "b"} {
		if findDecl(r, n) == nil {
			t.Errorf("for-loop multi-target decl %q missing; decls=%v", n, declNames(r))
		}
	}
	// Both a and b refs inside the body should resolve.
	for _, n := range []string{"a", "b"} {
		refs := refsNamed(r, n)
		if len(refs) == 0 {
			t.Errorf("expected ref to %q; got none", n)
			continue
		}
		resolved := false
		for _, ref := range refs {
			if ref.Binding.Kind == scope.BindResolved {
				resolved = true
				break
			}
		}
		if !resolved {
			t.Errorf("ref to %q did not resolve to for-loop var", n)
		}
	}
}

func TestParse_ClassTransparentForLEGB(t *testing.T) {
	// In Python, methods in a class body DON'T see class-scope names.
	// `x` inside the method is NOT resolved to Foo.x — it would need to
	// be `self.x`. Our resolver models this by skipping class scopes.
	src := []byte(`module_level = 1

class Foo:
    class_attr = 2

    def method(self):
        return module_level  # should resolve to file scope
`)
	r := Parse("a.py", src)
	// module_level ref inside method should resolve to the top-level decl.
	refs := refsNamed(r, "module_level")
	if len(refs) == 0 {
		t.Fatal("no refs to module_level")
	}
	resolved := false
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("module_level ref inside method should resolve to module scope")
	}
}

func TestParse_PropertyAccess(t *testing.T) {
	src := []byte(`obj = something()
x = obj.attr
y = obj.method().chain
`)
	r := Parse("a.py", src)
	for _, name := range []string{"attr", "method", "chain"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("property-access %q missing", name)
			continue
		}
		if refs[0].Binding.Kind != scope.BindProbable ||
			refs[0].Binding.Reason != "property_access" {
			t.Errorf("%q should be BindProbable/property_access, got %+v",
				name, refs[0].Binding)
		}
	}
}

func TestParse_Builtins(t *testing.T) {
	src := []byte(`def f(xs):
    return len(xs)
`)
	r := Parse("a.py", src)
	lenRefs := refsNamed(r, "len")
	if len(lenRefs) == 0 {
		t.Fatal("no ref to len")
	}
	if lenRefs[0].Binding.Reason != "builtin" {
		t.Errorf("len should resolve to builtin, got %+v", lenRefs[0].Binding)
	}
}

func TestParse_Decorator(t *testing.T) {
	src := []byte(`def my_decorator(fn):
    return fn

@my_decorator
def hello():
    pass
`)
	r := Parse("a.py", src)
	if findDecl(r, "hello") == nil {
		t.Errorf("hello missing")
	}
	// my_decorator is referenced by the @ decorator and should resolve.
	refs := refsNamed(r, "my_decorator")
	if len(refs) == 0 {
		t.Fatal("no ref to my_decorator")
	}
	if refs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("decorator ref unresolved: %+v", refs[0].Binding)
	}
}

func TestParse_NestedDef(t *testing.T) {
	src := []byte(`def outer():
    x = 1
    def inner():
        return x
    return inner
`)
	r := Parse("a.py", src)
	if findDecl(r, "outer") == nil || findDecl(r, "inner") == nil {
		t.Fatalf("nested defs missing; decls=%v", declNames(r))
	}
	// x inside inner's body should resolve via enclosing-scope walk.
	xRefs := refsNamed(r, "x")
	// At least one ref should resolve (the `return x` inside inner).
	resolved := false
	for _, ref := range xRefs {
		if ref.Binding.Kind == scope.BindResolved {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("x in inner should resolve via enclosing scope")
	}
}

// TestParse_Comprehension_ShadowsOuter verifies that a list comprehension
// introduces its own scope: the loop variable `x` inside the comprehension
// should bind to the comprehension-local decl, NOT to a same-named file-
// scope var.
func TestParse_Comprehension_ShadowsOuter(t *testing.T) {
	src := []byte(`x = 42
result = [x*2 for x in range(10)]
print(x)
`)
	r := Parse("a.py", src)

	// There should be two decls named "x": the file-scope assignment and
	// the comprehension-local for-var.
	var fileX, compX *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name != "x" {
			continue
		}
		if fileX == nil {
			fileX = d
		} else {
			compX = d
		}
	}
	if fileX == nil || compX == nil {
		t.Fatalf("expected two x decls (file + comprehension); decls=%v", declNames(r))
	}
	if fileX.Scope == compX.Scope {
		t.Fatalf("comprehension x should live in its own scope, got same scope %d for both", fileX.Scope)
	}

	// The x ref inside the comprehension's expression (the first `x` in
	// `x*2`) should resolve to the comprehension-local decl.
	// The x ref in `print(x)` should resolve to the file-scope decl.
	xRefs := refsNamed(r, "x")
	if len(xRefs) < 2 {
		t.Fatalf("expected at least 2 refs to x, got %d", len(xRefs))
	}
	// Find the ref inside the comprehension (between '[' and ']').
	openBr := -1
	closeBr := -1
	for i, b := range src {
		if b == '[' {
			openBr = i
		}
		if b == ']' {
			closeBr = i
			break
		}
	}
	if openBr < 0 || closeBr < 0 {
		t.Fatal("brackets missing from src")
	}
	var innerResolvedTo scope.DeclID
	var outerResolvedTo scope.DeclID
	for _, ref := range xRefs {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("x ref at %d unresolved: %+v", ref.Span.StartByte, ref.Binding)
			continue
		}
		if int(ref.Span.StartByte) > openBr && int(ref.Span.StartByte) < closeBr {
			innerResolvedTo = ref.Binding.Decl
		} else if int(ref.Span.StartByte) > closeBr {
			outerResolvedTo = ref.Binding.Decl
		}
	}
	if innerResolvedTo == 0 {
		t.Fatal("no inner-comprehension x ref found or resolved")
	}
	if outerResolvedTo == 0 {
		t.Fatal("no outer x ref (print(x)) found or resolved")
	}
	if innerResolvedTo != compX.ID {
		t.Errorf("inner x resolved to %d, want comprehension-local %d", innerResolvedTo, compX.ID)
	}
	if outerResolvedTo != fileX.ID {
		t.Errorf("outer x resolved to %d, want file-scope %d", outerResolvedTo, fileX.ID)
	}
}

// TestParse_Comprehension_Filter verifies that `if` filter clauses inside
// comprehensions see the comprehension-local for-var.
func TestParse_Comprehension_Filter(t *testing.T) {
	src := []byte(`xs = [1, 2, 3]
result = [x for x in xs if x > 0]
`)
	r := Parse("a.py", src)

	// Find the comprehension's for-var decl.
	var compX *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name == "x" {
			compX = d
			break
		}
	}
	if compX == nil {
		t.Fatalf("comprehension x decl missing; decls=%v", declNames(r))
	}

	// All three x refs (expression, for-var position is a decl not a ref,
	// and filter) should resolve to compX. Actually the for-var `x` is a
	// decl, so we have two refs: the expression `x` and the filter `x`.
	refs := refsNamed(r, "x")
	if len(refs) < 2 {
		t.Fatalf("expected at least 2 refs to x, got %d", len(refs))
	}
	for _, ref := range refs {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("x ref at %d unresolved: %+v", ref.Span.StartByte, ref.Binding)
			continue
		}
		if ref.Binding.Decl != compX.ID {
			t.Errorf("x ref at %d resolved to %d, want comprehension-local %d",
				ref.Span.StartByte, ref.Binding.Decl, compX.ID)
		}
	}
}

// TestParse_FullSpan_ScopeOwningDecls asserts that def and class decls
// populate FullSpan from the keyword through the end of the suite.
func TestParse_FullSpan_ScopeOwningDecls(t *testing.T) {
	src := []byte(`def greet(name):
    return "hi " + name

class Box:
    def __init__(self, value):
        self.value = value

    def unwrap(self):
        return self.value
`)
	r := Parse("a.py", src)

	greet := findDecl(r, "greet")
	if greet == nil {
		t.Fatal("greet missing")
	}
	if greet.FullSpan.StartByte >= greet.Span.StartByte {
		t.Errorf("greet: FullSpan.StartByte=%d should cover def keyword before Span.StartByte=%d",
			greet.FullSpan.StartByte, greet.Span.StartByte)
	}
	if greet.FullSpan.EndByte <= greet.Span.EndByte {
		t.Errorf("greet: FullSpan.EndByte=%d should cover body past Span.EndByte=%d",
			greet.FullSpan.EndByte, greet.Span.EndByte)
	}
	if got := string(src[greet.FullSpan.StartByte:greet.FullSpan.StartByte+3]); got != "def" {
		t.Errorf("greet: FullSpan starts with %q, want %q", got, "def")
	}

	box := findDecl(r, "Box")
	if box == nil {
		t.Fatal("Box missing")
	}
	if box.FullSpan.StartByte >= box.Span.StartByte {
		t.Errorf("Box: FullSpan.StartByte=%d should cover class keyword before Span.StartByte=%d",
			box.FullSpan.StartByte, box.Span.StartByte)
	}
	if box.FullSpan.EndByte <= box.Span.EndByte {
		t.Errorf("Box: FullSpan.EndByte=%d should cover body past Span.EndByte=%d",
			box.FullSpan.EndByte, box.Span.EndByte)
	}
	if got := string(src[box.FullSpan.StartByte:box.FullSpan.StartByte+5]); got != "class" {
		t.Errorf("Box: FullSpan starts with %q, want %q", got, "class")
	}
}
