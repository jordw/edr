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
