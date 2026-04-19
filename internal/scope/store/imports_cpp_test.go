package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Cpp_IncludeResolves: a function defined in foo.hpp
// (included from main.cpp via `#include "foo.hpp"`) is bound in
// main.cpp's ref list to the header's DeclID.
func TestImportGraph_Cpp_IncludeResolves(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.hpp":  "int greet(int x) { return x; }\n",
		"main.cpp": "#include \"foo.hpp\"\nint caller(int a) { return greet(a); }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rFoo := idx.ResultFor(root, "foo.hpp")
	rMain := idx.ResultFor(root, "main.cpp")
	if rFoo == nil || rMain == nil {
		t.Fatalf("ResultFor nil: foo=%v main=%v", rFoo, rMain)
	}
	fooGreet := findDecl(rFoo, "greet")
	if fooGreet == nil {
		t.Fatalf("foo.hpp: no greet decl")
	}
	if !fooGreet.Exported {
		t.Errorf("foo.hpp greet: Exported=false, want true")
	}
	refs := refsByName(rMain, "greet")
	if len(refs) == 0 {
		t.Fatal("main.cpp: no refs to greet")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == fooGreet.ID {
			matched = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("greet ref reason=%q, want %q", r.Binding.Reason, "import_export")
			}
		}
	}
	if !matched {
		t.Errorf("main.cpp: greet ref did not resolve to foo.hpp DeclID %d; bindings=%v",
			fooGreet.ID, bindingsFor(refs))
	}
}

// TestImportGraph_Cpp_UsingDeclaration: `using Foo::Bar;` rewrites a
// ref to Bar in the using'er file to the exported decl inside
// namespace Foo.
func TestImportGraph_Cpp_UsingDeclaration(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.hpp": "namespace Foo {\n  class Bar {};\n}\n",
		"main.cpp": "#include \"foo.hpp\"\n" +
			"using Foo::Bar;\n" +
			"int consume(int x) { Bar b; return x; }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rFoo := idx.ResultFor(root, "foo.hpp")
	rMain := idx.ResultFor(root, "main.cpp")
	if rFoo == nil || rMain == nil {
		t.Fatalf("ResultFor nil: foo=%v main=%v", rFoo, rMain)
	}
	fooBar := findDecl(rFoo, "Bar")
	if fooBar == nil {
		t.Fatalf("foo.hpp: no Bar decl")
	}
	if !fooBar.Exported {
		t.Errorf("foo.hpp Bar: Exported=false, want true")
	}
	// There should be a using-declaration decl named Bar in main.cpp.
	usingBar := findDecl(rMain, "Bar")
	if usingBar == nil {
		t.Fatalf("main.cpp: no Bar using-decl import")
	}
	// Refs to Bar in main.cpp should now point at Foo::Bar's DeclID,
	// not at the local using import decl.
	refs := refsByName(rMain, "Bar")
	if len(refs) == 0 {
		t.Fatal("main.cpp: no refs to Bar")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == fooBar.ID && r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("main.cpp: Bar ref not rewritten to Foo::Bar DeclID %d; bindings=%v",
			fooBar.ID, bindingsFor(refs))
	}
}

// TestImportGraph_Cpp_StaticStaysLocal: a `static` function declared
// in foo.hpp is NOT exported — callers that `#include "foo.hpp"`
// should NOT cross-resolve to it. (In practice you'd never put a
// static in a header; this test documents the linkage rule.)
func TestImportGraph_Cpp_StaticStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.hpp":  "static int helper(int x) { return x; }\n",
		"main.cpp": "#include \"foo.hpp\"\nint caller(int a) { return helper(a); }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rFoo := idx.ResultFor(root, "foo.hpp")
	rMain := idx.ResultFor(root, "main.cpp")
	helper := findDecl(rFoo, "helper")
	if helper == nil {
		t.Fatalf("foo.hpp: no helper decl")
	}
	if helper.Exported {
		t.Errorf("foo.hpp static helper: Exported=true, want false")
	}
	// main.cpp's helper ref must NOT have been rewritten to foo.hpp's
	// helper decl. It may remain unresolved (missing_import) or bind
	// to a builtin; either way, its Decl must not equal helper.ID.
	refs := refsByName(rMain, "helper")
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == helper.ID {
			t.Errorf("main.cpp helper: bound to static foo.hpp helper, want unresolved or external; binding=%+v", r.Binding)
		}
	}
}

// TestImportGraph_Cpp_SystemIncludeStaysLocal: `#include <vector>` is
// a system include; there is no repo-local file to bind against, so
// refs to system names must remain unresolved (or builtin-bound).
// The resolver must not crash and must not wildly rewrite anything.
func TestImportGraph_Cpp_SystemIncludeStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"main.cpp": "#include <vector>\nint use(int x) { vector v; return x; }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rMain := idx.ResultFor(root, "main.cpp")
	imp := findDecl(rMain, "vector")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("main.cpp: no vector import decl; got %+v", imp)
	}
	// The include decl must carry the <> marker.
	if imp.Signature != "vector\x00<>" {
		t.Errorf("Signature=%q, want %q", imp.Signature, "vector\x00<>")
	}
	// Refs to `vector` in main.cpp must not have been rewritten to
	// any cross-file DeclID (there's none to bind to). The existing
	// scope resolution may bind vector to the local import decl;
	// that's fine — just ensure we don't produce a spurious
	// import_export cross-file rewrite.
	refs := refsByName(rMain, "vector")
	for _, r := range refs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("system include produced spurious import_export binding: %+v", r.Binding)
		}
	}
}
