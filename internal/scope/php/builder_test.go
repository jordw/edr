package php

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

// TestParse_TopLevelFunction covers `<?php function hello() { return "hi"; } ?>`.
func TestParse_TopLevelFunction(t *testing.T) {
	src := []byte(`<?php function hello() { return "hi"; } ?>`)
	r := Parse("a.php", src)
	hello := findDecl(r, "hello")
	if hello == nil {
		t.Fatalf("function hello missing; decls=%v", declNames(r))
	}
	if hello.Kind != scope.KindFunction {
		t.Errorf("hello kind = %v, want function", hello.Kind)
	}
	// FullSpan should cover the function (the "function" keyword through
	// the closing brace).
	if hello.FullSpan.StartByte >= hello.FullSpan.EndByte {
		t.Errorf("hello FullSpan invalid: %+v", hello.FullSpan)
	}
	// Check that the start byte points at the `function` keyword.
	if !strings.HasPrefix(string(src[hello.FullSpan.StartByte:]), "function") {
		t.Errorf("FullSpan should begin with 'function'; got %q", string(src[hello.FullSpan.StartByte:hello.FullSpan.StartByte+8]))
	}
}

// TestParse_ClassWithMethod covers class, method, $this->field, fields.
func TestParse_ClassWithMethod(t *testing.T) {
	src := []byte(`<?php
class Foo {
    public int $x;
    public function bar() {
        return $this->x;
    }
}
?>`)
	r := Parse("a.php", src)
	foo := findDecl(r, "Foo")
	if foo == nil {
		t.Fatalf("class Foo missing; decls=%v", declNames(r))
	}
	if foo.Kind != scope.KindClass {
		t.Errorf("Foo kind = %v, want class", foo.Kind)
	}

	x := findDeclOfKind(r, "$x", scope.KindField)
	if x == nil {
		t.Fatalf("field $x missing; decls=%v", declNames(r))
	}
	if x.Namespace != scope.NSField {
		t.Errorf("$x namespace = %v, want NSField", x.Namespace)
	}

	bar := findDeclOfKind(r, "bar", scope.KindMethod)
	if bar == nil {
		t.Fatalf("method bar missing; decls=%v", declNames(r))
	}
	if bar.Namespace != scope.NSField {
		t.Errorf("bar namespace = %v, want NSField", bar.Namespace)
	}

	// `$this->x` should resolve via this_dot_field.
	var resolved bool
	for _, ref := range r.Refs {
		if ref.Name == "x" && ref.Binding.Reason == "this_dot_field" &&
			ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == x.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("$this->x did not resolve via this_dot_field; refs=%+v", r.Refs)
	}
}

// TestParse_TopLevelVar covers `$name = "world"; echo $name;`.
func TestParse_TopLevelVar(t *testing.T) {
	src := []byte(`<?php $name = "world"; echo $name; ?>`)
	r := Parse("a.php", src)
	n := findDeclOfKind(r, "$name", scope.KindVar)
	if n == nil {
		t.Fatalf("$name var decl missing; decls=%v", declNames(r))
	}
	// `echo $name` should resolve to the var.
	var resolved bool
	for _, ref := range r.Refs {
		if ref.Name == "$name" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == n.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("$name ref did not resolve to decl; refs=%+v", r.Refs)
	}
}

// TestParse_NamespaceStatement covers statement-form namespaces.
func TestParse_NamespaceStatement(t *testing.T) {
	src := []byte(`<?php namespace Foo\Bar; class Baz {} ?>`)
	r := Parse("a.php", src)
	baz := findDecl(r, "Baz")
	if baz == nil {
		t.Fatalf("Baz missing; decls=%v", declNames(r))
	}
	// Baz's enclosing scope should be a namespace.
	if int(baz.Scope) < 1 || int(baz.Scope) > len(r.Scopes) {
		t.Fatalf("Baz.Scope out of range: %d", baz.Scope)
	}
	sc := r.Scopes[int(baz.Scope)-1]
	if sc.Kind != scope.ScopeNamespace {
		t.Errorf("Baz enclosing scope kind = %v, want namespace", sc.Kind)
	}
	// Namespace decl itself.
	if findDeclOfKind(r, "Bar", scope.KindNamespace) == nil {
		t.Errorf("namespace Bar decl missing; decls=%v", declNames(r))
	}
}

// TestParse_Use covers `use Foo\Bar; new Bar();` — Bar is KindImport and
// the `new Bar()` ref resolves to it.
func TestParse_Use(t *testing.T) {
	src := []byte(`<?php use Foo\Bar; new Bar(); ?>`)
	r := Parse("a.php", src)
	bar := findDeclOfKind(r, "Bar", scope.KindImport)
	if bar == nil {
		t.Fatalf("use import Bar missing; decls=%v", declNames(r))
	}
	var resolved bool
	for _, ref := range r.Refs {
		if ref.Name == "Bar" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == bar.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("new Bar() ref did not resolve to import; refs=%+v", r.Refs)
	}
}

// TestParse_UseAs covers `use Foo\Bar as Baz;`.
func TestParse_UseAs(t *testing.T) {
	src := []byte(`<?php use Foo\Bar as Baz; new Baz(); ?>`)
	r := Parse("a.php", src)
	baz := findDeclOfKind(r, "Baz", scope.KindImport)
	if baz == nil {
		t.Fatalf("use-as import Baz missing; decls=%v", declNames(r))
	}
}

// TestParse_UseGrouped covers `use Foo\{A, B as C};`.
func TestParse_UseGrouped(t *testing.T) {
	src := []byte(`<?php use Foo\{A, B as C}; ?>`)
	r := Parse("a.php", src)
	if findDeclOfKind(r, "A", scope.KindImport) == nil {
		t.Errorf("grouped import A missing; decls=%v", declNames(r))
	}
	if findDeclOfKind(r, "C", scope.KindImport) == nil {
		t.Errorf("grouped import C (alias of B) missing; decls=%v", declNames(r))
	}
}

// TestParse_ArrowFunction covers `fn($x, $y) => $x + $y`.
func TestParse_ArrowFunction(t *testing.T) {
	src := []byte(`<?php $add = fn($x, $y) => $x + $y; ?>`)
	r := Parse("a.php", src)
	// $add at top level.
	if findDeclOfKind(r, "$add", scope.KindVar) == nil {
		t.Errorf("$add var missing; decls=%v", declNames(r))
	}
	// $x, $y as params.
	x := findDeclOfKind(r, "$x", scope.KindParam)
	if x == nil {
		t.Fatalf("param $x missing; decls=%v", declNames(r))
	}
	if findDeclOfKind(r, "$y", scope.KindParam) == nil {
		t.Errorf("param $y missing")
	}
	// The $x in the body should resolve to the param.
	var bodyResolved bool
	refs := refsNamed(r, "$x")
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == x.ID {
			bodyResolved = true
			break
		}
	}
	if !bodyResolved {
		t.Errorf("$x body ref did not resolve to arrow param; refs=%+v", refs)
	}
}

// TestParse_MethodCallViaThis covers `$this->foo()` resolving to a
// method decl.
func TestParse_MethodCallViaThis(t *testing.T) {
	src := []byte(`<?php
class Foo {
    public function foo() {}
    public function bar() {
        return $this->foo();
    }
}
?>`)
	r := Parse("a.php", src)
	foo := findDeclOfKind(r, "foo", scope.KindMethod)
	if foo == nil {
		t.Fatalf("method foo missing; decls=%v", declNames(r))
	}
	var resolved bool
	for _, ref := range r.Refs {
		if ref.Name == "foo" && ref.Binding.Reason == "this_dot_field" &&
			ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == foo.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("$this->foo() did not resolve to method decl; refs=%+v", r.Refs)
	}
}

// TestParse_ClassWithExtends covers superclass as a ref.
func TestParse_ClassWithExtends(t *testing.T) {
	src := []byte(`<?php class Child extends Parent1 {} ?>`)
	r := Parse("a.php", src)
	if findDeclOfKind(r, "Child", scope.KindClass) == nil {
		t.Fatalf("Child class missing")
	}
	parentRefs := refsNamed(r, "Parent1")
	if len(parentRefs) == 0 {
		t.Errorf("Parent1 ref missing; refs=%+v", r.Refs)
	}
}

// TestParse_Interface covers interface decl and method signatures.
func TestParse_Interface(t *testing.T) {
	src := []byte(`<?php
interface Greeter {
    public function hello(): string;
}
?>`)
	r := Parse("a.php", src)
	g := findDeclOfKind(r, "Greeter", scope.KindInterface)
	if g == nil {
		t.Fatalf("Greeter interface missing; decls=%v", declNames(r))
	}
}

// TestParse_Trait covers `trait Foo { ... }`.
func TestParse_Trait(t *testing.T) {
	src := []byte(`<?php trait HasName { public string $name; } ?>`)
	r := Parse("a.php", src)
	if findDecl(r, "HasName") == nil {
		t.Fatalf("trait HasName missing; decls=%v", declNames(r))
	}
}

// TestParse_Enum covers `enum Color { case Red; }`.
func TestParse_Enum(t *testing.T) {
	src := []byte(`<?php
enum Color {
    case Red;
    case Green;
}
?>`)
	r := Parse("a.php", src)
	if findDeclOfKind(r, "Color", scope.KindEnum) == nil {
		t.Fatalf("enum Color missing; decls=%v", declNames(r))
	}
}

// TestParse_AbstractFinalClass covers modifier keywords.
func TestParse_AbstractFinalClass(t *testing.T) {
	src := []byte(`<?php abstract class Foo {} final class Bar {} ?>`)
	r := Parse("a.php", src)
	if findDeclOfKind(r, "Foo", scope.KindClass) == nil {
		t.Fatalf("abstract class Foo missing; decls=%v", declNames(r))
	}
	if findDeclOfKind(r, "Bar", scope.KindClass) == nil {
		t.Fatalf("final class Bar missing; decls=%v", declNames(r))
	}
}

// TestParse_ClassConstant covers `const FOO = 1;` inside a class.
func TestParse_ClassConstant(t *testing.T) {
	src := []byte(`<?php
class Foo {
    const MAX = 10;
}
?>`)
	r := Parse("a.php", src)
	m := findDeclOfKind(r, "MAX", scope.KindConst)
	if m == nil {
		t.Fatalf("class const MAX missing; decls=%v", declNames(r))
	}
	if m.Namespace != scope.NSField {
		t.Errorf("class const MAX namespace = %v, want NSField", m.Namespace)
	}
}

// TestParse_TopLevelConst covers `const FOO = 1;` at top level.
func TestParse_TopLevelConst(t *testing.T) {
	src := []byte(`<?php const FOO = 1; ?>`)
	r := Parse("a.php", src)
	foo := findDeclOfKind(r, "FOO", scope.KindConst)
	if foo == nil {
		t.Fatalf("top-level const FOO missing; decls=%v", declNames(r))
	}
	if foo.Namespace != scope.NSValue {
		t.Errorf("top-level const FOO namespace = %v, want NSValue", foo.Namespace)
	}
}

// TestParse_AnonClosureWithUse covers `function($x) use ($captured) { }`.
func TestParse_AnonClosureWithUse(t *testing.T) {
	src := []byte(`<?php
$captured = 1;
$cb = function($x) use ($captured) { return $x + $captured; };
?>`)
	r := Parse("a.php", src)
	captured := findDeclOfKind(r, "$captured", scope.KindVar)
	if captured == nil {
		t.Fatalf("$captured var missing; decls=%v", declNames(r))
	}
	if findDeclOfKind(r, "$x", scope.KindParam) == nil {
		t.Errorf("$x param missing")
	}
}

// TestParse_StaticAccess covers `Foo::bar` as a property_access ref.
func TestParse_StaticAccess(t *testing.T) {
	src := []byte(`<?php Foo::bar(); ?>`)
	r := Parse("a.php", src)
	// Foo should be a ref.
	var sawFoo bool
	for _, ref := range r.Refs {
		if ref.Name == "Foo" {
			sawFoo = true
		}
	}
	if !sawFoo {
		t.Errorf("Foo ref missing; refs=%+v", r.Refs)
	}
	// bar should be a property_access ref.
	var sawBar bool
	for _, ref := range r.Refs {
		if ref.Name == "bar" && ref.Binding.Reason == "property_access" {
			sawBar = true
		}
	}
	if !sawBar {
		t.Errorf("bar property_access ref missing; refs=%+v", r.Refs)
	}
}

// TestParse_HtmlOutsideTags covers that HTML outside <?php tags is skipped.
func TestParse_HtmlOutsideTags(t *testing.T) {
	src := []byte(`<html>
<head><title>Hi</title></head>
<body><?php echo "hi"; ?></body>
</html>
`)
	r := Parse("a.php", src)
	// No decls expected; no panic.
	_ = r
}

// TestParse_Heredoc covers `<<<EOT ... EOT;` — body should be skipped.
func TestParse_Heredoc(t *testing.T) {
	src := []byte(`<?php
$x = <<<EOT
Hello $name
EOT;
?>`)
	r := Parse("a.php", src)
	if findDeclOfKind(r, "$x", scope.KindVar) == nil {
		t.Fatalf("$x var missing after heredoc; decls=%v", declNames(r))
	}
	// $name inside the heredoc should NOT emit a ref.
	if len(refsNamed(r, "$name")) != 0 {
		t.Errorf("$name inside heredoc should not emit a ref")
	}
}

// TestParse_Nowdoc covers `<<<'EOT' ... EOT;`.
func TestParse_Nowdoc(t *testing.T) {
	src := []byte(`<?php
$x = <<<'EOT'
literal $name here
EOT;
?>`)
	r := Parse("a.php", src)
	if findDeclOfKind(r, "$x", scope.KindVar) == nil {
		t.Fatalf("$x var missing after nowdoc; decls=%v", declNames(r))
	}
	if len(refsNamed(r, "$name")) != 0 {
		t.Errorf("$name inside nowdoc should not emit a ref")
	}
}

// TestParse_FunctionParams covers typed params including `?string $y`.
func TestParse_FunctionParams(t *testing.T) {
	src := []byte(`<?php function foo(int $x, ?string $y = "hi"): int { return $x; } ?>`)
	r := Parse("a.php", src)
	x := findDeclOfKind(r, "$x", scope.KindParam)
	if x == nil {
		t.Fatalf("$x param missing; decls=%v", declNames(r))
	}
	if findDeclOfKind(r, "$y", scope.KindParam) == nil {
		t.Errorf("$y param missing")
	}
	// Body `return $x` should bind to the param.
	var bodyResolved bool
	for _, ref := range r.Refs {
		if ref.Name == "$x" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == x.ID {
			bodyResolved = true
			break
		}
	}
	if !bodyResolved {
		t.Errorf("$x body ref did not resolve to param; refs=%+v", r.Refs)
	}
}

// TestParse_NoPanicOnMalformed ensures the parser doesn't panic on
// unbalanced or weird input.
func TestParse_NoPanicOnMalformed(t *testing.T) {
	samples := [][]byte{
		[]byte(`<?php`),
		[]byte(`<?php function`),
		[]byte(`<?php class Foo {`),
		[]byte(`<?php $ = 1;`),
		[]byte(`<?php "`),
		[]byte(`<?php <<<EOT`),
		[]byte(``),
	}
	for _, s := range samples {
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					t.Errorf("panic on %q: %v", string(s), rec)
				}
			}()
			_ = Parse("a.php", s)
		}()
	}
}

// TestParse_NoScopeZeroRefs ensures every ref has a non-zero scope.
func TestParse_NoScopeZeroRefs(t *testing.T) {
	src := []byte(`<?php
namespace App;
use Foo\Bar;
class Baz extends Bar {
    private int $count = 0;
    public function tick(): void {
        $this->count++;
    }
}
$b = new Baz();
$b->tick();
?>`)
	r := Parse("a.php", src)
	for _, ref := range r.Refs {
		if ref.Scope == 0 {
			t.Errorf("ref %q has scope=0 (stack underflow)", ref.Name)
		}
	}
}

// TestParse_FullSpanCoversFunctionBody ensures FullSpan extends through
// the closing brace of a function/class body.
func TestParse_FullSpanCoversFunctionBody(t *testing.T) {
	src := []byte(`<?php
class Foo {
    public function bar() {
        return 1;
    }
}
`)
	r := Parse("a.php", src)
	foo := findDecl(r, "Foo")
	if foo == nil {
		t.Fatal("Foo missing")
	}
	// FullSpan should include the closing } of Foo's body.
	if foo.FullSpan.EndByte == foo.Span.EndByte {
		t.Errorf("Foo FullSpan was never patched; got %+v", foo.FullSpan)
	}
	tail := string(src[foo.FullSpan.EndByte-1 : foo.FullSpan.EndByte])
	if tail != "}" {
		t.Errorf("Foo FullSpan should end at '}', got %q", tail)
	}
	bar := findDecl(r, "bar")
	if bar == nil {
		t.Fatal("bar missing")
	}
	if bar.FullSpan.EndByte == bar.Span.EndByte {
		t.Errorf("bar FullSpan was never patched; got %+v", bar.FullSpan)
	}
}

// TestParse_Builtins: core PHP functions + exception classes bind as
// builtins rather than BindUnresolved missing_import.
func TestParse_Builtins(t *testing.T) {
	src := []byte(`<?php
function greet($name) {
    if (empty($name)) {
        throw new InvalidArgumentException("blank");
    }
    return strlen($name);
}
`)
	r := Parse("a.php", src)
	for _, name := range []string{"strlen", "InvalidArgumentException"} {
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

// TestParse_InterfaceNSType: `interface Foo` emits a KindInterface decl
// in NSType only (no NSValue row) — interfaces are pure type names in
// PHP.
func TestParse_InterfaceNSType(t *testing.T) {
	src := []byte(`<?php
interface Foo {
    public function hello(): string;
}
?>`)
	r := Parse("a.php", src)
	var seen []scope.Namespace
	for i := range r.Decls {
		if r.Decls[i].Name == "Foo" && r.Decls[i].Kind == scope.KindInterface {
			seen = append(seen, r.Decls[i].Namespace)
		}
	}
	if len(seen) == 0 {
		t.Fatalf("no interface Foo decl found; decls=%v", declNames(r))
	}
	for _, ns := range seen {
		if ns != scope.NSType {
			t.Errorf("interface Foo namespace = %q, want NSType", ns)
		}
	}
	// There must not be an NSValue decl for interface Foo.
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name == "Foo" && d.Kind == scope.KindInterface && d.Namespace == scope.NSValue {
			t.Errorf("unexpected NSValue decl for interface Foo: %+v", d)
		}
	}
}

// TestParse_TraitNSType: `trait X` emits into NSType only. PHP's builder
// uses KindType for traits.
func TestParse_TraitNSType(t *testing.T) {
	src := []byte(`<?php trait HasName { public string $name; } ?>`)
	r := Parse("a.php", src)
	var trait *scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "HasName" && r.Decls[i].Kind == scope.KindType {
			trait = &r.Decls[i]
			break
		}
	}
	if trait == nil {
		t.Fatalf("trait HasName missing; decls=%v", declNames(r))
	}
	if trait.Namespace != scope.NSType {
		t.Errorf("trait HasName namespace = %q, want NSType", trait.Namespace)
	}
}

// TestParse_ClassDualResident: `class Foo` emits TWO decls (NSValue +
// NSType) that share a DeclID after within-file merge.
func TestParse_ClassDualResident(t *testing.T) {
	src := []byte(`<?php class Foo {} ?>`)
	r := Parse("a.php", src)

	var nsValueDecl, nsTypeDecl *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name != "Foo" || d.Kind != scope.KindClass {
			continue
		}
		switch d.Namespace {
		case scope.NSValue:
			nsValueDecl = d
		case scope.NSType:
			nsTypeDecl = d
		}
	}
	if nsValueDecl == nil {
		t.Fatalf("no NSValue decl for class Foo; decls=%+v", r.Decls)
	}
	if nsTypeDecl == nil {
		t.Fatalf("no NSType decl for class Foo; decls=%+v", r.Decls)
	}
	if nsValueDecl.ID != nsTypeDecl.ID {
		t.Errorf("class Foo NSValue.ID=%d NSType.ID=%d; want equal after merge",
			nsValueDecl.ID, nsTypeDecl.ID)
	}
}

// TestParse_EnumDualResident: `enum Color` emits TWO decls (NSValue +
// NSType) that share a DeclID after within-file merge.
func TestParse_EnumDualResident(t *testing.T) {
	src := []byte(`<?php
enum Color {
    case Red;
    case Green;
}
?>`)
	r := Parse("a.php", src)

	var nsValueDecl, nsTypeDecl *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name != "Color" || d.Kind != scope.KindEnum {
			continue
		}
		switch d.Namespace {
		case scope.NSValue:
			nsValueDecl = d
		case scope.NSType:
			nsTypeDecl = d
		}
	}
	if nsValueDecl == nil {
		t.Fatalf("no NSValue decl for enum Color; decls=%+v", r.Decls)
	}
	if nsTypeDecl == nil {
		t.Fatalf("no NSType decl for enum Color; decls=%+v", r.Decls)
	}
	if nsValueDecl.ID != nsTypeDecl.ID {
		t.Errorf("enum Color NSValue.ID=%d NSType.ID=%d; want equal after merge",
			nsValueDecl.ID, nsTypeDecl.ID)
	}
}

// TestParse_NameCollisionFunctionAndInterface: `function X()` (NSValue)
// and `interface X` (NSType) collide by name but live in different
// namespaces — each must be represented as its own decl row with the
// correct namespace tag. (Contrast: a function and a class with the
// same name both occupy NSValue and, given PHP's decl-ID scheme
// (path+name+ns+scope), end up sharing an ID — that's a pre-existing
// limitation, not something the type-split changes. An interface is
// the interesting case because it lives ONLY in NSType.)
func TestParse_NameCollisionFunctionAndInterface(t *testing.T) {
	src := []byte(`<?php
function X() { return 1; }
interface X {}
?>`)
	r := Parse("a.php", src)

	var fn, iface *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name != "X" {
			continue
		}
		switch d.Kind {
		case scope.KindFunction:
			if fn == nil {
				fn = d
			}
		case scope.KindInterface:
			if iface == nil {
				iface = d
			}
		}
	}
	if fn == nil {
		t.Fatalf("function X missing; decls=%+v", r.Decls)
	}
	if iface == nil {
		t.Fatalf("interface X missing; decls=%+v", r.Decls)
	}
	if fn.Namespace != scope.NSValue {
		t.Errorf("function X namespace = %q, want NSValue", fn.Namespace)
	}
	if iface.Namespace != scope.NSType {
		t.Errorf("interface X namespace = %q, want NSType", iface.Namespace)
	}
	// Namespaces differ → hashDecl produces different DeclIDs → the
	// two symbols are cleanly distinguishable.
	if fn.ID == iface.ID {
		t.Errorf("function X and interface X share DeclID %d; want distinct",
			fn.ID)
	}
}

// TestParse_InterfaceRefResolvesViaTypeNamespace: a parameter type hint
// referencing an interface (which lives in NSType only) must still
// resolve — either via direct NSType lookup or via the NSValue→NSType
// fallback in resolveRefs.
func TestParse_InterfaceRefResolvesViaTypeNamespace(t *testing.T) {
	src := []byte(`<?php
interface Foo {
    public function hello(): string;
}
function useIt(Foo $x) { return $x; }
?>`)
	r := Parse("a.php", src)

	var ifaceID scope.DeclID
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name == "Foo" && d.Kind == scope.KindInterface {
			ifaceID = d.ID
			break
		}
	}
	if ifaceID == 0 {
		t.Fatalf("no interface Foo decl found; decls=%v", declNames(r))
	}

	fooRefs := refsNamed(r, "Foo")
	if len(fooRefs) == 0 {
		t.Fatalf("no ref to Foo found; refs=%+v", r.Refs)
	}
	resolved := false
	for _, ref := range fooRefs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == ifaceID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("Foo type-hint ref didn't resolve to interface Foo; refs=%+v", fooRefs)
	}
}

// TestParse_UseSignature_Simple verifies `use Foo\Bar;` stamps
// Decl.Signature = "Foo\x00Bar" on the KindImport decl so the
// cross-file resolver can route to the source namespace.
func TestParse_UseSignature_Simple(t *testing.T) {
	src := []byte(`<?php use Foo\Bar; ?>`)
	r := Parse("a.php", src)
	bar := findDeclOfKind(r, "Bar", scope.KindImport)
	if bar == nil {
		t.Fatalf("Bar import missing; decls=%v", declNames(r))
	}
	if want := "Foo\x00Bar"; bar.Signature != want {
		t.Errorf("Signature = %q, want %q", bar.Signature, want)
	}
}

// TestParse_UseSignature_DeepPath verifies `use Foo\Bar\Baz;` stamps
// a backslash-separated modulePath.
func TestParse_UseSignature_DeepPath(t *testing.T) {
	src := []byte(`<?php use Foo\Bar\Baz; ?>`)
	r := Parse("a.php", src)
	baz := findDeclOfKind(r, "Baz", scope.KindImport)
	if baz == nil {
		t.Fatalf("Baz import missing; decls=%v", declNames(r))
	}
	if want := "Foo\\Bar\x00Baz"; baz.Signature != want {
		t.Errorf("Signature = %q, want %q", baz.Signature, want)
	}
}

// TestParse_UseSignature_Aliased verifies `use Foo\Bar as B;` stamps
// the ORIGINAL name in Signature (the decl's Name is the alias).
func TestParse_UseSignature_Aliased(t *testing.T) {
	src := []byte(`<?php use Foo\Bar as B; ?>`)
	r := Parse("a.php", src)
	b := findDeclOfKind(r, "B", scope.KindImport)
	if b == nil {
		t.Fatalf("B import missing; decls=%v", declNames(r))
	}
	if want := "Foo\x00Bar"; b.Signature != want {
		t.Errorf("Signature = %q, want %q (Name=%q should stay the alias)", b.Signature, want, b.Name)
	}
}

// TestParse_UseSignature_Grouped covers `use Foo\{A, B as C};`.
func TestParse_UseSignature_Grouped(t *testing.T) {
	src := []byte(`<?php use Foo\{A, B as C}; ?>`)
	r := Parse("a.php", src)
	a := findDeclOfKind(r, "A", scope.KindImport)
	c := findDeclOfKind(r, "C", scope.KindImport)
	if a == nil || c == nil {
		t.Fatalf("grouped imports missing; decls=%v", declNames(r))
	}
	if want := "Foo\x00A"; a.Signature != want {
		t.Errorf("A.Signature = %q, want %q", a.Signature, want)
	}
	if want := "Foo\x00B"; c.Signature != want {
		t.Errorf("C.Signature = %q, want %q (orig is B)", c.Signature, want)
	}
}

// TestParse_UseSignature_Function verifies `use function Foo\bar;`
// carries the "function:" prefix in Signature.
func TestParse_UseSignature_Function(t *testing.T) {
	src := []byte(`<?php use function Foo\bar; ?>`)
	r := Parse("a.php", src)
	bar := findDeclOfKind(r, "bar", scope.KindImport)
	if bar == nil {
		t.Fatalf("use-function import bar missing; decls=%v", declNames(r))
	}
	if want := "function:Foo\x00bar"; bar.Signature != want {
		t.Errorf("Signature = %q, want %q", bar.Signature, want)
	}
}

// TestParse_NamespaceFullPath verifies the file's KindNamespace decl
// carries the full backslash-qualified path in Signature.
func TestParse_NamespaceFullPath(t *testing.T) {
	src := []byte(`<?php namespace A\B\C; ?>`)
	r := Parse("a.php", src)
	ns := findDeclOfKind(r, "C", scope.KindNamespace)
	if ns == nil {
		t.Fatalf("namespace C decl missing; decls=%v", declNames(r))
	}
	if want := "A\\B\\C"; ns.Signature != want {
		t.Errorf("namespace Signature = %q, want %q", ns.Signature, want)
	}
}

// TestParse_ExportedDecls verifies top-level class/interface/trait/
// enum/function/const are marked Exported under PHP's implicit-public
// semantics.
func TestParse_ExportedDecls(t *testing.T) {
	src := []byte(`<?php
class Clazz {}
interface Iface {}
trait Tr {}
enum En {}
function fn1() {}
const K = 1;
?>`)
	r := Parse("a.php", src)
	for _, name := range []string{"Clazz", "Iface", "Tr", "En", "fn1", "K"} {
		d := findDecl(r, name)
		if d == nil {
			t.Errorf("%s decl missing; decls=%v", name, declNames(r))
			continue
		}
		if !d.Exported {
			t.Errorf("%s (kind=%s) not Exported; want true", name, d.Kind)
		}
	}
}

// TestParse_ExportedInNamespaceScope verifies top-level decls inside
// a `namespace A\B;` scope are still marked Exported.
func TestParse_ExportedInNamespaceScope(t *testing.T) {
	src := []byte(`<?php
namespace A\B;
class Foo {}
function bar() {}
?>`)
	r := Parse("a.php", src)
	for _, name := range []string{"Foo", "bar"} {
		d := findDecl(r, name)
		if d == nil {
			t.Errorf("%s decl missing; decls=%v", name, declNames(r))
			continue
		}
		if !d.Exported {
			t.Errorf("namespaced %s not Exported; want true", name)
		}
	}
}

// TestParse_NotExportedInClassBody verifies methods/fields are NOT
// marked Exported (they're class members, not file-scope exports).
func TestParse_NotExportedInClassBody(t *testing.T) {
	src := []byte(`<?php
class Foo {
  public function hello() {}
  public $x;
}
?>`)
	r := Parse("a.php", src)
	for _, name := range []string{"hello", "$x"} {
		d := findDecl(r, name)
		if d == nil {
			continue
		}
		if d.Exported {
			t.Errorf("class member %s is Exported; want false", name)
		}
	}
}
