package index

import "testing"

func TestParseLua_Fixture(t *testing.T) {
	src := []byte(`-- line comment, should not produce symbols
--[[ block comment
  function Fake() end
]]

-- Simple top-level function
function greet(name)
  print("Hello, " .. name)
end

-- Local function
local function helper(x)
  return x * 2
end

-- Module-style dot-syntax
function M.render(ctx)
  -- inner function should NOT appear
  local function inner()
    return 1
  end
  return inner()
end

-- Method syntax (colon)
function MyClass:init(val)
  self.val = val
end

-- Require imports
local json = require("cjson")
local utils = require 'myapp.utils'
local _ = require("socket.http")

-- Control flow blocks (should not confuse end tracking)
function compute(n)
  if n > 0 then
    for i = 1, n do
      -- nothing
    end
  end
  return n
end

-- Function inside string (should not match)
local s = "function FakeStr() end"
local s2 = [[
function FakeLong() end
]]
`)
	r := ParseLua(src)
	for i, sym := range r.Symbols {
		t.Logf("sym[%d] %-9s %-20s L%d-%d parent=%d", i, sym.Type, sym.Name, sym.StartLine, sym.EndLine, sym.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s L%d", i, imp.Path, imp.Line)
	}

	wantSyms := []struct{ typ, name string }{
		{"function", "greet"},
		{"function", "helper"},
		{"function", "render"},
		{"function", "init"},
		{"function", "compute"},
	}
	if len(r.Symbols) != len(wantSyms) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(wantSyms))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %q", i, s.Type, s.Name)
		}
	}
	for i, w := range wantSyms {
		if i >= len(r.Symbols) {
			t.Errorf("symbol %d missing: want %s %q", i, w.typ, w.name)
			continue
		}
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("symbol %d: got %s %q, want %s %q",
				i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}

	// Verify no spurious symbols
	for _, s := range r.Symbols {
		if s.Name == "Fake" || s.Name == "FakeStr" || s.Name == "FakeLong" || s.Name == "inner" {
			t.Errorf("spurious symbol: %+v", s)
		}
	}

	// Verify imports
	wantImps := []string{"cjson", "myapp.utils", "socket.http"}
	if len(r.Imports) != len(wantImps) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantImps))
	}
	for i, want := range wantImps {
		if i >= len(r.Imports) {
			t.Errorf("import %d missing: want %q", i, want)
			continue
		}
		if r.Imports[i].Path != want {
			t.Errorf("import %d: got %q, want %q", i, r.Imports[i].Path, want)
		}
	}

	// Verify EndLines are set
	for _, s := range r.Symbols {
		if s.EndLine == 0 {
			t.Errorf("symbol %q has EndLine=0", s.Name)
		}
		if s.EndLine < s.StartLine {
			t.Errorf("symbol %q: EndLine %d < StartLine %d", s.Name, s.EndLine, s.StartLine)
		}
	}
}

func TestParseLua_LongStringComment(t *testing.T) {
	src := []byte(`--[==[
function Fake() end
]==]

function real() end
`)
	r := ParseLua(src)
	if len(r.Symbols) != 1 {
		t.Errorf("got %d symbols, want 1", len(r.Symbols))
		for _, s := range r.Symbols {
			t.Logf("  %s %q", s.Type, s.Name)
		}
		return
	}
	if r.Symbols[0].Name != "real" {
		t.Errorf("got %q, want %q", r.Symbols[0].Name, "real")
	}
}

func TestParseLua_ModuleDot(t *testing.T) {
	src := []byte(`function a.b.c(x) return x end`)
	r := ParseLua(src)
	if len(r.Symbols) != 1 {
		t.Errorf("got %d symbols, want 1", len(r.Symbols))
		return
	}
	if r.Symbols[0].Name != "c" {
		t.Errorf("got name %q, want %q", r.Symbols[0].Name, "c")
	}
}

func TestParseLua_RequireVariants(t *testing.T) {
	src := []byte(`
require("foo")
require 'bar'
local x = require("baz.qux")
`)
	r := ParseLua(src)
	wantPaths := []string{"foo", "bar", "baz.qux"}
	if len(r.Imports) != len(wantPaths) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantPaths))
		for _, imp := range r.Imports {
			t.Logf("  %q", imp.Path)
		}
		return
	}
	for i, want := range wantPaths {
		if r.Imports[i].Path != want {
			t.Errorf("import %d: got %q, want %q", i, r.Imports[i].Path, want)
		}
	}
}
