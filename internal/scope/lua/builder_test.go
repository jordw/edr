package lua

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

func find(t *testing.T, decls []scope.Decl, name string) *scope.Decl {
	t.Helper()
	for i := range decls {
		if decls[i].Name == name {
			return &decls[i]
		}
	}
	return nil
}

func countRefs(refs []scope.Ref, name string) int {
	n := 0
	for _, r := range refs {
		if r.Name == name {
			n++
		}
	}
	return n
}

func TestParse_TopLevelFunction(t *testing.T) {
	src := []byte(`function compute(x)
  return x * 2
end

function caller()
  return compute(5)
end
`)
	r := Parse("a.lua", src)
	if find(t, r.Decls, "compute") == nil {
		t.Fatalf("expected decl `compute`; got %+v", r.Decls)
	}
	if find(t, r.Decls, "caller") == nil {
		t.Fatalf("expected decl `caller`; got %+v", r.Decls)
	}
	if countRefs(r.Refs, "compute") != 1 {
		t.Errorf("expected 1 ref to compute; got %d", countRefs(r.Refs, "compute"))
	}
}

func TestParse_LocalFunctionShadowsOuter(t *testing.T) {
	src := []byte(`function compute() end

function user()
  local function compute() return 42 end
  return compute()
end
`)
	r := Parse("a.lua", src)
	// Two decls named compute: outer at file scope, inner at user's func scope.
	count := 0
	for _, d := range r.Decls {
		if d.Name == "compute" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 decls named compute (outer + local), got %d", count)
	}
	// The ref `compute()` inside user() should bind to the local, not the outer.
	var bound scope.DeclID
	for _, d := range r.Decls {
		if d.Name == "compute" && d.Scope != 1 { // not file-scope
			bound = d.ID
		}
	}
	if bound == 0 {
		t.Fatalf("expected a non-file-scope `compute` decl")
	}
	for _, ref := range r.Refs {
		if ref.Name == "compute" && ref.Binding.Kind == scope.BindResolved {
			if ref.Binding.Decl != bound {
				t.Errorf("expected ref to bind to local compute (Decl=%d), got %d", bound, ref.Binding.Decl)
			}
		}
	}
}

func TestParse_LocalVarBinding(t *testing.T) {
	src := []byte(`local x = 1
local y = x + 2
print(y)
`)
	r := Parse("a.lua", src)
	if find(t, r.Decls, "x") == nil || find(t, r.Decls, "y") == nil {
		t.Fatalf("expected decls x and y; got %+v", r.Decls)
	}
	// `x` ref on line 2 should resolve to the file-scope local.
	resolved := false
	for _, ref := range r.Refs {
		if ref.Name == "x" && ref.Binding.Kind == scope.BindResolved {
			resolved = true
		}
	}
	if !resolved {
		t.Errorf("expected x ref to resolve to its decl")
	}
}

func TestParse_PropertyAccess(t *testing.T) {
	src := []byte(`local m = require("mod")
m.foo(m.bar)
m:baz()
`)
	r := Parse("a.lua", src)
	// foo, bar, baz should be NSField refs with Reason="property_access".
	for _, want := range []string{"foo", "bar", "baz"} {
		found := false
		for _, ref := range r.Refs {
			if ref.Name == want && ref.Binding.Reason == "property_access" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected property_access ref for %q", want)
		}
	}
}

func TestParse_FunctionParamScope(t *testing.T) {
	src := []byte(`function outer(a, b)
  return a + b
end

function user()
  return a -- a here is unresolved (outer's param doesn't escape)
end
`)
	r := Parse("a.lua", src)
	// Both params should be decls.
	if find(t, r.Decls, "a") == nil || find(t, r.Decls, "b") == nil {
		t.Fatalf("expected param decls a, b; got %+v", r.Decls)
	}
	// The `a` ref inside user() should be unresolved (not bound to outer's param).
	var refInUser *scope.Ref
	for i, ref := range r.Refs {
		if ref.Name == "a" {
			// Check it's inside user (scope > the params)
			if ref.Span.StartByte > 50 {
				refInUser = &r.Refs[i]
				break
			}
		}
	}
	if refInUser == nil {
		t.Fatalf("expected an `a` ref inside user(); refs=%+v", r.Refs)
	}
	if refInUser.Binding.Kind == scope.BindResolved && refInUser.Binding.Reason == "direct_scope" {
		t.Errorf("`a` inside user() should NOT bind to outer's param (different scope); got %+v", refInUser.Binding)
	}
}

func TestParse_ForLoopVars(t *testing.T) {
	src := []byte(`for i, v in ipairs(t) do
  print(i, v)
end
`)
	r := Parse("a.lua", src)
	if find(t, r.Decls, "i") == nil || find(t, r.Decls, "v") == nil {
		t.Errorf("expected loop var decls i, v; got %+v", r.Decls)
	}
	// Both i and v refs inside the loop should resolve.
	for _, want := range []string{"i", "v"} {
		var found bool
		for _, ref := range r.Refs {
			if ref.Name == want && ref.Binding.Kind == scope.BindResolved && ref.Binding.Reason == "direct_scope" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected ref to %q to resolve to loop var", want)
		}
	}
}

func TestParse_BuiltinResolution(t *testing.T) {
	src := []byte(`print("hello")
local s = tostring(42)
`)
	r := Parse("a.lua", src)
	for _, want := range []string{"print", "tostring"} {
		var found bool
		for _, ref := range r.Refs {
			if ref.Name == want && ref.Binding.Reason == "builtin" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %q ref to resolve as builtin", want)
		}
	}
}

func TestParse_DoBlockScope(t *testing.T) {
	src := []byte(`local x = 1
do
  local x = 2
  print(x)
end
print(x)
`)
	r := Parse("a.lua", src)
	// Two decls named x.
	count := 0
	for _, d := range r.Decls {
		if d.Name == "x" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 decls named x (outer + inner), got %d", count)
	}
}

func TestParse_FunctionMethodSyntax(t *testing.T) {
	src := []byte(`function Counter:add(n)
  self.value = self.value + n
end
`)
	r := Parse("a.lua", src)
	// `add` should be emitted as a method.
	d := find(t, r.Decls, "add")
	if d == nil {
		t.Fatalf("expected decl `add`; got %+v", r.Decls)
	}
	if d.Kind != scope.KindMethod {
		t.Errorf("expected `add` to be KindMethod; got %v", d.Kind)
	}
	// `self` should be implicit param.
	if find(t, r.Decls, "self") == nil {
		t.Errorf("expected implicit `self` param decl")
	}
}
