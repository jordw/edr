package swift

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

// TestParse_ClassWithMethodAndField: class body contains a method
// (KindMethod, NSField) and a field (KindField, NSField). Body refs
// to `x` inside the method resolve via implicit self.
func TestParse_ClassWithMethodAndField(t *testing.T) {
	src := []byte(`class Foo {
    var x: Int = 0
    func bar() -> Int { return x }
}
`)
	r := Parse("a.swift", src)
	if findDecl(r, "Foo") == nil {
		t.Fatalf("Foo missing; decls=%v", declNames(r))
	}
	xDecl := findDecl(r, "x")
	if xDecl == nil {
		t.Fatalf("x missing; decls=%v", declNames(r))
	}
	if xDecl.Kind != scope.KindField {
		t.Errorf("x kind = %v, want field", xDecl.Kind)
	}
	if xDecl.Namespace != scope.NSField {
		t.Errorf("x namespace = %v, want field", xDecl.Namespace)
	}
	barDecl := findDecl(r, "bar")
	if barDecl == nil {
		t.Fatalf("bar missing; decls=%v", declNames(r))
	}
	if barDecl.Kind != scope.KindMethod {
		t.Errorf("bar kind = %v, want method", barDecl.Kind)
	}
	if barDecl.Namespace != scope.NSField {
		t.Errorf("bar namespace = %v, want field (method on class)", barDecl.Namespace)
	}
}

// TestParse_StructWithFieldAndMethod: same shape as class.
func TestParse_StructWithFieldAndMethod(t *testing.T) {
	src := []byte(`struct Bar {
    let y: Int
    func baz() {}
}
`)
	r := Parse("a.swift", src)
	if findDecl(r, "Bar") == nil {
		t.Fatalf("Bar missing; decls=%v", declNames(r))
	}
	yDecl := findDecl(r, "y")
	if yDecl == nil {
		t.Fatalf("y missing; decls=%v", declNames(r))
	}
	if yDecl.Kind != scope.KindField || yDecl.Namespace != scope.NSField {
		t.Errorf("y kind=%v ns=%v, want field/NSField", yDecl.Kind, yDecl.Namespace)
	}
	bazDecl := findDecl(r, "baz")
	if bazDecl == nil {
		t.Fatalf("baz missing; decls=%v", declNames(r))
	}
	if bazDecl.Kind != scope.KindMethod {
		t.Errorf("baz kind = %v, want method", bazDecl.Kind)
	}
}

// TestParse_Protocol: protocol body with method is KindInterface with
// methods as KindMethod.
func TestParse_Protocol(t *testing.T) {
	src := []byte(`protocol P {
    func doit() -> Int
}
`)
	r := Parse("a.swift", src)
	pDecl := findDecl(r, "P")
	if pDecl == nil {
		t.Fatalf("P missing; decls=%v", declNames(r))
	}
	if pDecl.Kind != scope.KindInterface {
		t.Errorf("P kind = %v, want interface", pDecl.Kind)
	}
	reqDecl := findDecl(r, "doit")
	if reqDecl == nil {
		t.Fatalf("doit missing; decls=%v", declNames(r))
	}
	if reqDecl.Kind != scope.KindMethod {
		t.Errorf("doit kind = %v, want method", reqDecl.Kind)
	}
	if reqDecl.Namespace != scope.NSField {
		t.Errorf("doit namespace = %v, want field", reqDecl.Namespace)
	}
}

// TestParse_Extension: extension methods are emitted with a scope
// bound to the extension's target type.
func TestParse_Extension(t *testing.T) {
	src := []byte(`extension Foo {
    func bazE() {}
}
`)
	r := Parse("a.swift", src)
	bazDecl := findDecl(r, "bazE")
	if bazDecl == nil {
		t.Fatalf("bazE missing; decls=%v", declNames(r))
	}
	if bazDecl.Kind != scope.KindMethod {
		t.Errorf("bazE kind = %v, want method", bazDecl.Kind)
	}
	// The extension target `Foo` should appear as a ref.
	fooRefs := refsNamed(r, "Foo")
	if len(fooRefs) == 0 {
		t.Errorf("expected Foo to appear as a ref from extension target")
	}
	// The method scope should be a ScopeClass (extension body).
	classScopes := 0
	for _, sc := range r.Scopes {
		if sc.Kind == scope.ScopeClass {
			classScopes++
		}
	}
	if classScopes < 1 {
		t.Errorf("expected at least 1 ScopeClass for extension body, got %d", classScopes)
	}
}

// TestParse_Closure: closure with typed params emits them as KindParam
// scoped to the closure body.
func TestParse_Closure(t *testing.T) {
	src := []byte(`let add = { (a: Int, b: Int) in a + b }
`)
	r := Parse("a.swift", src)
	aDecl := findDecl(r, "a")
	bDecl := findDecl(r, "b")
	if aDecl == nil || bDecl == nil {
		t.Fatalf("closure params missing; decls=%v", declNames(r))
	}
	if aDecl.Kind != scope.KindParam {
		t.Errorf("a kind = %v, want param", aDecl.Kind)
	}
	if bDecl.Kind != scope.KindParam {
		t.Errorf("b kind = %v, want param", bDecl.Kind)
	}
	// `a + b` body refs should resolve to the closure params.
	aRefs := refsNamed(r, "a")
	if len(aRefs) == 0 {
		t.Fatal("no refs to a")
	}
	resolved := false
	for _, ref := range aRefs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == aDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("a ref not resolved to closure param; refs=%+v", aRefs)
	}
}

// TestParse_ArgumentLabels_NotEmitted: call-site argument labels like
// `foo(first: 1, second: 2)` should NOT emit refs to `first` or
// `second` (they are labels, not references).
func TestParse_ArgumentLabels_NotEmitted(t *testing.T) {
	src := []byte(`func user() {
    foo(first: 1, second: 2)
}
`)
	r := Parse("a.swift", src)
	firstRefs := refsNamed(r, "first")
	secondRefs := refsNamed(r, "second")
	if len(firstRefs) != 0 {
		t.Errorf("expected no refs to argument label 'first', got %d", len(firstRefs))
	}
	if len(secondRefs) != 0 {
		t.Errorf("expected no refs to argument label 'second', got %d", len(secondRefs))
	}
	// `foo` should still appear as a ref.
	if len(refsNamed(r, "foo")) == 0 {
		t.Errorf("expected foo to appear as a ref")
	}
}

// TestParse_SelfDotField: `self.x` inside a method resolves to the
// type's field via self_dot_field reason.
func TestParse_SelfDotField(t *testing.T) {
	src := []byte(`class Counter {
    var n: Int = 0
    func inc() { self.n = self.n + 1 }
}
`)
	r := Parse("a.swift", src)
	nDecl := findDeclKind(r, "n", scope.KindField)
	if nDecl == nil {
		t.Fatal("n field missing")
	}
	nRefs := refsNamed(r, "n")
	if len(nRefs) == 0 {
		t.Fatal("no refs to n")
	}
	selfRefs := 0
	for _, ref := range nRefs {
		if ref.Binding.Reason == "self_dot_field" &&
			ref.Binding.Kind == scope.BindResolved &&
			ref.Binding.Decl == nDecl.ID {
			selfRefs++
		}
	}
	if selfRefs == 0 {
		t.Errorf("no self.n refs resolved via self_dot_field; refs=%+v", nRefs)
	}
}

// TestParse_GenericStruct: `struct Box<T> { let value: T }` — T is
// KindType.
func TestParse_GenericStruct(t *testing.T) {
	src := []byte(`struct Box<T> {
    let value: T
}
`)
	r := Parse("a.swift", src)
	if findDecl(r, "Box") == nil {
		t.Fatalf("Box missing; decls=%v", declNames(r))
	}
	tDecl := findDecl(r, "T")
	if tDecl == nil {
		t.Fatalf("T missing; decls=%v", declNames(r))
	}
	if tDecl.Kind != scope.KindType {
		t.Errorf("T kind = %v, want type", tDecl.Kind)
	}
	valueDecl := findDecl(r, "value")
	if valueDecl == nil {
		t.Fatalf("value missing; decls=%v", declNames(r))
	}
	if valueDecl.Namespace != scope.NSField {
		t.Errorf("value namespace = %v, want field", valueDecl.Namespace)
	}
}

// TestParse_Import: `import Foundation` emits the import decl.
func TestParse_Import(t *testing.T) {
	src := []byte(`import Foundation

class X {}
`)
	r := Parse("a.swift", src)
	fDecl := findDecl(r, "Foundation")
	if fDecl == nil {
		t.Fatalf("Foundation missing; decls=%v", declNames(r))
	}
	if fDecl.Kind != scope.KindImport {
		t.Errorf("Foundation kind = %v, want import", fDecl.Kind)
	}
}

// TestParse_FullSpan_ClassCoversKeywordThroughBrace: the class decl's
// FullSpan should start at the `class` keyword and end after `}`.
func TestParse_FullSpan_ClassCoversKeywordThroughBrace(t *testing.T) {
	src := []byte(`class Foo {
    var x: Int = 0
}
`)
	r := Parse("a.swift", src)
	fooDecl := findDecl(r, "Foo")
	if fooDecl == nil {
		t.Fatalf("Foo missing")
	}
	// FullSpan.StartByte should be at the 'c' of 'class', i.e., byte 0.
	if fooDecl.FullSpan.StartByte != 0 {
		t.Errorf("Foo FullSpan.StartByte = %d, want 0 (start of 'class')",
			fooDecl.FullSpan.StartByte)
	}
	// FullSpan.EndByte should be past the closing '}' at the end of the
	// class body.
	closeBrace := strings.LastIndex(string(src), "}")
	if int(fooDecl.FullSpan.EndByte) <= closeBrace {
		t.Errorf("Foo FullSpan.EndByte = %d, want > %d (past closing brace)",
			fooDecl.FullSpan.EndByte, closeBrace)
	}
}

// TestParse_InitAndDeinit: init/deinit emit as KindMethod in a class scope.
func TestParse_InitAndDeinit(t *testing.T) {
	src := []byte(`class Foo {
    init(x: Int) {}
    deinit {}
}
`)
	r := Parse("a.swift", src)
	initDecl := findDecl(r, "init")
	if initDecl == nil {
		t.Fatalf("init missing; decls=%v", declNames(r))
	}
	if initDecl.Kind != scope.KindMethod {
		t.Errorf("init kind = %v, want method", initDecl.Kind)
	}
	deinitDecl := findDecl(r, "deinit")
	if deinitDecl == nil {
		t.Fatalf("deinit missing; decls=%v", declNames(r))
	}
	if deinitDecl.Kind != scope.KindMethod {
		t.Errorf("deinit kind = %v, want method", deinitDecl.Kind)
	}
}

// TestParse_ExternalParamLabel: `func greet(first name: String)` —
// `first` is a label (skipped), `name` is the param.
func TestParse_ExternalParamLabel(t *testing.T) {
	src := []byte(`func greet(first name: String) {
    print(name)
}
`)
	r := Parse("a.swift", src)
	nameDecl := findDecl(r, "name")
	if nameDecl == nil {
		t.Fatalf("name param missing; decls=%v", declNames(r))
	}
	if nameDecl.Kind != scope.KindParam {
		t.Errorf("name kind = %v, want param", nameDecl.Kind)
	}
	// `first` should NOT be emitted as a decl.
	if findDecl(r, "first") != nil {
		t.Errorf("'first' (external label) should not be emitted as a decl")
	}
	// `name` body ref should resolve to the param.
	nameRefs := refsNamed(r, "name")
	if len(nameRefs) == 0 {
		t.Fatal("no refs to name")
	}
	resolved := false
	for _, ref := range nameRefs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == nameDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("name ref in body not resolved to param")
	}
}

// TestParse_TypeAlias: `typealias Name = SomeType` emits Name as KindType.
func TestParse_TypeAlias(t *testing.T) {
	src := []byte(`typealias Thing = Int
`)
	r := Parse("a.swift", src)
	tDecl := findDecl(r, "Thing")
	if tDecl == nil {
		t.Fatalf("Thing missing; decls=%v", declNames(r))
	}
	if tDecl.Kind != scope.KindType {
		t.Errorf("Thing kind = %v, want type", tDecl.Kind)
	}
}

// TestParse_EnumCases: enum-case members are emitted as KindConst in
// the enum scope.
func TestParse_EnumCases(t *testing.T) {
	src := []byte(`enum Color {
    case red, green, blue
}
`)
	r := Parse("a.swift", src)
	if findDecl(r, "Color") == nil {
		t.Fatalf("Color missing")
	}
	redDecl := findDecl(r, "red")
	if redDecl == nil {
		t.Fatalf("red missing; decls=%v", declNames(r))
	}
	if redDecl.Kind != scope.KindConst {
		t.Errorf("red kind = %v, want const", redDecl.Kind)
	}
	for _, n := range []string{"green", "blue"} {
		if findDecl(r, n) == nil {
			t.Errorf("enum case %q missing", n)
		}
	}
}

// TestParse_ControlFlowBinding: `if let x = opt { ... }` — x is KindVar
// inside the body.
func TestParse_ControlFlowBinding(t *testing.T) {
	src := []byte(`func g(opt: Int?) {
    if let x = opt {
        print(x)
    }
}
`)
	r := Parse("a.swift", src)
	xDecl := findDecl(r, "x")
	if xDecl == nil {
		t.Fatalf("x missing; decls=%v", declNames(r))
	}
	if xDecl.Kind != scope.KindLet {
		t.Errorf("x kind = %v, want let", xDecl.Kind)
	}
}

// TestParse_NoPanicOnStringAndComments: adversarial string/comment
// shapes should not panic.
func TestParse_NoPanicOnStringAndComments(t *testing.T) {
	src := []byte(`class X {
    // single-line comment with "quotes" inside
    /* block /* nested */ still block */
    let a = "plain"
    let b = "with \(1 + 2) interpolation"
    let c = """
triple
quoted
"""
}
`)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Parse panicked: %v", r)
		}
	}()
	_ = Parse("a.swift", src)
}

// TestParse_AttributesSkipped: @MainActor and @objc should not appear
// as refs.
func TestParse_AttributesSkipped(t *testing.T) {
	src := []byte(`@MainActor
class Foo {
    @objc func bar() {}
}
`)
	r := Parse("a.swift", src)
	if refs := refsNamed(r, "MainActor"); len(refs) != 0 {
		t.Errorf("@MainActor should be skipped, got %d refs", len(refs))
	}
	if refs := refsNamed(r, "objc"); len(refs) != 0 {
		t.Errorf("@objc should be skipped, got %d refs", len(refs))
	}
	// The class and method should still be emitted.
	if findDecl(r, "Foo") == nil {
		t.Errorf("Foo missing after attribute skip")
	}
	if findDecl(r, "bar") == nil {
		t.Errorf("bar missing after attribute skip")
	}
}

// TestParse_FullSpan_FuncCoversKeywordThroughBrace: `func` decl
// FullSpan covers keyword through closing brace.
func TestParse_FullSpan_FuncCoversKeywordThroughBrace(t *testing.T) {
	src := []byte(`func hi() { return }
`)
	r := Parse("a.swift", src)
	fDecl := findDecl(r, "hi")
	if fDecl == nil {
		t.Fatalf("hi missing")
	}
	if fDecl.FullSpan.StartByte != 0 {
		t.Errorf("hi FullSpan.StartByte = %d, want 0", fDecl.FullSpan.StartByte)
	}
	closeBrace := strings.LastIndex(string(src), "}")
	if int(fDecl.FullSpan.EndByte) <= closeBrace {
		t.Errorf("hi FullSpan.EndByte = %d, want > %d", fDecl.FullSpan.EndByte, closeBrace)
	}
}

// TestParse_Builtins: stdlib types + top-level functions bind as
// builtins rather than BindUnresolved missing_import.
func TestParse_Builtins(t *testing.T) {
	src := []byte(`func greet(names: [String]) {
    print(names.count)
    let xs: Array<Int> = []
    _ = xs
}
`)
	r := Parse("a.swift", src)
	for _, name := range []string{"print", "Array"} {
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

// TestParse_IfLetBinding: `if let x = foo() { use(x) }` emits `x`
// as KindLet scoped to the if-block body; the ref to `x` inside the
// block resolves to that decl.
func TestParse_IfLetBinding(t *testing.T) {
	src := []byte(`func g(opt: Int?) {
    if let x = opt {
        use(x)
    }
}
`)
	r := Parse("a.swift", src)
	xDecl := findDeclKind(r, "x", scope.KindLet)
	if xDecl == nil {
		t.Fatalf("x as KindLet missing; decls=%+v", r.Decls)
	}
	// The decl should live inside a ScopeBlock (the if-body), not in
	// the enclosing function scope.
	var xScope *scope.Scope
	for i := range r.Scopes {
		if r.Scopes[i].ID == xDecl.Scope {
			xScope = &r.Scopes[i]
			break
		}
	}
	if xScope == nil || xScope.Kind != scope.ScopeBlock {
		t.Errorf("x scope = %v, want ScopeBlock (if-body)", xScope)
	}
	// `use(x)` inside the block should resolve.
	resolved := false
	for _, ref := range refsNamed(r, "x") {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == xDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("ref(x) not resolved to if-let decl; refs=%+v", refsNamed(r, "x"))
	}
}

// TestParse_GuardLetBinding: `guard let y = foo() else { return };
// use(y)` emits `y` into the ENCLOSING scope (the function body), so
// the later `use(y)` — which lives outside the else-block — resolves.
func TestParse_GuardLetBinding(t *testing.T) {
	src := []byte(`func g(opt: Int?) {
    guard let y = opt else { return }
    use(y)
}
`)
	r := Parse("a.swift", src)
	yDecl := findDeclKind(r, "y", scope.KindLet)
	if yDecl == nil {
		t.Fatalf("y as KindLet missing; decls=%+v", r.Decls)
	}
	// y should be scoped to the enclosing function body, NOT the
	// guard's else-block. Find the function scope for g.
	var gScope scope.ScopeID
	for _, d := range r.Decls {
		if d.Name == "g" && d.Kind == scope.KindFunction {
			// Find the scope whose parent chain includes the file and
			// whose kind is ScopeFunction opened right after this decl.
			for _, s := range r.Scopes {
				if s.Kind == scope.ScopeFunction && s.Parent == d.Scope {
					gScope = s.ID
					break
				}
			}
		}
	}
	if gScope == 0 {
		t.Fatalf("could not find g's function scope; scopes=%+v", r.Scopes)
	}
	if yDecl.Scope != gScope {
		t.Errorf("y scope = %d, want enclosing function scope %d (guard binds into enclosing)", yDecl.Scope, gScope)
	}
	// The later `use(y)` should resolve.
	resolved := false
	for _, ref := range refsNamed(r, "y") {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == yDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("ref(y) not resolved to guard-let decl; refs=%+v", refsNamed(r, "y"))
	}
}

// TestParse_WhileLetBinding: `while let item = iter.next() { process(item) }`
// emits `item` as KindLet scoped to the while-body; the ref inside
// the loop resolves.
func TestParse_WhileLetBinding(t *testing.T) {
	src := []byte(`func g(iter: Iter) {
    while let item = iter.next() {
        process(item)
    }
}
`)
	r := Parse("a.swift", src)
	itemDecl := findDeclKind(r, "item", scope.KindLet)
	if itemDecl == nil {
		t.Fatalf("item as KindLet missing; decls=%+v", r.Decls)
	}
	var itemScope *scope.Scope
	for i := range r.Scopes {
		if r.Scopes[i].ID == itemDecl.Scope {
			itemScope = &r.Scopes[i]
			break
		}
	}
	if itemScope == nil || itemScope.Kind != scope.ScopeBlock {
		t.Errorf("item scope = %v, want ScopeBlock (while-body)", itemScope)
	}
	resolved := false
	for _, ref := range refsNamed(r, "item") {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == itemDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("ref(item) not resolved to while-let decl; refs=%+v", refsNamed(r, "item"))
	}
}

// TestParse_IfVarBinding: `if var x = opt { ... }` binds as KindVar.
func TestParse_IfVarBinding(t *testing.T) {
	src := []byte(`func g(opt: Int?) {
    if var x = opt {
        use(x)
    }
}
`)
	r := Parse("a.swift", src)
	xDecl := findDeclKind(r, "x", scope.KindVar)
	if xDecl == nil {
		t.Fatalf("x as KindVar missing; decls=%+v", r.Decls)
	}
	resolved := false
	for _, ref := range refsNamed(r, "x") {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == xDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("ref(x) not resolved to if-var decl; refs=%+v", refsNamed(r, "x"))
	}
}

// TestParse_IfLetCommaBindings: `if let x = a, let y = b { ... }`
// emits both x and y as KindLet scoped to the if-block body.
func TestParse_IfLetCommaBindings(t *testing.T) {
	src := []byte(`func g(a: Int?, b: Int?) {
    if let x = a, let y = b {
        use(x)
        use(y)
    }
}
`)
	r := Parse("a.swift", src)
	xDecl := findDeclKind(r, "x", scope.KindLet)
	yDecl := findDeclKind(r, "y", scope.KindLet)
	if xDecl == nil {
		t.Fatalf("x as KindLet missing; decls=%+v", r.Decls)
	}
	if yDecl == nil {
		t.Fatalf("y as KindLet missing; decls=%+v", r.Decls)
	}
	if xDecl.Scope != yDecl.Scope {
		t.Errorf("x scope=%d, y scope=%d — both should be in the if-block", xDecl.Scope, yDecl.Scope)
	}
	// Both bindings should live in a ScopeBlock.
	for _, d := range []*scope.Decl{xDecl, yDecl} {
		var sc *scope.Scope
		for i := range r.Scopes {
			if r.Scopes[i].ID == d.Scope {
				sc = &r.Scopes[i]
				break
			}
		}
		if sc == nil || sc.Kind != scope.ScopeBlock {
			t.Errorf("%s scope = %v, want ScopeBlock", d.Name, sc)
		}
	}
	// Both refs should resolve to their decls.
	for _, tc := range []struct {
		name string
		id   scope.DeclID
	}{{"x", xDecl.ID}, {"y", yDecl.ID}} {
		resolved := false
		for _, ref := range refsNamed(r, tc.name) {
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == tc.id {
				resolved = true
				break
			}
		}
		if !resolved {
			t.Errorf("ref(%s) not resolved; refs=%+v", tc.name, refsNamed(r, tc.name))
		}
	}
}

// TestParse_GuardVarBinding: `guard var y = opt else { return }; use(y)`
// emits y as KindVar into the enclosing scope.
func TestParse_GuardVarBinding(t *testing.T) {
	src := []byte(`func g(opt: Int?) {
    guard var y = opt else { return }
    use(y)
}
`)
	r := Parse("a.swift", src)
	yDecl := findDeclKind(r, "y", scope.KindVar)
	if yDecl == nil {
		t.Fatalf("y as KindVar missing; decls=%+v", r.Decls)
	}
	resolved := false
	for _, ref := range refsNamed(r, "y") {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == yDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("ref(y) not resolved to guard-var decl; refs=%+v", refsNamed(r, "y"))
	}
}
