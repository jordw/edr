package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Lua_RequireModuleHandle: `local m = require("foo")`
// in user.lua binds m to foo.lua. `m.Helper()` should resolve the
// property ref to foo.lua's Helper decl.
func TestImportGraph_Lua_RequireModuleHandle(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.lua":  "function Helper()\nend\n",
		"user.lua": "local m = require(\"foo\")\nm.Helper()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rfoo := idx.ResultFor(root, "foo.lua")
	ruser := idx.ResultFor(root, "user.lua")
	if rfoo == nil || ruser == nil {
		t.Fatalf("ResultFor nil: foo=%v user=%v", rfoo, ruser)
	}
	helper := findDecl(rfoo, "Helper")
	if helper == nil {
		t.Fatalf("foo.lua: no Helper decl; decls=%v", declsOf(rfoo))
	}
	refs := refsByName(ruser, "Helper")
	if len(refs) == 0 {
		t.Fatal("user.lua: no refs to Helper")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == helper.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("user.lua: Helper ref did not resolve through module handle; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Lua_DottedModulePath: `require("foo.bar")` resolves
// to foo/bar.lua.
func TestImportGraph_Lua_DottedModulePath(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo/bar.lua": "function Greet()\nend\n",
		"user.lua":    "local m = require(\"foo.bar\")\nm.Greet()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rbar := idx.ResultFor(root, "foo/bar.lua")
	ruser := idx.ResultFor(root, "user.lua")
	if rbar == nil || ruser == nil {
		t.Fatalf("ResultFor nil: bar=%v user=%v", rbar, ruser)
	}
	greet := findDecl(rbar, "Greet")
	if greet == nil {
		t.Fatalf("foo/bar.lua: no Greet decl")
	}
	refs := refsByName(ruser, "Greet")
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == greet.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("user.lua: Greet ref did not resolve via dotted require; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Lua_InitModule: `require("pkg")` resolves to
// pkg/init.lua when pkg.lua doesn't exist (Penlight-style packages).
func TestImportGraph_Lua_InitModule(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"pkg/init.lua": "function Run()\nend\n",
		"user.lua":     "local p = require(\"pkg\")\np.Run()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	rinit := idx.ResultFor(root, "pkg/init.lua")
	ruser := idx.ResultFor(root, "user.lua")
	if rinit == nil || ruser == nil {
		t.Fatalf("ResultFor nil: init=%v user=%v", rinit, ruser)
	}
	run := findDecl(rinit, "Run")
	if run == nil {
		t.Fatalf("pkg/init.lua: no Run decl")
	}
	refs := refsByName(ruser, "Run")
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == run.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("user.lua: Run did not resolve to pkg/init.lua; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Lua_NoParenStringForm: `require"foo"` (no parens,
// Lua-permitted single-string-arg form) is captured by the builder
// and resolved.
func TestImportGraph_Lua_NoParenStringForm(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.lua":  "function X()\nend\n",
		"user.lua": "local m = require\"foo\"\nm.X()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	rfoo := idx.ResultFor(root, "foo.lua")
	ruser := idx.ResultFor(root, "user.lua")
	if rfoo == nil || ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	x := findDecl(rfoo, "X")
	if x == nil {
		t.Fatalf("foo.lua: no X decl")
	}
	refs := refsByName(ruser, "X")
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == x.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("user.lua: X did not resolve via no-paren require; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Lua_ExternalModuleStaysLocal: require of a module
// not in the repo (stdlib like "io", or a luarocks dep) keeps the
// import local — no panic, no bogus rewrite.
func TestImportGraph_Lua_ExternalModuleStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"user.lua": "local io = require(\"io\")\nio.write(\"hi\")\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	ruser := idx.ResultFor(root, "user.lua")
	if ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	// `write` ref shouldn't be unexpectedly bound to anything in the
	// repo; the resolver must just leave it alone.
	for _, r := range ruser.Refs {
		if r.Name == "write" && r.Binding.Reason == "import_export" {
			t.Errorf("write ref unexpectedly rewritten as import_export")
		}
	}
	// Decl for `io` should still be a KindImport (signature stamped).
	io := findDecl(ruser, "io")
	if io == nil {
		t.Fatalf("no `io` decl in user.lua")
	}
	if io.Kind != scope.KindImport {
		t.Errorf("`io` decl: kind=%v, want KindImport", io.Kind)
	}
}

// TestImportGraph_Lua_MultiLHSDisablesDetection: `local a, b = ...`
// has multiple LHS names; require detection must not fire (we only
// support single-binding forms).
func TestImportGraph_Lua_MultiLHSDisablesDetection(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"user.lua": "local a, b = require(\"foo\"), require(\"bar\")\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	ruser := idx.ResultFor(root, "user.lua")
	if ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	a := findDecl(ruser, "a")
	if a == nil {
		t.Fatalf("no `a` decl")
	}
	if a.Kind == scope.KindImport {
		t.Errorf("multi-LHS: `a` was rewritten to KindImport, want KindLet")
	}
}

// TestImportGraph_Lua_NotARequireRHS: `local foo = bar` (not a
// require call) must keep `foo` as KindLet, not KindImport.
func TestImportGraph_Lua_NotARequireRHS(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"user.lua": "local foo = 5\nlocal bar = something()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	ruser := idx.ResultFor(root, "user.lua")
	if ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	for _, name := range []string{"foo", "bar"} {
		d := findDecl(ruser, name)
		if d == nil {
			t.Fatalf("no `%s` decl", name)
		}
		if d.Kind == scope.KindImport {
			t.Errorf("`%s` rewritten to KindImport, want KindLet", name)
		}
	}
}
