package golang

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

func TestParse_PackageAndTopLevel(t *testing.T) {
	src := []byte(`package foo

const Pi = 3.14
var Name = "edr"

func Hello() string {
	return Name
}
`)
	r := Parse("a.go", src)
	for _, n := range []string{"Pi", "Name", "Hello"} {
		if findDecl(r, n) == nil {
			t.Errorf("missing decl %q; decls=%v", n, declNames(r))
		}
	}
	// Name inside Hello's body should resolve to the file-scope Name.
	nameRefs := refsNamed(r, "Name")
	if len(nameRefs) == 0 {
		t.Fatal("no refs to Name")
	}
	if nameRefs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("Name ref unresolved: %+v", nameRefs[0].Binding)
	}
}

func TestParse_FunctionParams(t *testing.T) {
	src := []byte(`package foo

func Add(a, b int) int {
	return a + b
}
`)
	r := Parse("a.go", src)
	for _, n := range []string{"Add", "a", "b"} {
		d := findDecl(r, n)
		if d == nil {
			t.Fatalf("missing decl %q; decls=%v", n, declNames(r))
		}
	}
	// Refs in body bind to params.
	aRefs := refsNamed(r, "a")
	if len(aRefs) == 0 || aRefs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("a ref unresolved: %+v", aRefs)
	}
}

func TestParse_ShortVarDecl(t *testing.T) {
	src := []byte(`package foo

func Run() {
	x := 1
	y, z := 2, 3
	_ = x + y + z
}
`)
	r := Parse("a.go", src)
	for _, n := range []string{"x", "y", "z"} {
		d := findDecl(r, n)
		if d == nil {
			t.Errorf("missing decl %q; decls=%v", n, declNames(r))
			continue
		}
		if d.Kind != scope.KindVar {
			t.Errorf("%q kind = %v, want var", n, d.Kind)
		}
	}
}

func TestParse_ImportBlock(t *testing.T) {
	src := []byte(`package foo

import (
	"fmt"
	alias "strings"
)

func X() {
	fmt.Println()
	alias.ToUpper("")
}
`)
	r := Parse("a.go", src)
	if findDecl(r, "alias") == nil {
		t.Errorf("alias import missing; decls=%v", declNames(r))
	}
	// alias.ToUpper: alias is a ref, ToUpper is property access (not emitted)
	refs := refsNamed(r, "alias")
	if len(refs) == 0 || refs[0].Binding.Kind != scope.BindResolved {
		t.Errorf("alias ref unresolved: %+v", refs)
	}
}

func TestParse_Struct(t *testing.T) {
	src := []byte(`package foo

type Point struct {
	X int
	Y int
}
`)
	r := Parse("a.go", src)
	if findDecl(r, "Point") == nil {
		t.Fatal("type Point missing")
	}
	for _, n := range []string{"X", "Y"} {
		d := findDecl(r, n)
		if d == nil {
			t.Errorf("struct field %q missing; decls=%v", n, declNames(r))
		} else if d.Kind != scope.KindField {
			t.Errorf("%q kind = %v, want field", n, d.Kind)
		}
	}
}

func TestParse_MethodWithReceiver(t *testing.T) {
	src := []byte(`package foo

type Counter struct {
	n int
}

func (c *Counter) Inc() {
	c.n++
}
`)
	r := Parse("a.go", src)
	if findDecl(r, "Counter") == nil {
		t.Fatal("type Counter missing")
	}
	if findDecl(r, "Inc") == nil {
		t.Errorf("method Inc missing; decls=%v", declNames(r))
	}
	// Receiver `c` should be a param decl in the method scope.
	cDecl := findDecl(r, "c")
	if cDecl == nil || cDecl.Kind != scope.KindParam {
		t.Errorf("receiver c missing or wrong kind; decls=%v", declNames(r))
	}
}



func TestParse_PropertyAccess(t *testing.T) {
	src := []byte(`package p

import "fmt"

func F(x Y) {
	fmt.Println(x.Name)
	return x.Do()
}
`)
	r := Parse("a.go", src)
	for _, name := range []string{"Println", "Name", "Do"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("property-access %q missing", name)
			continue
		}
		if refs[0].Binding.Kind != scope.BindProbable || refs[0].Binding.Reason != "property_access" {
			t.Errorf("%q should be property_access probable, got %+v", name, refs[0].Binding)
		}
	}
}

func TestParse_Builtins(t *testing.T) {
	src := []byte(`package foo

func F(xs []int) int {
	return len(xs)
}
`)
	r := Parse("a.go", src)
	// `len` and `int` should bind as builtins.
	for _, name := range []string{"len", "int"} {
		refs := refsNamed(r, name)
		if len(refs) == 0 {
			t.Errorf("no ref to builtin %q", name)
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

func TestParse_UnresolvedRef(t *testing.T) {
	src := []byte(`package foo

func F() int {
	return unknown_global
}
`)
	r := Parse("a.go", src)
	ref := refsNamed(r, "unknown_global")
	if len(ref) == 0 {
		t.Fatal("missing ref to unknown_global")
	}
	if ref[0].Binding.Kind != scope.BindUnresolved {
		t.Errorf("expected unresolved, got %+v", ref[0].Binding)
	}
}

func TestParse_VarBlock(t *testing.T) {
	src := []byte(`package foo

var (
	a = 1
	b = 2
)
`)
	r := Parse("a.go", src)
	for _, n := range []string{"a", "b"} {
		if findDecl(r, n) == nil {
			t.Errorf("var-block decl %q missing; decls=%v", n, declNames(r))
		}
	}
}

// TestParse_FullSpan_ScopeOwningDecls asserts that function, type,
// struct, and interface decls populate FullSpan covering from the
// declaration keyword through the closing brace.
func TestParse_FullSpan_ScopeOwningDecls(t *testing.T) {
	src := []byte(`package p

func Greet(name string) string {
	return "hi " + name
}

type Point struct {
	X int
	Y int
}

type Stringer interface {
	String() string
}
`)
	r := Parse("a.go", src)

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
		got := string(src[d.FullSpan.StartByte:min(int(d.FullSpan.EndByte), len(src))])
		if len(got) < len(wantPrefix) || got[:len(wantPrefix)] != wantPrefix {
			t.Errorf("%s: FullSpan content starts %q, want prefix %q", name, got, wantPrefix)
		}
		if got[len(got)-1] != '}' {
			t.Errorf("%s: FullSpan content does not end at }: %q", name, got)
		}
	}
	check("Greet", "func Greet")
	check("Point", "type Point struct")
	check("Stringer", "type Stringer interface")
}

// TestParse_FullSpan_LeafDecls asserts that non-scope-owning decls
// (var, const, param, field) fall back to FullSpan == Span. The pass
// does not track statement boundaries for leaf decls, so this is the
// documented behavior.
func TestParse_FullSpan_LeafDecls(t *testing.T) {
	src := []byte(`package p

var Pi = 3.14
const MaxN = 10
`)
	r := Parse("a.go", src)
	for _, name := range []string{"Pi", "MaxN"} {
		d := findDecl(r, name)
		if d == nil {
			t.Fatalf("decl %q missing", name)
		}
		// FullSpan.StartByte covers the keyword (var/const), so it is
		// less than Span.StartByte. FullSpan.EndByte matches Span.EndByte
		// (leaf decls do not track statement end).
		if d.FullSpan.StartByte >= d.Span.StartByte {
			t.Errorf("%s: FullSpan.StartByte=%d should cover the keyword before Span.StartByte=%d",
				name, d.FullSpan.StartByte, d.Span.StartByte)
		}
		if d.FullSpan.EndByte != d.Span.EndByte {
			t.Errorf("%s: FullSpan.EndByte=%d should equal Span.EndByte=%d for leaf decl",
				name, d.FullSpan.EndByte, d.Span.EndByte)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestParse_CompositeLiteralKey asserts that struct-composite-literal
// field keys (`T{field: value}`) do NOT bind to same-named outer decls.
// Otherwise renaming a type `span` rewrites every struct field named
// span on unrelated types (caught while dogfooding edr on itself).
func TestParse_CompositeLiteralKey(t *testing.T) {
	src := []byte(`package p

type span struct {
	lo, hi int
}

type entry struct {
	file string
	span [2]int
}

func make() entry {
	return entry{
		file: "x",
		span: [2]int{1, 2},
	}
}
`)
	r := Parse("a.go", src)

	// Two decls named "span": one top-level type, one field on entry.
	// Field span lives in NSField with scope = entry's class scope.
	var spanType *scope.Decl
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Name == "span" && d.Kind == scope.KindType {
			spanType = d
			break
		}
	}
	if spanType == nil {
		t.Fatalf("top-level `span` type decl missing; decls=%v", declNames(r))
	}

	// The `span:` key inside `entry{span: ...}` must NOT emit a ref bound
	// to the top-level span type.
	for _, ref := range r.Refs {
		if ref.Name != "span" {
			continue
		}
		if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl == spanType.ID {
			t.Errorf("composite-literal key `span:` wrongly bound to span type; ref at byte %d", ref.Span.StartByte)
		}
	}
}


// TestParse_ImportSignature_Basic: `import "fmt"` emits a KindImport decl
// named "fmt" (last path segment) with Signature = "fmt\x00*". The
// import-graph resolver relies on this format.
func TestParse_ImportSignature_Basic(t *testing.T) {
	src := []byte(`package foo

import "fmt"

func X() { fmt.Println() }
`)
	r := Parse("a.go", src)
	d := findDecl(r, "fmt")
	if d == nil {
		t.Fatalf("no fmt import decl; decls=%v", declNames(r))
	}
	if d.Kind != scope.KindImport {
		t.Errorf("fmt: kind=%v, want KindImport", d.Kind)
	}
	want := "fmt\x00*"
	if d.Signature != want {
		t.Errorf("fmt.Signature = %q, want %q", d.Signature, want)
	}
	if d.Exported {
		t.Errorf("fmt.Exported = true (imports should never be exported)")
	}
}

// TestParse_ImportSignature_MultiSegment: `import "net/http"` binds to
// the last segment ("http"). Signature keeps the full path.
func TestParse_ImportSignature_MultiSegment(t *testing.T) {
	src := []byte(`package foo

import "net/http"
`)
	r := Parse("a.go", src)
	if findDecl(r, "http") == nil {
		t.Fatalf("no http import decl; decls=%v", declNames(r))
	}
	d := findDecl(r, "http")
	want := "net/http\x00*"
	if d.Signature != want {
		t.Errorf("http.Signature = %q, want %q", d.Signature, want)
	}
}

// TestParse_ImportSignature_Aliased: an aliased import keeps the alias
// as the decl name but the Signature carries the actual import path.
// Origname slot is "*" because Go imports bind a whole package.
func TestParse_ImportSignature_Aliased(t *testing.T) {
	src := []byte(`package foo

import (
	"fmt"
	alias "strings"
)

func X() {
	fmt.Println(alias.ToUpper(""))
}
`)
	r := Parse("a.go", src)
	if findDecl(r, "fmt") == nil {
		t.Errorf("no fmt decl; decls=%v", declNames(r))
	}
	aliasDecl := findDecl(r, "alias")
	if aliasDecl == nil {
		t.Fatalf("no alias decl; decls=%v", declNames(r))
	}
	if aliasDecl.Kind != scope.KindImport {
		t.Errorf("alias: kind=%v, want KindImport", aliasDecl.Kind)
	}
	want := "strings\x00*"
	if aliasDecl.Signature != want {
		t.Errorf("alias.Signature = %q, want %q", aliasDecl.Signature, want)
	}
	// The non-aliased "fmt" should also carry its signature.
	fmtDecl := findDecl(r, "fmt")
	if fmtDecl.Signature != "fmt\x00*" {
		t.Errorf("fmt.Signature = %q, want %q", fmtDecl.Signature, "fmt\x00*")
	}
}

// TestParse_ExportedFlag: file-scope decls whose first rune is uppercase
// get Exported=true. Lowercase decls and non-file-scope decls (params,
// block locals) do not. KindImport always stays unexported regardless
// of name casing.
func TestParse_ExportedFlag(t *testing.T) {
	src := []byte(`package foo

import "fmt"

const Pi = 3.14
const e = 2.71

var Name = "edr"
var hidden = 1

type Config struct{ Debug bool }
type internal struct{}

func Public(x int) int {
	y := x + 1
	return y
}

func private() {}

func (c *Config) Method() {}
`)
	r := Parse("a.go", src)
	cases := map[string]bool{
		"Pi":       true,
		"e":        false,
		"Name":     true,
		"hidden":   false,
		"Config":   true,
		"internal": false,
		"Public":   true,
		"private":  false,
		"Method":   true,
		// import: never exported
		"fmt": false,
	}
	for name, want := range cases {
		d := findDecl(r, name)
		if d == nil {
			t.Errorf("missing decl %q; decls=%v", name, declNames(r))
			continue
		}
		if d.Exported != want {
			t.Errorf("%q: Exported=%v, want %v", name, d.Exported, want)
		}
	}
	// Non-file-scope decls: params and block-local vars must never be
	// Exported. `Public`'s param `x` and its local `y` are both at
	// function scope.
	for _, d := range r.Decls {
		if d.Name == "x" || d.Name == "y" {
			if d.Exported {
				t.Errorf("non-file-scope decl %q should not be Exported", d.Name)
			}
		}
	}
}
