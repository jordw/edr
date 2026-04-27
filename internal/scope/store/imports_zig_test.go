package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Zig_ImportSiblingFile: `const m = @import("foo.zig")`
// in user.zig binds m to foo.zig. `m.Helper()` should resolve the
// property ref to foo.zig's Helper decl.
func TestImportGraph_Zig_ImportSiblingFile(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.zig":  "pub fn Helper() void {}\n",
		"user.zig": "const m = @import(\"foo.zig\");\npub fn use() void { m.Helper(); }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rfoo := idx.ResultFor(root, "foo.zig")
	ruser := idx.ResultFor(root, "user.zig")
	if rfoo == nil || ruser == nil {
		t.Fatalf("ResultFor nil: foo=%v user=%v", rfoo, ruser)
	}
	helper := findDecl(rfoo, "Helper")
	if helper == nil {
		t.Fatalf("foo.zig: no Helper decl; decls=%v", declsOf(rfoo))
	}
	refs := refsByName(ruser, "Helper")
	if len(refs) == 0 {
		t.Fatal("user.zig: no refs to Helper")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == helper.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("user.zig: Helper ref did not resolve through module handle; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Zig_RelativeParentPath: `@import("../bar/baz.zig")`
// in src/sub/user.zig resolves to src/bar/baz.zig.
func TestImportGraph_Zig_RelativeParentPath(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"src/bar/baz.zig":  "pub fn Run() void {}\n",
		"src/sub/user.zig": "const b = @import(\"../bar/baz.zig\");\npub fn go() void { b.Run(); }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rbaz := idx.ResultFor(root, "src/bar/baz.zig")
	ruser := idx.ResultFor(root, "src/sub/user.zig")
	if rbaz == nil || ruser == nil {
		t.Fatalf("ResultFor nil: baz=%v user=%v", rbaz, ruser)
	}
	run := findDecl(rbaz, "Run")
	if run == nil {
		t.Fatalf("src/bar/baz.zig: no Run decl")
	}
	refs := refsByName(ruser, "Run")
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == run.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("user.zig: Run ref did not resolve via relative parent path; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Zig_LogicalNameStaysExternal: `@import("std")` is
// a logical name resolved by the Zig build system, not a file path.
// The resolver must leave it alone — no panic, no bogus rewrite.
func TestImportGraph_Zig_LogicalNameStaysExternal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"user.zig": "const std = @import(\"std\");\npub fn go() void { std.debug.print(\"hi\", .{}); }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	ruser := idx.ResultFor(root, "user.zig")
	if ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	for _, r := range ruser.Refs {
		if r.Name == "debug" && r.Binding.Reason == "import_export" {
			t.Errorf("debug ref unexpectedly rewritten as import_export")
		}
	}
	// `std` decl should still be a KindImport (signature stamped).
	stdDecl := findDecl(ruser, "std")
	if stdDecl == nil {
		t.Fatalf("no `std` decl in user.zig")
	}
	if stdDecl.Kind != scope.KindImport {
		t.Errorf("`std` decl: kind=%v, want KindImport", stdDecl.Kind)
	}
}

// TestImportGraph_Zig_VarImport: `var m = @import(...)` (instead of
// const) is recognised the same way.
func TestImportGraph_Zig_VarImport(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.zig":  "pub fn X() void {}\n",
		"user.zig": "var m = @import(\"foo.zig\");\npub fn go() void { m.X(); }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	rfoo := idx.ResultFor(root, "foo.zig")
	ruser := idx.ResultFor(root, "user.zig")
	if rfoo == nil || ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	x := findDecl(rfoo, "X")
	if x == nil {
		t.Fatalf("foo.zig: no X decl")
	}
	refs := refsByName(ruser, "X")
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == x.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("user.zig: X did not resolve via var import; bindings=%v", bindingsFor(refs))
	}
}

// TestImportGraph_Zig_NotAnImportRHS: `const x = 5` (no @import) keeps
// x as KindConst, not KindImport.
func TestImportGraph_Zig_NotAnImportRHS(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"user.zig": "const x = 5;\nconst y = something();\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	ruser := idx.ResultFor(root, "user.zig")
	if ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	for _, name := range []string{"x", "y"} {
		d := findDecl(ruser, name)
		if d == nil {
			t.Fatalf("no `%s` decl", name)
		}
		if d.Kind == scope.KindImport {
			t.Errorf("`%s` rewritten to KindImport, want KindConst", name)
		}
	}
}

// TestImportGraph_Zig_ImportInsideExpression: `@import` appearing as a
// nested call argument (not the top-level RHS of a const/var) must not
// retroactively rewrite the buffered decl. This guards against the
// `const Foo = bar(@import("x.zig"))` shape — though `Foo`'s value is
// derived from the import, the const itself is the function result,
// not the module.
func TestImportGraph_Zig_ImportInsideExpression(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"x.zig":    "pub fn Y() void {}\n",
		"user.zig": "const Foo = bar(@import(\"x.zig\"));\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, _ := Load(edrDir)
	ruser := idx.ResultFor(root, "user.zig")
	if ruser == nil {
		t.Fatalf("ResultFor nil")
	}
	foo := findDecl(ruser, "Foo")
	if foo == nil {
		t.Fatalf("no `Foo` decl")
	}
	if foo.Kind == scope.KindImport {
		t.Errorf("Foo rewritten to KindImport when @import was inside an expression; should be KindConst")
	}
}
