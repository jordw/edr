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
	// util.run — util resolves; run is a property access, should not be emitted as a ref.
	utilRefs := refsNamed(r, "util")
	if len(utilRefs) == 0 || utilRefs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("util ref not resolved")
	}
	for _, ref := range r.Refs {
		if ref.Name == "run" {
			t.Errorf("property access 'run' should not be emitted as a ref")
		}
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

func declNames(r *scope.Result) []string {
	out := make([]string, 0, len(r.Decls))
	for _, d := range r.Decls {
		out = append(out, d.Name)
	}
	return out
}
