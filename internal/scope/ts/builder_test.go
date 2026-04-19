package ts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// findDecl returns the first Decl with the given name, or nil.
func findDecl(r *scope.Result, name string) *scope.Decl {
	for i := range r.Decls {
		if r.Decls[i].Name == name {
			return &r.Decls[i]
		}
	}
	return nil
}

// findRef returns the first Ref with the given name, or nil.
func findRef(r *scope.Result, name string) *scope.Ref {
	for i := range r.Refs {
		if r.Refs[i].Name == name {
			return &r.Refs[i]
		}
	}
	return nil
}

// refsNamed returns every Ref with the given name, in source order.
func refsNamed(r *scope.Result, name string) []scope.Ref {
	var out []scope.Ref
	for _, ref := range r.Refs {
		if ref.Name == name {
			out = append(out, ref)
		}
	}
	return out
}

func TestParse_TopLevelConst(t *testing.T) {
	src := []byte(`const x = 42
const y = x + 1
`)
	r := Parse("a.ts", src)

	dx := findDecl(r, "x")
	if dx == nil {
		t.Fatalf("decl x not found; decls=%v", r.Decls)
	}
	if dx.Kind != scope.KindConst {
		t.Errorf("x kind = %v, want const", dx.Kind)
	}

	// The `x` used on the RHS of y should bind to the const decl.
	refsOfX := refsNamed(r, "x")
	if len(refsOfX) != 1 {
		t.Fatalf("expected 1 ref to x, got %d (%v)", len(refsOfX), refsOfX)
	}
	if refsOfX[0].Binding.Kind != scope.BindResolved {
		t.Errorf("x ref unresolved: %+v", refsOfX[0].Binding)
	}
	if refsOfX[0].Binding.Decl != dx.ID {
		t.Errorf("x ref bound to wrong decl")
	}
}

func TestParse_FunctionAndParams(t *testing.T) {
	src := []byte(`function add(a, b) {
  return a + b
}
`)
	r := Parse("a.ts", src)
	if findDecl(r, "add") == nil {
		t.Fatalf("function add not declared; decls=%v", r.Decls)
	}
	// Params should be emitted as KindParam decls in the function scope.
	da := findDecl(r, "a")
	db := findDecl(r, "b")
	if da == nil || db == nil {
		t.Fatalf("param decls missing; decls=%v", declNames(r))
	}
	if da.Kind != scope.KindParam || db.Kind != scope.KindParam {
		t.Errorf("param kinds = %v, %v; want param", da.Kind, db.Kind)
	}
	// Refs in body should bind to param decls.
	refsA := refsNamed(r, "a")
	if len(refsA) == 0 || refsA[0].Binding.Kind != scope.BindResolved ||
		refsA[0].Binding.Decl != da.ID {
		t.Errorf("a ref did not resolve to param decl: %+v", refsA)
	}
	refsB := refsNamed(r, "b")
	if len(refsB) == 0 || refsB[0].Binding.Kind != scope.BindResolved ||
		refsB[0].Binding.Decl != db.ID {
		t.Errorf("b ref did not resolve to param decl: %+v", refsB)
	}
}

func TestParse_ClassMethods(t *testing.T) {
	src := []byte(`class Processor {
  constructor(initial) {
    this.value = initial
  }
  process(input: number, factor = 2) {
    return input * factor
  }
}
const p = new Processor(10)
`)
	r := Parse("a.ts", src)

	// Method decls should appear with kind=method.
	for _, name := range []string{"constructor", "process"} {
		d := findDecl(r, name)
		if d == nil {
			t.Fatalf("method %q missing; decls=%v", name, declNames(r))
		}
		if d.Kind != scope.KindMethod {
			t.Errorf("%q kind = %v, want method", name, d.Kind)
		}
	}

	// Method params should be param decls, and refs in bodies should bind.
	for _, pname := range []string{"initial", "input", "factor"} {
		d := findDecl(r, pname)
		if d == nil {
			t.Errorf("param %q missing", pname)
			continue
		}
		if d.Kind != scope.KindParam {
			t.Errorf("%q kind = %v, want param", pname, d.Kind)
		}
	}

	// `input` in the body should resolve to the process param.
	inputDecl := findDecl(r, "input")
	inputRefs := refsNamed(r, "input")
	if len(inputRefs) == 0 || inputRefs[0].Binding.Decl != inputDecl.ID {
		t.Errorf("input ref did not bind to method param")
	}
}

func TestParse_TypedParams(t *testing.T) {
	// Type annotations and default values should not confuse param detection.
	src := []byte(`function greet(name: string, count: number = 1) {
  return name + count
}
`)
	r := Parse("a.ts", src)
	if findDecl(r, "name") == nil || findDecl(r, "count") == nil {
		t.Fatalf("typed param decls missing; decls=%v", declNames(r))
	}
	// `string` and `number` inside the param list are NOT params.
	if d := findDecl(r, "string"); d != nil && d.Kind == scope.KindParam {
		t.Errorf("`string` leaked as a param decl")
	}
	if d := findDecl(r, "number"); d != nil && d.Kind == scope.KindParam {
		t.Errorf("`number` leaked as a param decl")
	}
}

func TestParse_Class(t *testing.T) {
	src := []byte(`class Foo {
  constructor() {}
  bar() { return 1 }
}
const f = new Foo()
`)
	r := Parse("a.ts", src)
	if findDecl(r, "Foo") == nil {
		t.Fatalf("class Foo not declared")
	}
	// `new Foo()` should produce a resolved ref to the class.
	refs := refsNamed(r, "Foo")
	if len(refs) == 0 {
		t.Fatalf("no refs to Foo")
	}
	if refs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("Foo ref unresolved: %+v", refs[0].Binding)
	}
}

func TestParse_Import(t *testing.T) {
	src := []byte(`import { Schema, type Config as Cfg } from './schema'
import defaultExport from './other'
import * as util from './util'
const s = new Schema()
const c: Cfg = defaultExport
util.run()
`)
	r := Parse("a.ts", src)
	for _, name := range []string{"Schema", "Cfg", "defaultExport", "util"} {
		if findDecl(r, name) == nil {
			t.Errorf("import decl %q not found; decls=%v", name, declNames(r))
		}
	}
	// Schema ref resolves to the imported decl.
	refs := refsNamed(r, "Schema")
	if len(refs) == 0 || refs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("Schema ref not resolved: %+v", refs)
	}
	// util.run — util resolves; run is a property-access ref (probable,
	// name-only, no scope-chain resolution).
	utilRefs := refsNamed(r, "util")
	if len(utilRefs) == 0 || utilRefs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("util ref not resolved")
	}
	runRefs := refsNamed(r, "run")
	if len(runRefs) == 0 {
		t.Errorf("property-access 'run' should be emitted as a probable ref")
	} else if runRefs[0].Binding.Kind != scope.BindProbable ||
		runRefs[0].Binding.Reason != "property_access" {
		t.Errorf("run ref should be BindProbable/property_access, got %+v",
			runRefs[0].Binding)
	}
}

func TestParse_StringLiteralsAndComments(t *testing.T) {
	// Identifiers inside strings and comments must not become Refs or Decls.
	src := []byte("const greeting = 'hello world'\n" +
		"// const notReal = 1\n" +
		"/* function alsoNotReal() {} */\n" +
		"const real = 1\n" +
		"const template = `value is ${real}`\n")
	r := Parse("a.ts", src)

	for _, bogus := range []string{"hello", "world", "notReal", "alsoNotReal"} {
		if findDecl(r, bogus) != nil {
			t.Errorf("bogus decl %q leaked from string/comment", bogus)
		}
		if findRef(r, bogus) != nil {
			t.Errorf("bogus ref %q leaked from string/comment", bogus)
		}
	}
	if findDecl(r, "real") == nil {
		t.Errorf("real const not found")
	}
	// v1 limitation: refs inside template-literal ${...} expressions are
	// not emitted — skipTemplateExpr walks them without re-entering the
	// main parser. Documented in package doc; fix in v2.
}

func TestParse_BlockShadowing(t *testing.T) {
	src := []byte(`const x = 1
{
  const x = 2
  const y = x
}
const z = x
`)
	r := Parse("a.ts", src)
	// There should be two x decls (outer and inner block).
	xDecls := 0
	for _, d := range r.Decls {
		if d.Name == "x" {
			xDecls++
		}
	}
	if xDecls != 2 {
		t.Fatalf("expected 2 x decls, got %d", xDecls)
	}
	// Find the ref `x` in `const y = x` — should bind to the inner x.
	// Find the ref `x` in `const z = x` — should bind to the outer x.
	// Both should be BindResolved (bindings to different decls).
	refs := refsNamed(r, "x")
	if len(refs) != 2 {
		t.Fatalf("expected 2 x refs, got %d (%v)", len(refs), refs)
	}
	for _, ref := range refs {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("x ref unresolved: %+v", ref)
		}
	}
	// The two refs should bind to DIFFERENT decls (scope-aware).
	if refs[0].Binding.Decl == refs[1].Binding.Decl {
		t.Errorf("both x refs bound to same decl — scope shadowing broken")
	}
}

func TestParse_UnresolvedRef(t *testing.T) {
	src := []byte(`const x = undefined_global
`)
	r := Parse("a.ts", src)
	ref := findRef(r, "undefined_global")
	if ref == nil {
		t.Fatal("undefined_global ref missing")
	}
	if ref.Binding.Kind != scope.BindUnresolved {
		t.Errorf("expected Unresolved, got %+v", ref.Binding)
	}
	if ref.Binding.Reason == "" {
		t.Errorf("unresolved ref missing reason")
	}
}

func TestParse_DeclIDStableAcrossRebuild(t *testing.T) {
	// Content-hashed DeclID: same input -> same ID.
	src := []byte(`const x = 1
export function foo() {}
class Bar {}
`)
	r1 := Parse("a.ts", src)
	r2 := Parse("a.ts", src)

	for _, name := range []string{"x", "foo", "Bar"} {
		d1 := findDecl(r1, name)
		d2 := findDecl(r2, name)
		if d1 == nil || d2 == nil {
			t.Fatalf("%s missing in one of the parses", name)
		}
		if d1.ID != d2.ID {
			t.Errorf("%s DeclID unstable: %x vs %x", name, d1.ID, d2.ID)
		}
	}
}

func TestParse_DeclIDDiffersAcrossFiles(t *testing.T) {
	// Different files, same name -> different DeclID (v1 uses file path
	// as canonical path).
	src := []byte(`function foo() {}`)
	r1 := Parse("a.ts", src)
	r2 := Parse("b.ts", src)
	d1 := findDecl(r1, "foo")
	d2 := findDecl(r2, "foo")
	if d1 == nil || d2 == nil {
		t.Fatal("foo missing in one parse")
	}
	if d1.ID == d2.ID {
		t.Errorf("DeclID should differ across files: both %x", d1.ID)
	}
}


func TestParse_ArrowFunctionParams(t *testing.T) {
	src := []byte(`const double = (x) => {
  return x * 2
}
const add = (a, b) => {
  return a + b
}
`)
	r := Parse("a.ts", src)

	xDecl := findDecl(r, "x")
	if xDecl == nil || xDecl.Kind != scope.KindParam {
		t.Fatalf("arrow param x missing or wrong kind; decls=%v", declNames(r))
	}
	xRefs := refsNamed(r, "x")
	if len(xRefs) == 0 || xRefs[0].Binding.Kind != scope.BindResolved || xRefs[0].Binding.Decl != xDecl.ID {
		t.Errorf("x ref did not resolve to arrow param: %+v", xRefs)
	}
	aDecl := findDecl(r, "a")
	bDecl := findDecl(r, "b")
	if aDecl == nil || aDecl.Kind != scope.KindParam {
		t.Errorf("arrow param a missing or wrong kind")
	}
	if bDecl == nil || bDecl.Kind != scope.KindParam {
		t.Errorf("arrow param b missing or wrong kind")
	}
}

func TestParse_RestParams(t *testing.T) {
	// `...args` should emit `args` as a param decl, not drop it.
	src := []byte(`function collect(first, ...rest) {
  return [first, rest]
}
`)
	r := Parse("a.ts", src)
	first := findDecl(r, "first")
	rest := findDecl(r, "rest")
	if first == nil || first.Kind != scope.KindParam {
		t.Errorf("first param missing")
	}
	if rest == nil || rest.Kind != scope.KindParam {
		t.Errorf("rest param missing; decls=%v", declNames(r))
	}
	if refs := refsNamed(r, "rest"); len(refs) == 0 || refs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("rest ref unresolved: %+v", refs)
	}
}

func TestParse_ExpressionBodyArrowDoesNotLeakParams(t *testing.T) {
	// Expression-body arrows (no { body }) must not leak pending params
	// into the next function scope that opens.
	src := []byte(`const inc = (n) => n + 1
function later(x) {
  return x
}
`)
	r := Parse("a.ts", src)
	xRefs := refsNamed(r, "x")
	xDecl := findDecl(r, "x")
	if xDecl == nil || xDecl.Kind != scope.KindParam {
		t.Fatal("later param x missing")
	}
	if len(xRefs) == 0 || xRefs[0].Binding.Decl != xDecl.ID {
		t.Errorf("x ref did not bind to later's param")
	}
}

func TestParse_ExpressionBodyArrowScopesParams(t *testing.T) {
	// Expression-body arrows must open their own function scope so
	// params do not leak into the enclosing file scope and body refs
	// bind to them.
	src := []byte(`const add = (a, b) => a + b
`)
	r := Parse("a.ts", src)

	// The arrow params a, b must be scoped to the arrow (not file scope).
	fileScopeID := scope.ScopeID(1)
	aDecl := findDecl(r, "a")
	bDecl := findDecl(r, "b")
	if aDecl == nil || aDecl.Kind != scope.KindParam {
		t.Fatalf("arrow param a missing or wrong kind; decls=%v", declNames(r))
	}
	if bDecl == nil || bDecl.Kind != scope.KindParam {
		t.Fatalf("arrow param b missing or wrong kind; decls=%v", declNames(r))
	}
	if aDecl.Scope == fileScopeID {
		t.Errorf("arrow param a leaked into file scope (scope=%d)", aDecl.Scope)
	}
	if bDecl.Scope == fileScopeID {
		t.Errorf("arrow param b leaked into file scope (scope=%d)", bDecl.Scope)
	}
	if aDecl.Scope != bDecl.Scope {
		t.Errorf("a and b should share the arrow scope; got %d vs %d", aDecl.Scope, bDecl.Scope)
	}

	// Both refs in the body (`a + b`) must bind to the arrow params.
	aRefs := refsNamed(r, "a")
	bRefs := refsNamed(r, "b")
	if len(aRefs) == 0 || aRefs[0].Binding.Kind != scope.BindResolved || aRefs[0].Binding.Decl != aDecl.ID {
		t.Errorf("a ref did not resolve to arrow param: %+v", aRefs)
	}
	if len(bRefs) == 0 || bRefs[0].Binding.Kind != scope.BindResolved || bRefs[0].Binding.Decl != bDecl.ID {
		t.Errorf("b ref did not resolve to arrow param: %+v", bRefs)
	}
}

func TestParse_BareIdentArrowScopesParam(t *testing.T) {
	// `x => x + 1` (no parens around the single param) must also open
	// a function scope whose body sees x as a param, not a ref into the
	// enclosing scope.
	src := []byte(`const inc = x => x + 1
`)
	r := Parse("a.ts", src)

	xDecl := findDecl(r, "x")
	if xDecl == nil || xDecl.Kind != scope.KindParam {
		t.Fatalf("bare-ident arrow param x missing or wrong kind; decls=%v", declNames(r))
	}
	fileScopeID := scope.ScopeID(1)
	if xDecl.Scope == fileScopeID {
		t.Errorf("bare-ident arrow param x leaked into file scope")
	}
	// The body `x + 1` should bind x to the param.
	xRefs := refsNamed(r, "x")
	if len(xRefs) == 0 {
		t.Fatal("no x refs in body")
	}
	if xRefs[0].Binding.Kind != scope.BindResolved || xRefs[0].Binding.Decl != xDecl.ID {
		t.Errorf("body x ref did not resolve to bare-ident arrow param: %+v", xRefs[0])
	}
}

func TestParse_BlockBodyArrowShadowsOuter(t *testing.T) {
	// Block-body arrow opens a function scope; `const outer` inside
	// shadows the file-scope `const outer`. The inner `return outer`
	// must bind to the inner decl, not the outer.
	src := []byte(`const outer = 1
const f = () => {
  const outer = 2
  return outer
}
`)
	r := Parse("a.ts", src)

	// There must be two `outer` decls: one at file scope, one inside f.
	var outerDecls []scope.Decl
	for _, d := range r.Decls {
		if d.Name == "outer" {
			outerDecls = append(outerDecls, d)
		}
	}
	if len(outerDecls) != 2 {
		t.Fatalf("expected 2 outer decls (outer shadow), got %d: %v", len(outerDecls), declNames(r))
	}
	// The inner `outer` must live in a non-file scope.
	fileScopeID := scope.ScopeID(1)
	var innerOuter *scope.Decl
	for i := range outerDecls {
		if outerDecls[i].Scope != fileScopeID {
			innerOuter = &outerDecls[i]
		}
	}
	if innerOuter == nil {
		t.Fatal("inner outer decl is at file scope; arrow did not open a scope")
	}

	// The `return outer` ref must bind to the inner decl.
	refs := refsNamed(r, "outer")
	if len(refs) == 0 {
		t.Fatal("no outer refs")
	}
	// The ref we care about is the last one (`return outer`).
	last := refs[len(refs)-1]
	if last.Binding.Kind != scope.BindResolved {
		t.Fatalf("outer ref not resolved: %+v", last)
	}
	if last.Binding.Decl != innerOuter.ID {
		t.Errorf("return outer bound to wrong decl: got %x, want inner %x",
			last.Binding.Decl, innerOuter.ID)
	}
}

func TestConsumerAPI_RefsToDecl(t *testing.T) {
	// End-to-end: parse a file, look up a file-scope decl by name, fetch
	// every ref binding to it. Shape of the Tier 1 "focus --refs-to" path.
	src := []byte(`export function compute(x) {
  return x * 2
}
const a = compute(1)
const b = compute(2)
const c = compute(3)
`)
	r := Parse("a.ts", src)

	d := scope.FindDeclByName(r, "compute")
	if d == nil {
		t.Fatal("compute decl not found at file scope")
	}
	refs := scope.RefsToDecl(r, d.ID)
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs to compute, got %d (%v)",
			len(refs), refs)
	}
	for _, ref := range refs {
		if ref.Name != "compute" {
			t.Errorf("ref.Name = %q, want compute", ref.Name)
		}
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("expected Resolved, got %+v", ref.Binding)
		}
	}
}



func TestParse_GenericTypeParams(t *testing.T) {
	// Generic type params on functions should emit as KindType decls in the
	// function scope, so body refs to them bind.
	src := []byte(`function identity<T>(x: T): T {
  const y: T = x
  return y
}
`)
	r := Parse("a.ts", src)

	tDecl := findDecl(r, "T")
	if tDecl == nil {
		t.Fatalf("generic T decl missing; decls=%v", declNames(r))
	}
	if tDecl.Kind != scope.KindType {
		t.Errorf("T kind = %v, want type", tDecl.Kind)
	}

	// All T refs in the body should resolve to the type-param decl.
	tRefs := refsNamed(r, "T")
	if len(tRefs) == 0 {
		t.Fatal("no refs to T")
	}
	resolved := 0
	for _, ref := range tRefs {
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == tDecl.ID {
			resolved++
		}
	}
	if resolved == 0 {
		t.Errorf("no T ref resolved to type param; refs=%+v", tRefs)
	}
}

func TestParse_GenericMethod(t *testing.T) {
	// A generic method in a class body should still be detected as a method
	// (the <T> shouldn't defeat the ( lookahead).
	src := []byte(`class Box {
  unwrap<T>(x: T): T {
    return x
  }
}
`)
	r := Parse("a.ts", src)
	m := findDecl(r, "unwrap")
	if m == nil {
		t.Fatalf("generic method unwrap not detected; decls=%v", declNames(r))
	}
	if m.Kind != scope.KindMethod {
		t.Errorf("unwrap kind = %v, want method", m.Kind)
	}
	if findDecl(r, "x") == nil {
		t.Error("method param x missing")
	}
	if findDecl(r, "T") == nil {
		t.Error("method generic T missing")
	}
}

func TestParse_GenericClass(t *testing.T) {
	src := []byte(`class Container<T> {
  get(): T { return this.value as T }
}
`)
	r := Parse("a.ts", src)
	if findDecl(r, "Container") == nil {
		t.Fatal("class Container missing")
	}
	tDecl := findDecl(r, "T")
	if tDecl == nil || tDecl.Kind != scope.KindType {
		t.Errorf("class generic T missing or wrong kind; decls=%v", declNames(r))
	}
}



func TestParse_Destructuring(t *testing.T) {
	// Object destructuring in var decls should emit each name as a decl
	// in the surrounding scope, NOT in a new block scope. Refs in a
	// later statement should resolve.
	src := []byte(`const issue = { code: "x", format: "y" }
const { code, format } = issue
const msg = code + format
`)
	r := Parse("a.ts", src)

	for _, name := range []string{"code", "format"} {
		d := findDecl(r, name)
		if d == nil {
			t.Fatalf("destructured decl %q missing; decls=%v", name, declNames(r))
		}
		if d.Kind != scope.KindConst {
			t.Errorf("%q kind = %v, want const", name, d.Kind)
		}
		if d.Scope != 1 {
			t.Errorf("%q scope = %d, want 1 (file)", name, d.Scope)
		}
	}
	// Refs from `const msg = code + format` must resolve.
	for _, name := range []string{"code", "format"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("no refs to %q", name)
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
			t.Errorf("refs to %q never resolved: %+v", name, refs)
		}
	}
}

func TestParse_DestructuringInForOf(t *testing.T) {
	// Common pattern: for (const { a, b } of items) { body using a, b }
	src := []byte(`for (const { schema, input } of items) {
  schema.process(input)
}
`)
	r := Parse("a.ts", src)
	for _, name := range []string{"schema", "input"} {
		d := findDecl(r, name)
		if d == nil {
			t.Fatalf("destructured loop var %q missing", name)
		}
	}
	// Refs in the body should resolve to the destructured names.
	for _, name := range []string{"schema", "input"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("no body refs to %q", name)
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
			t.Errorf("refs to %q never resolved in body: %+v", name, refs)
		}
	}
}

func TestParse_ArrayDestructuring(t *testing.T) {
	src := []byte(`const [first, second] = [1, 2]
const sum = first + second
`)
	r := Parse("a.ts", src)
	if findDecl(r, "first") == nil || findDecl(r, "second") == nil {
		t.Fatalf("array destructuring missing; decls=%v", declNames(r))
	}
}

func TestParse_InterfaceFields(t *testing.T) {
	src := []byte(`interface Issue {
  readonly code: string
  format?: string
  input: unknown
}
`)
	r := Parse("a.ts", src)
	for _, name := range []string{"code", "format", "input"} {
		d := findDecl(r, name)
		if d == nil {
			t.Fatalf("interface field %q missing; decls=%v", name, declNames(r))
		}
		if d.Kind != scope.KindField {
			t.Errorf("%q kind = %v, want field", name, d.Kind)
		}
	}
}

func TestParse_ExportedTypeDecl(t *testing.T) {
	// `export type X = ...` must recognize X as a KindType decl.
	src := []byte(`export type Schema = { kind: string }
export interface Config { debug: boolean }
export class Runner {}
`)
	r := Parse("a.ts", src)
	for _, name := range []string{"Schema", "Config", "Runner"} {
		d := findDecl(r, name)
		if d == nil {
			t.Errorf("exported decl %q missing; decls=%v", name, declNames(r))
		}
	}
}

func TestParse_BuiltinResolution(t *testing.T) {
	src := []byte(`const arr: Array<string> = []
const p = Promise.resolve(arr)
`)
	r := Parse("a.ts", src)
	for _, name := range []string{"Array", "Promise"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("no refs to builtin %q", name)
			continue
		}
		if refs[0].Binding.Kind != scope.BindResolved {
			t.Errorf("%q not resolved: %+v", name, refs[0].Binding)
		}
		if refs[0].Binding.Reason != "builtin" {
			t.Errorf("%q resolved but reason = %q, want \"builtin\"", name, refs[0].Binding.Reason)
		}
	}
}

func TestDogfood_InFileResolutionRate(t *testing.T) {
	dir := os.Getenv("EDR_SCOPE_DOGFOOD_DIR")
	if dir == "" {
		t.Skip()
	}
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".ts") && !strings.HasSuffix(path, ".d.ts") {
			files = append(files, path)
		}
		return nil
	})

	var totalRefs, resolved, unresolvedLocal, unresolvedExternal int
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		r := Parse(f, src)
		localNames := map[string]bool{}
		for _, d := range r.Decls {
			localNames[d.Name] = true
		}
		for _, ref := range r.Refs {
			totalRefs++
			if ref.Binding.Kind == scope.BindResolved {
				resolved++
				continue
			}
			if localNames[ref.Name] {
				// Ref name matches a local decl but scope walk failed
				unresolvedLocal++
			} else {
				unresolvedExternal++
			}
		}
	}
	t.Logf("total refs: %d", totalRefs)
	t.Logf("resolved (incl. builtins): %d (%.1f%%)", resolved, 100*float64(resolved)/float64(totalRefs))
	t.Logf("unresolved, name IS declared in file (scope-walk miss): %d (%.1f%%)",
		unresolvedLocal, 100*float64(unresolvedLocal)/float64(totalRefs))
	t.Logf("unresolved, name NOT declared in file (external import/etc): %d (%.1f%%)",
		unresolvedExternal, 100*float64(unresolvedExternal)/float64(totalRefs))
	resolvable := resolved + unresolvedLocal
	if unresolvedExternal > 0 {
		t.Logf("in-file resolution rate: %d/%d = %.1f%% of refs whose target exists in-file",
			resolved, resolvable, 100*float64(resolved)/float64(resolvable))
	}
}



func TestParse_JSXBasicComponent(t *testing.T) {
	src := []byte(`import { Button } from './btn'
function App() {
  return <Button label="hi" />
}
`)
	r := Parse("a.tsx", src)
	// Button should be an import decl + a ref from the JSX element.
	if findDecl(r, "Button") == nil {
		t.Fatalf("Button import decl missing; decls=%v", declNames(r))
	}
	refs := refsNamed(r, "Button")
	if len(refs) == 0 {
		t.Fatal("no refs to Button")
	}
	if refs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("Button ref did not resolve: %+v", refs[0].Binding)
	}
}

func TestParse_JSXEmbeddedExpression(t *testing.T) {
	src := []byte(`function App() {
  const name = "hi"
  return <Greeting text={name} count={42} />
}
`)
	r := Parse("a.tsx", src)
	// name should be a const decl; its ref inside {name} in JSX should resolve.
	nameDecl := findDecl(r, "name")
	if nameDecl == nil {
		t.Fatal("const name missing")
	}
	nameRefs := refsNamed(r, "name")
	if len(nameRefs) == 0 {
		t.Fatal("no ref to name inside JSX embedded expression")
	}
	if nameRefs[0].Binding.Kind != scope.BindResolved || nameRefs[0].Binding.Decl != nameDecl.ID {
		t.Errorf("name ref did not bind to const decl: %+v", nameRefs[0])
	}
}

func TestParse_JSXDoesntBreakGenerics(t *testing.T) {
	// In a .ts file (non-JSX), function<T>(x) must still work as generic.
	src := []byte(`function id<T>(x: T): T { return x }`)
	r := Parse("a.ts", src)
	if findDecl(r, "T") == nil {
		t.Fatal("generic T missing in .ts file (JSX should not be active)")
	}
}

func TestParse_JSXNestedElements(t *testing.T) {
	src := []byte(`function App() {
  return (
    <Container>
      <Header title="hi" />
      <Body>
        {items.map((item) => <Item key={item.id} />)}
      </Body>
    </Container>
  )
}
`)
	r := Parse("a.tsx", src)
	// Components should all be emitted as refs.
	for _, name := range []string{"Container", "Header", "Body", "Item"} {
		if len(refsNamed(r, name)) == 0 {
			t.Errorf("no ref to component %q", name)
		}
	}
	// Inside the embedded expression, items.map should work — items is a ref.
	if len(refsNamed(r, "items")) == 0 {
		t.Error("items inside JSX embedded expression missed")
	}
}

func TestParse_JSXFragment(t *testing.T) {
	src := []byte(`function App() {
  return <>
    <A />
    <B />
  </>
}
`)
	r := Parse("a.tsx", src)
	if len(refsNamed(r, "A")) == 0 || len(refsNamed(r, "B")) == 0 {
		t.Error("fragment children not seen as component refs")
	}
}



func TestParse_PropertyAccessEmitsProbableRef(t *testing.T) {
	src := []byte(`const obj = { foo: 1 }
const x = obj.foo
const y = obj.bar.baz
`)
	r := Parse("a.ts", src)
	// `foo`, `bar`, `baz` should all be probable property-access refs.
	for _, name := range []string{"foo", "bar", "baz"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("property-access %q missing", name)
			continue
		}
		// At least one ref should be a property_access probable.
		found := false
		for _, ref := range refs {
			if ref.Binding.Kind == scope.BindProbable && ref.Binding.Reason == "property_access" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no property_access probable ref for %q: %+v", name, refs)
		}
	}
}

// TestParse_ThisDotFieldResolves asserts that `this.X` inside a method
// body binds to the enclosing class's field/method decl, not an
// unresolved property_access ref. Also verifies:
//   - `this.X.Y` only resolves the first segment (X); Y remains property_access.
//   - `this` inside an arrow nested in a method still resolves to the class.
//   - standalone (non-method) `this.X` falls through to property_access.
func TestParse_ThisDotFieldResolves(t *testing.T) {
	src := []byte(`class Counter {
  value: number
  step: number
  increment() {
    this.value = this.value + this.step
    const bump = () => { this.value += 1 }
    bump()
    return this.value.toString()
  }
}
function topLevel() {
  return this.value
}
`)
	r := Parse("a.ts", src)

	// Find field decls.
	var valueDecl, stepDecl *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Namespace != scope.NSField {
			continue
		}
		switch d.Name {
		case "value":
			if d.Kind == scope.KindField {
				valueDecl = d
			}
		case "step":
			if d.Kind == scope.KindField {
				stepDecl = d
			}
		}
	}
	if valueDecl == nil {
		t.Fatalf("field decl `value` missing; decls=%v", declNames(r))
	}
	if stepDecl == nil {
		t.Fatalf("field decl `step` missing; decls=%v", declNames(r))
	}

	// Count resolved `this.value` refs. The body has 4 occurrences of
	// `this.value` inside the method:
	//   1. `this.value = ...`       (direct method body)
	//   2. `this.value + ...`       (direct method body)
	//   3. `this.value += 1`        (inside arrow, lexical this)
	//   4. `this.value.toString()`  (only `value` resolves; `toString` stays property_access)
	// And 1 more in topLevel() that should fall back to property_access.
	resolvedValue := 0
	propAccessValue := 0
	for _, ref := range r.Refs {
		if ref.Name != "value" {
			continue
		}
		switch {
		case ref.Binding.Kind == scope.BindResolved &&
			ref.Binding.Decl == valueDecl.ID &&
			ref.Binding.Reason == "this_dot_field":
			resolvedValue++
		case ref.Binding.Kind == scope.BindProbable &&
			ref.Binding.Reason == "property_access":
			propAccessValue++
		}
	}
	if resolvedValue != 4 {
		t.Errorf("expected 4 resolved this.value refs, got %d; refs=%+v", resolvedValue, refsNamed(r, "value"))
	}
	// The standalone topLevel() function's `this.value` should fall
	// through to property_access (no enclosing class on the scope stack).
	if propAccessValue != 1 {
		t.Errorf("expected 1 property_access `value` ref (from topLevel), got %d; refs=%+v", propAccessValue, refsNamed(r, "value"))
	}

	// `this.step` appears once and should resolve.
	resolvedStep := 0
	for _, ref := range r.Refs {
		if ref.Name != "step" {
			continue
		}
		if ref.Binding.Kind == scope.BindResolved &&
			ref.Binding.Decl == stepDecl.ID &&
			ref.Binding.Reason == "this_dot_field" {
			resolvedStep++
		}
	}
	if resolvedStep != 1 {
		t.Errorf("expected 1 resolved this.step ref, got %d", resolvedStep)
	}

	// `this.value.toString()` — `toString` should still be property_access,
	// not resolved against anything (no such field on the class).
	toStringFound := false
	for _, ref := range r.Refs {
		if ref.Name != "toString" {
			continue
		}
		if ref.Binding.Kind == scope.BindProbable && ref.Binding.Reason == "property_access" {
			toStringFound = true
		}
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "this_dot_field" {
			t.Errorf("toString should NOT be resolved as this_dot_field: %+v", ref)
		}
	}
	if !toStringFound {
		t.Errorf("expected property_access ref for toString")
	}
}

func declNames(r *scope.Result) []string {
	out := make([]string, 0, len(r.Decls))
	for _, d := range r.Decls {
		out = append(out, d.Name)
	}
	return out
}

// TestParse_FullSpan_ScopeOwningDecls asserts that function, class,
// interface, and type decls populate FullSpan covering from the
// declaration keyword through the closing brace.
func TestParse_FullSpan_ScopeOwningDecls(t *testing.T) {
	src := []byte(`function greet(name: string): string {
  return "hi " + name
}

class Box<T> {
  value: T
  unwrap(): T { return this.value }
}

interface Shape {
  area(): number
}
`)
	r := Parse("a.ts", src)
	cases := []struct {
		name       string
		wantPrefix string
	}{
		{"greet", "function greet"},
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
	// Class method: owns a function scope but has no preceding decl
	// keyword. FullSpan still patched via scope close. Start is the
	// identifier; end is the closing brace.
	d := findDecl(r, "unwrap")
	if d == nil {
		t.Fatal("unwrap method decl missing")
	}
	if d.FullSpan.EndByte <= d.Span.EndByte {
		t.Errorf("unwrap: FullSpan.EndByte=%d should cover body past Span.EndByte=%d",
			d.FullSpan.EndByte, d.Span.EndByte)
	}
}

// TestParse_ClassInterfaceMerging: TS allows `class Foo {}` and
// `interface Foo {}` to declare-merge — they should share a DeclID
// after parse so refs-to against one finds references to both. Also
// covers `class Foo {}` + `type Foo = ...` (less common but same rule).
func TestParse_ClassInterfaceMerging(t *testing.T) {
	src := []byte(`class Foo {}
interface Foo { x: number }
function use(f: Foo) {
  return f
}
`)
	r := Parse("a.ts", src)
	// Find both Foo decls.
	var foos []*scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "Foo" {
			foos = append(foos, &r.Decls[i])
		}
	}
	if len(foos) < 2 {
		t.Fatalf("expected >=2 Foo decls; got %d; decls=%v", len(foos), declNames(r))
	}
	if foos[0].ID != foos[1].ID {
		t.Errorf("class Foo and interface Foo have distinct IDs %d vs %d (should merge)",
			foos[0].ID, foos[1].ID)
	}
}

// TestParse_ValueTypeNameCollision: when a value and a type share the
// same name (`const X = 1; type X = string`), they emit DISTINCT decls
// — the value in NSValue, the type in NSType. Refs in type position
// bind to the type decl; refs in value position bind to the value decl.
func TestParse_ValueTypeNameCollision(t *testing.T) {
	src := []byte(`const X = 1
type X = string
let v = X
let t: X = "hi"
`)
	r := Parse("a.ts", src)

	var constX, typeX *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name != "X" {
			continue
		}
		if d.Kind == scope.KindConst && d.Namespace == scope.NSValue {
			constX = d
		} else if d.Kind == scope.KindType && d.Namespace == scope.NSType {
			typeX = d
		}
	}
	if constX == nil || typeX == nil {
		t.Fatalf("missing decls: const=%v type=%v\nall decls: %v", constX, typeX, declNames(r))
	}
	if constX.ID == typeX.ID {
		t.Fatalf("const X and type X should have distinct DeclIDs; both are %d", constX.ID)
	}

	// Find the ref to X in `let v = X` (value position) and `let t: X`
	// (type position). Assert each binds to the correct decl.
	refs := refsNamed(r, "X")
	if len(refs) < 2 {
		t.Fatalf("expected at least 2 refs to X; got %d", len(refs))
	}
	var vRef, tRef *scope.Ref
	for i := range refs {
		// Crude positional disambiguation: type ref is after the second `:`.
		if refs[i].Namespace == scope.NSValue && vRef == nil {
			vRef = &refs[i]
		} else if refs[i].Namespace == scope.NSType && tRef == nil {
			tRef = &refs[i]
		}
	}
	if vRef == nil {
		t.Errorf("no NSValue ref to X (value position)")
	} else if vRef.Binding.Decl != constX.ID {
		t.Errorf("value-position ref bound to %d, want const X %d", vRef.Binding.Decl, constX.ID)
	}
	if tRef == nil {
		t.Errorf("no NSType ref to X (type position)")
	} else if tRef.Binding.Decl != typeX.ID {
		t.Errorf("type-position ref bound to %d, want type X %d", tRef.Binding.Decl, typeX.ID)
	}
}

// TestParse_ClassDualResident: a class resolves in BOTH namespaces —
// `new Foo()` (value) and `let f: Foo` (type) must bind to the same
// DeclID so refs-to finds both uses.
func TestParse_ClassDualResident(t *testing.T) {
	src := []byte(`class Foo {}
const a = new Foo()
let b: Foo
`)
	r := Parse("a.ts", src)

	// After within-file merge, all Foo decls share one ID.
	var fooID scope.DeclID
	for i := range r.Decls {
		if r.Decls[i].Name == "Foo" {
			if fooID == 0 {
				fooID = r.Decls[i].ID
			} else if r.Decls[i].ID != fooID {
				t.Errorf("class Foo decls have distinct IDs %d vs %d (should merge)",
					fooID, r.Decls[i].ID)
			}
		}
	}
	if fooID == 0 {
		t.Fatalf("no Foo decl found; decls=%v", declNames(r))
	}
	refs := refsNamed(r, "Foo")
	if len(refs) < 2 {
		t.Fatalf("expected >=2 Foo refs; got %d", len(refs))
	}
	for i, ref := range refs {
		if ref.Binding.Kind != scope.BindResolved {
			t.Errorf("Foo ref %d not resolved: %+v", i, ref.Binding)
			continue
		}
		if ref.Binding.Decl != fooID {
			t.Errorf("Foo ref %d bound to %d, want %d", i, ref.Binding.Decl, fooID)
		}
	}
}

// TestParse_InterfaceNSType: interface Foo emits a KindInterface decl
// in NSType only (no shadow NSValue). Refs bind to it via NSType lookup.
func TestParse_InterfaceNSType(t *testing.T) {
	src := []byte(`interface Foo { x: number }
function use(f: Foo) { return f }
`)
	r := Parse("a.ts", src)
	var foo *scope.Decl
	for i := range r.Decls {
		if r.Decls[i].Name == "Foo" && r.Decls[i].Kind == scope.KindInterface {
			foo = &r.Decls[i]
			break
		}
	}
	if foo == nil {
		t.Fatalf("no interface Foo decl; decls=%v", declNames(r))
	}
	if foo.Namespace != scope.NSType {
		t.Errorf("interface Foo namespace = %q, want NSType", foo.Namespace)
	}
	// The `f: Foo` ref should bind.
	refs := refsNamed(r, "Foo")
	if len(refs) == 0 {
		t.Fatalf("no Foo ref found")
	}
	if refs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("Foo ref not resolved: %+v", refs[0].Binding)
	}
	if refs[0].Binding.Decl != foo.ID {
		t.Errorf("Foo ref bound to %d, want %d", refs[0].Binding.Decl, foo.ID)
	}
}
