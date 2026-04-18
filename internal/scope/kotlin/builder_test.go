package kotlin

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

func TestParse_BasicClassPrimaryCtor(t *testing.T) {
	src := []byte(`class Foo(val x: Int, var y: String)
`)
	r := Parse("Foo.kt", src)
	if findDeclKind(r, "Foo", scope.KindClass) == nil {
		t.Fatalf("class Foo missing; decls=%v", declNames(r))
	}
	xDecl := findDeclKind(r, "x", scope.KindField)
	if xDecl == nil {
		t.Fatalf("primary-ctor `val x` should be KindField; decls=%v", declNames(r))
	}
	if xDecl.Namespace != scope.NSField {
		t.Errorf("x namespace = %v, want NSField", xDecl.Namespace)
	}
	yDecl := findDeclKind(r, "y", scope.KindField)
	if yDecl == nil {
		t.Errorf("primary-ctor `var y` should be KindField; decls=%v", declNames(r))
	}
}

func TestParse_ClassMethod(t *testing.T) {
	src := []byte(`class Foo {
    fun bar(): Int = 42
}
`)
	r := Parse("Foo.kt", src)
	barDecl := findDeclKind(r, "bar", scope.KindMethod)
	if barDecl == nil {
		t.Fatalf("method `bar` missing; decls=%v", declNames(r))
	}
	if barDecl.Namespace != scope.NSField {
		t.Errorf("bar namespace = %v, want NSField", barDecl.Namespace)
	}
}

func TestParse_TopLevelFunction(t *testing.T) {
	src := []byte(`fun hello() = "hi"
`)
	r := Parse("main.kt", src)
	hello := findDeclKind(r, "hello", scope.KindFunction)
	if hello == nil {
		t.Fatalf("top-level fun `hello` missing; decls=%v", declNames(r))
	}
	if hello.Namespace != scope.NSValue {
		t.Errorf("hello namespace = %v, want NSValue", hello.Namespace)
	}
}

func TestParse_ExtensionFunction(t *testing.T) {
	src := []byte(`fun String.greet(): String = "hi"
`)
	r := Parse("ext.kt", src)
	greet := findDeclKind(r, "greet", scope.KindFunction)
	if greet == nil {
		t.Fatalf("extension fun `greet` missing; decls=%v", declNames(r))
	}
	found := false
	for _, ref := range r.Refs {
		if ref.Name == "String" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ref to `String` (extension receiver)")
	}
}

func TestParse_ThisFieldResolves(t *testing.T) {
	src := []byte(`class Foo(val x: Int) {
    fun use() {
        val y = this.x
    }
}
`)
	r := Parse("Foo.kt", src)
	xDecl := findDeclKind(r, "x", scope.KindField)
	if xDecl == nil {
		t.Fatalf("field x missing; decls=%v", declNames(r))
	}
	found := false
	for _, ref := range r.Refs {
		if ref.Name == "x" && ref.Binding.Reason == "this_dot_field" && ref.Binding.Decl == xDecl.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("this.x did not resolve to field x; refs=%+v", refsNamed(r, "x"))
	}
}

func TestParse_GenericClass(t *testing.T) {
	src := []byte(`class Box<T>(val value: T)
`)
	r := Parse("Box.kt", src)
	if findDeclKind(r, "Box", scope.KindClass) == nil {
		t.Fatalf("class Box missing; decls=%v", declNames(r))
	}
	tDecl := findDeclKind(r, "T", scope.KindType)
	if tDecl == nil {
		t.Fatalf("generic T missing; decls=%v", declNames(r))
	}
	if tDecl.Scope == 0 {
		t.Errorf("T should be scoped to class, got scope=0")
	}
	valueDecl := findDeclKind(r, "value", scope.KindField)
	if valueDecl == nil {
		t.Errorf("primary-ctor `val value` should be KindField; decls=%v", declNames(r))
	}
}

func TestParse_Lambda(t *testing.T) {
	src := []byte(`val add = { a: Int, b: Int -> a + b }
`)
	r := Parse("lambda.kt", src)
	addDecl := findDecl(r, "add")
	if addDecl == nil {
		t.Fatalf("val `add` missing; decls=%v", declNames(r))
	}
	aDecl := findDeclKind(r, "a", scope.KindParam)
	if aDecl == nil {
		t.Fatalf("lambda param `a` missing; decls=%v", declNames(r))
	}
	bDecl := findDeclKind(r, "b", scope.KindParam)
	if bDecl == nil {
		t.Fatalf("lambda param `b` missing; decls=%v", declNames(r))
	}
	var lambdaScope *scope.Scope
	for i := range r.Scopes {
		if r.Scopes[i].Kind == scope.ScopeFunction {
			lambdaScope = &r.Scopes[i]
			break
		}
	}
	if lambdaScope == nil {
		t.Fatalf("expected a ScopeFunction for the lambda")
	}
	if aDecl.Scope != lambdaScope.ID {
		t.Errorf("param `a` scope = %d, want lambda scope %d", aDecl.Scope, lambdaScope.ID)
	}
	if bDecl.Scope != lambdaScope.ID {
		t.Errorf("param `b` scope = %d, want lambda scope %d", bDecl.Scope, lambdaScope.ID)
	}
}

func TestParse_ObjectSingleton(t *testing.T) {
	src := []byte(`object Singleton {
    fun go() {}
}
`)
	r := Parse("obj.kt", src)
	sDecl := findDeclKind(r, "Singleton", scope.KindClass)
	if sDecl == nil {
		t.Fatalf("object `Singleton` missing; decls=%v", declNames(r))
	}
	goDecl := findDeclKind(r, "go", scope.KindMethod)
	if goDecl == nil {
		t.Fatalf("object member `go` should be KindMethod; decls=%v", declNames(r))
	}
	if goDecl.Namespace != scope.NSField {
		t.Errorf("go namespace = %v, want NSField", goDecl.Namespace)
	}
}

func TestParse_Import(t *testing.T) {
	src := []byte(`import com.foo.Bar
import com.foo.baz.*

class A
`)
	r := Parse("A.kt", src)
	barDecl := findDeclKind(r, "Bar", scope.KindImport)
	if barDecl == nil {
		t.Fatalf("import decl `Bar` missing; decls=%v", declNames(r))
	}
	for _, d := range r.Decls {
		if d.Name == "*" {
			t.Errorf("wildcard import should not emit a `*` decl")
		}
	}
}

func TestParse_FullSpanClassAndFun(t *testing.T) {
	src := []byte(`class Foo {
    fun bar() {
        return
    }
}
`)
	r := Parse("Foo.kt", src)
	fooDecl := findDeclKind(r, "Foo", scope.KindClass)
	if fooDecl == nil {
		t.Fatalf("class Foo missing")
	}
	if fooDecl.FullSpan.StartByte != 0 {
		t.Errorf("Foo FullSpan.StartByte = %d, want 0 (start of 'class')", fooDecl.FullSpan.StartByte)
	}
	lastBrace := -1
	for i := len(src) - 1; i >= 0; i-- {
		if src[i] == '}' {
			lastBrace = i
			break
		}
	}
	if lastBrace < 0 {
		t.Fatalf("no closing brace in source")
	}
	if int(fooDecl.FullSpan.EndByte) != lastBrace+1 {
		t.Errorf("Foo FullSpan.EndByte = %d, want %d (past class '}')",
			fooDecl.FullSpan.EndByte, lastBrace+1)
	}

	barDecl := findDeclKind(r, "bar", scope.KindMethod)
	if barDecl == nil {
		t.Fatalf("method bar missing")
	}
	funIdx := -1
	for i := 0; i+3 <= len(src); i++ {
		if string(src[i:i+3]) == "fun" {
			funIdx = i
			break
		}
	}
	if funIdx < 0 {
		t.Fatalf("no 'fun' in source")
	}
	if int(barDecl.FullSpan.StartByte) != funIdx {
		t.Errorf("bar FullSpan.StartByte = %d, want %d (start of 'fun')",
			barDecl.FullSpan.StartByte, funIdx)
	}
}

func TestParse_SealedClass(t *testing.T) {
	src := []byte(`sealed class Result {
    class Ok : Result()
    class Err : Result()
}
`)
	r := Parse("Result.kt", src)
	if findDeclKind(r, "Result", scope.KindClass) == nil {
		t.Errorf("sealed class `Result` missing; decls=%v", declNames(r))
	}
}

func TestParse_DataClass(t *testing.T) {
	src := []byte(`data class Point(val x: Int, val y: Int)
`)
	r := Parse("Point.kt", src)
	if findDeclKind(r, "Point", scope.KindClass) == nil {
		t.Fatalf("data class `Point` missing; decls=%v", declNames(r))
	}
	if findDeclKind(r, "x", scope.KindField) == nil {
		t.Errorf("data class field `x` missing")
	}
	if findDeclKind(r, "y", scope.KindField) == nil {
		t.Errorf("data class field `y` missing")
	}
}

func TestParse_EnumClass(t *testing.T) {
	src := []byte(`enum class Color { RED, GREEN, BLUE }
`)
	r := Parse("Color.kt", src)
	if findDeclKind(r, "Color", scope.KindClass) == nil {
		t.Errorf("enum class `Color` missing; decls=%v", declNames(r))
	}
}

func TestParse_Interface(t *testing.T) {
	src := []byte(`interface Greeter {
    fun greet(): String
}
`)
	r := Parse("Greeter.kt", src)
	if findDeclKind(r, "Greeter", scope.KindInterface) == nil {
		t.Fatalf("interface `Greeter` missing; decls=%v", declNames(r))
	}
}

func TestParse_TopLevelValVar(t *testing.T) {
	src := []byte(`val globalConst = 42
var globalMutable = "hi"
`)
	r := Parse("globals.kt", src)
	if findDeclKind(r, "globalConst", scope.KindVar) == nil {
		t.Errorf("top-level `val globalConst` missing (expected KindVar); decls=%v", declNames(r))
	}
	if findDeclKind(r, "globalMutable", scope.KindVar) == nil {
		t.Errorf("top-level `var globalMutable` missing; decls=%v", declNames(r))
	}
}

func TestParse_RefsNoPanicOnEmpty(t *testing.T) {
	r := Parse("empty.kt", []byte(""))
	if r == nil {
		t.Fatalf("Parse returned nil on empty input")
	}
}

func TestParse_RefsHaveBinding(t *testing.T) {
	src := []byte(`class Foo {
    fun bar() {
        use(undefined)
    }
}
`)
	r := Parse("Foo.kt", src)
	for _, ref := range r.Refs {
		if ref.Binding.Kind == scope.BindUnresolved && ref.Binding.Reason == "" {
			t.Errorf("unresolved ref %q missing reason", ref.Name)
		}
	}
}

func TestParse_TypealiasAsType(t *testing.T) {
	src := []byte(`typealias Ints = List<Int>
`)
	r := Parse("alias.kt", src)
	if findDeclKind(r, "Ints", scope.KindType) == nil {
		t.Errorf("typealias `Ints` should be KindType; decls=%v", declNames(r))
	}
}

func TestParse_LambdaImplicitIt(t *testing.T) {
	src := []byte(`val f = listOf(1, 2, 3).map { it * 2 }
`)
	r := Parse("it.kt", src)
	hasFunc := false
	for _, s := range r.Scopes {
		if s.Kind == scope.ScopeFunction {
			hasFunc = true
			break
		}
	}
	if !hasFunc {
		t.Errorf("expected a ScopeFunction for the lambda")
	}
}

func TestParse_BuiltinResolves(t *testing.T) {
	src := []byte(`val x: Int = 42
`)
	r := Parse("b.kt", src)
	found := false
	for _, ref := range r.Refs {
		if ref.Name == "Int" && ref.Binding.Reason == "builtin" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Int ref did not resolve as builtin; refs=%+v", refsNamed(r, "Int"))
	}
}
