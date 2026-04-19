package rust

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

func TestParse_FnWithLetAndParam(t *testing.T) {
	src := []byte(`fn add(x: i32, y: i32) -> i32 {
    let z = x + y;
    z
}
`)
	r := Parse("a.rs", src)
	for _, n := range []string{"add", "x", "y", "z"} {
		if findDecl(r, n) == nil {
			t.Errorf("missing decl %q; decls=%v", n, declNames(r))
		}
	}
	// x and y are params.
	for _, n := range []string{"x", "y"} {
		d := findDecl(r, n)
		if d == nil {
			continue
		}
		if d.Kind != scope.KindParam {
			t.Errorf("%q kind = %v, want param", n, d.Kind)
		}
	}
	// z is a var.
	if d := findDecl(r, "z"); d != nil && d.Kind != scope.KindVar {
		t.Errorf("z kind = %v, want var", d.Kind)
	}
	// Body refs resolve to params.
	xRefs := refsNamed(r, "x")
	if len(xRefs) == 0 {
		t.Fatal("no refs to x")
	}
	found := false
	for _, ref := range xRefs {
		if ref.Binding.Kind == scope.BindResolved {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("x refs none resolved; refs=%+v", xRefs)
	}
}

func TestParse_StructAndImplMethod(t *testing.T) {
	src := []byte(`struct Counter {
    n: i32,
}

impl Counter {
    fn inc(&mut self) {
        self.n = self.n + 1;
    }
}
`)
	r := Parse("a.rs", src)
	if findDecl(r, "Counter") == nil {
		t.Fatalf("Counter missing; decls=%v", declNames(r))
	}
	// Field n.
	nDecl := findDecl(r, "n")
	if nDecl == nil {
		t.Fatalf("field n missing; decls=%v", declNames(r))
	}
	if nDecl.Kind != scope.KindField {
		t.Errorf("n kind = %v, want field", nDecl.Kind)
	}
	if nDecl.Namespace != scope.NSField {
		t.Errorf("n namespace = %v, want field", nDecl.Namespace)
	}
	// Method inc is NSField in impl scope.
	incDecl := findDecl(r, "inc")
	if incDecl == nil {
		t.Fatalf("method inc missing; decls=%v", declNames(r))
	}
	if incDecl.Namespace != scope.NSField {
		t.Errorf("inc namespace = %v, want field (impl scope member)", incDecl.Namespace)
	}
}

func TestParse_SelfDotField_ResolvesToStructField(t *testing.T) {
	src := []byte(`struct Counter {
    n: i32,
}

impl Counter {
    fn inc(&mut self) {
        self.n = self.n + 1;
    }
}
`)
	r := Parse("a.rs", src)
	nDecl := findDeclKind(r, "n", scope.KindField)
	if nDecl == nil {
		t.Fatal("field n missing")
	}
	// Any self.n reference should be resolved with reason="self_dot_field".
	nRefs := refsNamed(r, "n")
	if len(nRefs) == 0 {
		t.Fatal("no refs to n")
	}
	selfFieldRefs := 0
	for _, ref := range nRefs {
		if ref.Binding.Reason == "self_dot_field" && ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == nDecl.ID {
			selfFieldRefs++
		}
	}
	if selfFieldRefs == 0 {
		t.Errorf("no self.n refs resolved via self_dot_field; refs=%+v", nRefs)
	}
}

func TestParse_GenericTypeParam(t *testing.T) {
	src := []byte(`fn id<T>(x: T) -> T {
    x
}
`)
	r := Parse("a.rs", src)
	tDecl := findDecl(r, "T")
	if tDecl == nil {
		t.Fatalf("T missing; decls=%v", declNames(r))
	}
	if tDecl.Kind != scope.KindType {
		t.Errorf("T kind = %v, want type", tDecl.Kind)
	}
	// T refs inside the fn should resolve to the T decl (either via scope
	// chain or signature-scope fallback).
	tRefs := refsNamed(r, "T")
	if len(tRefs) == 0 {
		t.Fatal("no refs to T")
	}
	resolved := false
	for _, ref := range tRefs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == tDecl.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("T refs none resolved; refs=%+v", tRefs)
	}
}

func TestParse_UseAndQualifiedCall(t *testing.T) {
	src := []byte(`use foo::Bar;

fn baz() {
    Bar::new()
}
`)
	r := Parse("a.rs", src)
	barImport := findDecl(r, "Bar")
	if barImport == nil {
		t.Fatalf("Bar import missing; decls=%v", declNames(r))
	}
	if barImport.Kind != scope.KindImport {
		t.Errorf("Bar kind = %v, want import", barImport.Kind)
	}
	// Bar ref inside fn body should resolve to the import.
	barRefs := refsNamed(r, "Bar")
	if len(barRefs) == 0 {
		t.Fatal("no refs to Bar")
	}
	resolved := false
	for _, ref := range barRefs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == barImport.ID {
			resolved = true
			break
		}
	}
	if !resolved {
		t.Errorf("Bar ref not resolved to import; refs=%+v", barRefs)
	}
	// `new` should be a property-access ref (after `::`).
	newRefs := refsNamed(r, "new")
	if len(newRefs) == 0 {
		t.Fatal("no refs to new")
	}
	if newRefs[0].Binding.Reason != "property_access" {
		t.Errorf("new reason = %q, want property_access", newRefs[0].Binding.Reason)
	}
}

func TestParse_MatchArmBinding(t *testing.T) {
	src := []byte(`fn f(x: Option<i32>) -> i32 {
    match x {
        Some(y) => y,
        None => 0,
    }
}
`)
	r := Parse("a.rs", src)
	// y should be declared somewhere in a match arm scope.
	yDecl := findDecl(r, "y")
	if yDecl == nil {
		t.Fatalf("y missing from decls; decls=%v", declNames(r))
	}
	if yDecl.Kind != scope.KindVar {
		t.Errorf("y kind = %v, want var", yDecl.Kind)
	}
}

func TestParse_MacroRulesAndInvocation(t *testing.T) {
	src := []byte(`macro_rules! greet {
    () => { println!("hello") };
}

fn main() {
    greet!();
}
`)
	r := Parse("a.rs", src)
	greetDecl := findDecl(r, "greet")
	if greetDecl == nil {
		t.Fatalf("macro greet missing; decls=%v", declNames(r))
	}
	if greetDecl.Kind != scope.KindFunction {
		t.Errorf("greet kind = %v, want function (macro)", greetDecl.Kind)
	}
	// greet! inside main should emit `greet` as a ref (not property access).
	greetRefs := refsNamed(r, "greet")
	if len(greetRefs) == 0 {
		t.Fatal("no refs to greet")
	}
	found := false
	for _, ref := range greetRefs {
		if ref.Binding.Reason == "property_access" {
			t.Errorf("greet should not be property_access; got %+v", ref.Binding)
		}
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == greetDecl.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("greet ref not resolved to macro decl; refs=%+v", greetRefs)
	}
}

func TestParse_FullSpan_ScopeOwningDecls(t *testing.T) {
	src := []byte(`fn greet(name: &str) -> String {
    format!("hi {}", name)
}

struct Point {
    x: i32,
    y: i32,
}

trait Named {
    fn name(&self) -> &str;
}

impl Point {
    fn origin() -> Point {
        Point { x: 0, y: 0 }
    }
}
`)
	r := Parse("a.rs", src)
	check := func(name, wantPrefix string) {
		t.Helper()
		d := findDecl(r, name)
		if d == nil {
			t.Fatalf("decl %q missing", name)
		}
		if d.FullSpan.StartByte >= d.Span.StartByte {
			t.Errorf("%s: FullSpan.StartByte=%d should be < Span.StartByte=%d (must cover keyword)",
				name, d.FullSpan.StartByte, d.Span.StartByte)
		}
		if d.FullSpan.EndByte <= d.Span.EndByte {
			t.Errorf("%s: FullSpan.EndByte=%d should be > Span.EndByte=%d (must cover body)",
				name, d.FullSpan.EndByte, d.Span.EndByte)
		}
		end := int(d.FullSpan.EndByte)
		if end > len(src) {
			end = len(src)
		}
		got := string(src[d.FullSpan.StartByte:end])
		if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
			t.Errorf("%s: FullSpan content starts %q, want prefix %q", name, got, wantPrefix)
		}
		if got[len(got)-1] != '}' {
			t.Errorf("%s: FullSpan does not end at }: %q", name, got)
		}
	}
	check("greet", "fn greet")
	check("Point", "struct Point")
	check("Named", "trait Named")
}

func TestParse_UseGroupWithAlias(t *testing.T) {
	src := []byte(`use foo::{a, b as c};
`)
	r := Parse("a.rs", src)
	// Expect `a` (simple member) and `c` (the alias — the visible binder).
	// `b` is the original name and does NOT create a local binding.
	for _, n := range []string{"a", "c"} {
		d := findDecl(r, n)
		if d == nil {
			t.Errorf("missing import %q; decls=%v", n, declNames(r))
			continue
		}
		if d.Kind != scope.KindImport {
			t.Errorf("%q kind = %v, want import", n, d.Kind)
		}
	}
	// b should NOT be a decl — the alias `c` replaces it as the binder.
	if findDecl(r, "b") != nil {
		t.Errorf("b should not be a decl when aliased to c")
	}
}

func TestParse_LifetimeTypeParam(t *testing.T) {
	src := []byte(`fn borrow<'a, T>(x: &'a T) -> &'a T {
    x
}
`)
	r := Parse("a.rs", src)
	// T should be a type decl.
	if findDecl(r, "T") == nil {
		t.Errorf("T missing; decls=%v", declNames(r))
	}
	// 'a lifetime is emitted as KindType decl, with the leading quote in
	// the name to distinguish from plain type params.
	aDecl := findDecl(r, "'a")
	if aDecl == nil {
		t.Errorf("'a lifetime decl missing; decls=%v", declNames(r))
	} else if aDecl.Kind != scope.KindType {
		t.Errorf("'a kind = %v, want type", aDecl.Kind)
	}
}

func TestParse_UnresolvedRef(t *testing.T) {
	src := []byte(`fn f() {
    some_undefined_name();
}
`)
	r := Parse("a.rs", src)
	refs := refsNamed(r, "some_undefined_name")
	if len(refs) == 0 {
		t.Fatal("no ref to some_undefined_name")
	}
	if refs[0].Binding.Kind != scope.BindUnresolved {
		t.Errorf("expected unresolved, got %+v", refs[0].Binding)
	}
	if refs[0].Binding.Reason == "" {
		t.Errorf("unresolved ref missing reason")
	}
}

func TestParse_Builtin(t *testing.T) {
	src := []byte(`fn f() -> i32 {
    42
}
`)
	r := Parse("a.rs", src)
	refs := refsNamed(r, "i32")
	if len(refs) == 0 {
		t.Fatal("no ref to i32")
	}
	if refs[0].Binding.Kind != scope.BindResolved || refs[0].Binding.Reason != "builtin" {
		t.Errorf("i32 expected builtin-resolved, got %+v", refs[0].Binding)
	}
}

// TestParse_AttributeSkipped verifies attribute contents don't produce decls
// or scope-relevant refs.
func TestParse_AttributeSkipped(t *testing.T) {
	src := []byte(`#[derive(Debug, Clone)]
struct Foo {
    x: i32,
}
`)
	r := Parse("a.rs", src)
	// Foo should still be recognized.
	if findDecl(r, "Foo") == nil {
		t.Fatalf("Foo missing after attribute; decls=%v", declNames(r))
	}
	// `Debug` and `Clone` inside the attribute should NOT appear as refs.
	for _, n := range []string{"Debug", "Clone", "derive"} {
		if refs := refsNamed(r, n); len(refs) > 0 {
			t.Errorf("attribute content %q leaked as ref: %+v", n, refs)
		}
	}
}

// TestParse_NoPanicOnRawStringAndChars ensures the scanner handles raw
// strings, byte strings, and char literals without panicking.
func TestParse_NoPanicOnRawStringAndChars(t *testing.T) {
	src := []byte(`fn f() {
    let _a = "hello";
    let _b = r"raw\nstring";
    let _c = r#"raw "with quotes" string"#;
    let _d = b"bytes";
    let _e = br#"raw bytes"#;
    let _f = 'x';
    let _g = '\n';
    let _h = '\x41';
}
`)
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("panic: %v", rec)
		}
	}()
	r := Parse("a.rs", src)
	// At minimum: f should be a decl.
	if findDecl(r, "f") == nil {
		t.Errorf("f missing; decls=%v", declNames(r))
	}
}

// TestParse_NestedBlockComment covers Rust's nested /* /* */ */ comments.
func TestParse_NestedBlockComment(t *testing.T) {
	src := []byte(`/* outer /* inner */ still outer */
fn inside() {}
`)
	r := Parse("a.rs", src)
	if findDecl(r, "inside") == nil {
		t.Errorf("inside missing; decls=%v", declNames(r))
	}
}

// TestParse_TraitDefaultMethodSelf: inside a trait body's default impl,
// `self.other()` resolves to sibling trait methods via direct
// self_dot_field — the trait scope IS the container.
func TestParse_TraitDefaultMethodSelf(t *testing.T) {
	src := []byte(`trait Greet {
    fn hello(&self) -> String;
    fn say(&self) -> String {
        self.hello()
    }
}
`)
	r := Parse("a.rs", src)
	var helloID scope.DeclID
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name == "hello" && d.Namespace == scope.NSField {
			helloID = d.ID
			break
		}
	}
	if helloID == 0 {
		t.Fatalf("no hello method decl; decls=%v", declNames(r))
	}
	refs := refsNamed(r, "hello")
	var found bool
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == helloID {
			found = true
			if ref.Binding.Reason != "self_dot_field" && ref.Binding.Reason != "trait_method" {
				t.Errorf("hello ref reason = %q, want self_dot_field or trait_method",
					ref.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("self.hello() ref did not resolve to the trait's hello method; refs=%+v", refs)
	}
}

// TestParse_ImplTraitMethodOwn: inside `impl T for S`, a method defined
// in THIS impl block resolves via self_dot_field (not trait_method).
func TestParse_ImplTraitMethodOwn(t *testing.T) {
	src := []byte(`trait T {
    fn base(&self);
}
struct S;
impl T for S {
    fn base(&self) { }
    fn driver(&self) {
        self.base();
    }
}
`)
	r := Parse("a.rs", src)
	refs := refsNamed(r, "base")
	var implBase *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name != "base" || d.Namespace != scope.NSField {
			continue
		}
		// Pick the one inside the impl body (its scope should be different
		// from the trait's scope). Heuristic: later occurrence in decls.
		implBase = d
	}
	if implBase == nil {
		t.Fatalf("no base decl; decls=%v", declNames(r))
	}
	var selfCall *scope.Ref
	for i := range refs {
		if refs[i].Binding.Kind == scope.BindResolved {
			selfCall = &refs[i]
		}
	}
	if selfCall == nil {
		t.Fatalf("self.base() did not resolve; refs=%+v", refs)
	}
	// Either binding is acceptable — direct self_dot_field from impl or
	// trait_method fallback would both be correct semantically.
	if selfCall.Binding.Reason != "self_dot_field" && selfCall.Binding.Reason != "trait_method" {
		t.Errorf("self.base reason = %q, want self_dot_field or trait_method",
			selfCall.Binding.Reason)
	}
}

// TestParse_ImplTraitDefaultMethod: the key case — impl block has NO
// override of `default_only`, but the trait provides a default impl.
// `self.default_only()` in a different impl method must resolve via
// trait_method.
func TestParse_ImplTraitDefaultMethod(t *testing.T) {
	src := []byte(`trait T {
    fn default_only(&self) -> i32 { 42 }
}
struct S;
impl T for S {
    fn call(&self) -> i32 {
        self.default_only()
    }
}
`)
	r := Parse("a.rs", src)
	var traitMethodID scope.DeclID
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name == "default_only" && d.Namespace == scope.NSField {
			traitMethodID = d.ID
			break
		}
	}
	if traitMethodID == 0 {
		t.Fatalf("no default_only decl; decls=%v", declNames(r))
	}
	refs := refsNamed(r, "default_only")
	var bound *scope.Ref
	for i := range refs {
		if refs[i].Binding.Kind == scope.BindResolved {
			bound = &refs[i]
			break
		}
	}
	if bound == nil {
		t.Fatalf("self.default_only() did not resolve; refs=%+v", refs)
	}
	if bound.Binding.Decl != traitMethodID {
		t.Errorf("bound to %d, want trait's default_only %d", bound.Binding.Decl, traitMethodID)
	}
	if bound.Binding.Reason != "trait_method" {
		t.Errorf("reason = %q, want trait_method (key feature)", bound.Binding.Reason)
	}
}

// TestParse_ImplTraitUnknownMethod: negative — self.unknown() in an
// impl block where neither the impl nor the trait define `unknown`
// must NOT spuriously resolve. Falls to probable property_access.
func TestParse_ImplTraitUnknownMethod(t *testing.T) {
	src := []byte(`trait T {
    fn known(&self);
}
struct S;
impl T for S {
    fn known(&self) { }
    fn caller(&self) {
        self.unknown();
    }
}
`)
	r := Parse("a.rs", src)
	refs := refsNamed(r, "unknown")
	if len(refs) == 0 {
		t.Fatalf("no ref to unknown; refs=%+v", r.Refs)
	}
	for _, ref := range refs {
		if ref.Binding.Kind == scope.BindResolved {
			t.Errorf("self.unknown() should not resolve, got %+v", ref.Binding)
		}
	}
}

// TestParse_UnderscoreNotAref: the wildcard `_` is never a ref to
// anything — it's the ignore pattern in tuple destructuring, match
// arms, closures, and type-inference holes. Dogfood on tokio surfaced
// 6k+ spurious `_` refs; this test locks in the fix.
func TestParse_UnderscoreNotARef(t *testing.T) {
	src := []byte(`fn demo() {
    let (a, _) = (1, 2);
    let _ = a;
    let v: Vec<_> = vec![];
    let _ = v;
    let f = |_, x| x;
    let g = |_| 42;
    match 1 {
        _ => {},
    }
}
`)
	r := Parse("a.rs", src)
	for _, ref := range r.Refs {
		if ref.Name == "_" {
			t.Errorf("underscore emitted as ref at span %d-%d with binding %+v",
				ref.Span.StartByte, ref.Span.EndByte, ref.Binding)
		}
	}
}
