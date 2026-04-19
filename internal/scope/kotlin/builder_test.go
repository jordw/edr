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
	// Synthetic `it` param should be emitted in the lambda scope.
	itDecl := findDeclKind(r, "it", scope.KindParam)
	if itDecl == nil {
		t.Fatalf("synthetic `it` param missing; decls=%v", declNames(r))
	}
	if itDecl.Scope != lambdaScope.ID {
		t.Errorf("`it` scope = %d, want lambda scope %d",
			itDecl.Scope, lambdaScope.ID)
	}
	// The `it` ref inside the lambda body should resolve to the synthetic param.
	refs := refsNamed(r, "it")
	if len(refs) < 1 {
		t.Fatalf("expected at least 1 ref to `it`, got %d", len(refs))
	}
	for i, ref := range refs {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("ref %d to `it` not resolved: %+v", i, ref.Binding)
			continue
		}
		if ref.Binding.Decl != itDecl.ID {
			t.Errorf("ref %d to `it` resolves to %d, want synthetic `it` decl %d",
				i, ref.Binding.Decl, itDecl.ID)
		}
	}
}

// Explicit-param lambdas must NOT additionally synthesize `it`.
func TestParse_LambdaExplicitParamsNoImplicitIt(t *testing.T) {
	src := []byte(`val add = { a: Int, b: Int -> a + b }
`)
	r := Parse("add.kt", src)
	if findDeclKind(r, "it", scope.KindParam) != nil {
		t.Errorf("did not expect synthetic `it` for explicit-param lambda; decls=%v",
			declNames(r))
	}
	// Sanity: explicit params are still there.
	if findDeclKind(r, "a", scope.KindParam) == nil ||
		findDeclKind(r, "b", scope.KindParam) == nil {
		t.Errorf("explicit lambda params missing; decls=%v", declNames(r))
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

// TestParse_SingleExpressionFn: `fun f(x) = x*x` should open a function
// scope for the body so the param x is declared in it, and the ref to
// x resolves to that param (not to some outer-scope binding).
func TestParse_SingleExpressionFn(t *testing.T) {
	src := []byte(`fun square(x: Int) = x * x
`)
	r := Parse("a.kt", src)
	// Find the param decl.
	var paramDecl *scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "x" && r.Decls[i].Kind == scope.KindParam {
			paramDecl = &r.Decls[i]
			break
		}
	}
	if paramDecl == nil {
		t.Fatalf("no param decl for x; decls=%v", declNames(r))
	}
	if paramDecl.Scope == 1 {
		t.Errorf("param x was emitted into file scope (=1); expected a function scope")
	}
	// Find refs to x. They should resolve to the param.
	refs := refsNamed(r, "x")
	if len(refs) < 2 {
		t.Fatalf("expected at least 2 refs to x, got %d", len(refs))
	}
	for i, ref := range refs {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("ref %d to x not resolved: %+v", i, ref.Binding)
			continue
		}
		if ref.Binding.Decl != paramDecl.ID {
			t.Errorf("ref %d to x resolves to %d, want param decl %d",
				i, ref.Binding.Decl, paramDecl.ID)
		}
	}
}

// TestParse_ImportSignatureSimple: `import com.foo.Bar` emits a
// KindImport decl named "Bar" with Signature="com.foo\x00Bar".
func TestParse_ImportSignatureSimple(t *testing.T) {
	src := []byte(`import com.foo.Bar

class A
`)
	r := Parse("A.kt", src)
	bar := findDeclKind(r, "Bar", scope.KindImport)
	if bar == nil {
		t.Fatalf("import decl Bar missing; decls=%v", declNames(r))
	}
	want := "com.foo" + "\x00" + "Bar"
	if bar.Signature != want {
		t.Errorf("Bar.Signature = %q, want %q", bar.Signature, want)
	}
}

// TestParse_ImportSignatureTopLevelFun: `import com.foo.bar` (lowercase,
// could be a top-level fun or val) emits a KindImport with Signature
// "com.foo\x00bar". The builder doesn't care whether the imported name
// is a class or a top-level fun — that's the resolver's job.
func TestParse_ImportSignatureTopLevelFun(t *testing.T) {
	src := []byte(`import com.foo.bar
`)
	r := Parse("A.kt", src)
	bar := findDeclKind(r, "bar", scope.KindImport)
	if bar == nil {
		t.Fatalf("import decl bar missing; decls=%v", declNames(r))
	}
	want := "com.foo" + "\x00" + "bar"
	if bar.Signature != want {
		t.Errorf("bar.Signature = %q, want %q", bar.Signature, want)
	}
}

// TestParse_ImportAliased: `import com.foo.Bar as B` binds the local
// name B but records origName=Bar in the Signature.
func TestParse_ImportAliased(t *testing.T) {
	src := []byte(`import com.foo.Bar as B

class A
`)
	r := Parse("A.kt", src)
	b := findDeclKind(r, "B", scope.KindImport)
	if b == nil {
		t.Fatalf("import decl B missing; decls=%v", declNames(r))
	}
	want := "com.foo" + "\x00" + "Bar"
	if b.Signature != want {
		t.Errorf("B.Signature = %q, want %q", b.Signature, want)
	}
	// The un-aliased name should NOT be present as an import decl.
	if findDeclKind(r, "Bar", scope.KindImport) != nil {
		t.Errorf("aliased import should not leave a `Bar` import decl")
	}
}

// TestParse_ImportWildcard: `import com.foo.*` is punted in v1 — no
// decl is emitted, but the parser doesn't crash and subsequent decls
// still extract correctly.
func TestParse_ImportWildcard(t *testing.T) {
	src := []byte(`import com.foo.*

class A
`)
	r := Parse("A.kt", src)
	for _, d := range r.Decls {
		if d.Name == "*" {
			t.Errorf("wildcard import should not emit a `*` decl; decls=%v",
				declNames(r))
		}
		// Also verify no decl was emitted for the last dotted path
		// segment (`foo`) as an import — common past bug.
		if d.Name == "foo" && d.Kind == scope.KindImport {
			t.Errorf("wildcard import incorrectly emitted `foo` as import")
		}
	}
	if findDeclKind(r, "A", scope.KindClass) == nil {
		t.Errorf("class A missing; decls=%v", declNames(r))
	}
}

// TestParse_PackageCaptured: `package com.acme` is recorded as a
// synthetic KindNamespace decl at file scope with the full dotted path
// as Name. The Phase-1 import graph resolver reads this to compute
// FQNs of top-level decls.
func TestParse_PackageCaptured(t *testing.T) {
	src := []byte(`package com.acme.sub

class A
`)
	r := Parse("A.kt", src)
	var pkgDecl *scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Kind == scope.KindNamespace {
			pkgDecl = &r.Decls[i]
			break
		}
	}
	if pkgDecl == nil {
		t.Fatalf("no KindNamespace decl for package; decls=%v", declNames(r))
	}
	if pkgDecl.Name != "com.acme.sub" {
		t.Errorf("package decl Name = %q, want %q", pkgDecl.Name, "com.acme.sub")
	}
	// Class A should still parse fine alongside the package clause.
	if findDeclKind(r, "A", scope.KindClass) == nil {
		t.Errorf("class A missing alongside package clause; decls=%v",
			declNames(r))
	}
}

// TestParse_PackageNone: a file without any `package` clause has no
// KindNamespace decl — packagePath is the empty default package.
func TestParse_PackageNone(t *testing.T) {
	src := []byte(`class A
`)
	r := Parse("A.kt", src)
	for _, d := range r.Decls {
		if d.Kind == scope.KindNamespace {
			t.Errorf("unexpected KindNamespace decl %q when no package clause",
				d.Name)
		}
	}
}

// TestParse_ExportedVsPrivate: file-scope classes/funs/vals without
// `private` are marked Exported=true; `private` at stmtStart prevents
// export. `internal` and default visibility both count as exported.
func TestParse_ExportedVsPrivate(t *testing.T) {
	src := []byte(`class Public1
private class Private1
internal class Internal1
fun publicFun() {}
private fun privateFun() {}
val publicVal = 1
private val privateVal = 2
`)
	r := Parse("A.kt", src)
	cases := []struct {
		name     string
		kind     scope.DeclKind
		exported bool
	}{
		{"Public1", scope.KindClass, true},
		{"Private1", scope.KindClass, false},
		{"Internal1", scope.KindClass, true},
		{"publicFun", scope.KindFunction, true},
		{"privateFun", scope.KindFunction, false},
		{"publicVal", scope.KindVar, true},
		{"privateVal", scope.KindVar, false},
	}
	for _, c := range cases {
		d := findDeclKind(r, c.name, c.kind)
		if d == nil {
			t.Errorf("decl %q (%v) missing; decls=%v", c.name, c.kind, declNames(r))
			continue
		}
		if d.Exported != c.exported {
			t.Errorf("decl %q Exported = %v, want %v", c.name, d.Exported, c.exported)
		}
	}
}

// TestParse_MembersNotExported: class members (methods/fields) do NOT
// get Exported=true regardless of the class's export state — export is
// a file-scope property in our v1 model.
func TestParse_MembersNotExported(t *testing.T) {
	src := []byte(`class Foo {
    fun bar() {}
    val x = 1
}
`)
	r := Parse("Foo.kt", src)
	foo := findDeclKind(r, "Foo", scope.KindClass)
	if foo == nil {
		t.Fatalf("class Foo missing")
	}
	if !foo.Exported {
		t.Errorf("Foo Exported = false, want true (file-scope, default public)")
	}
	bar := findDeclKind(r, "bar", scope.KindMethod)
	if bar == nil {
		t.Fatalf("method bar missing")
	}
	if bar.Exported {
		t.Errorf("method bar Exported = true, want false (member, not top-level)")
	}
	x := findDeclKind(r, "x", scope.KindField)
	if x == nil {
		t.Fatalf("field x missing")
	}
	if x.Exported {
		t.Errorf("field x Exported = true, want false (member, not top-level)")
	}
}
