package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Rust_CrossFileUseItem: `use crate::foo::Bar;` in main.rs
// binds to Bar in foo.rs.
func TestImportGraph_Rust_CrossFileUseItem(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.rs":  "pub struct Bar {}\n",
		"main.rs": "use crate::foo::Bar;\nfn f() { let _ = Bar; }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	foo := idx.ResultFor(root, "foo.rs")
	main := idx.ResultFor(root, "main.rs")
	if foo == nil || main == nil {
		t.Fatalf("ResultFor nil: foo=%v main=%v", foo, main)
	}
	fooBar := findDecl(foo, "Bar")
	if fooBar == nil {
		t.Fatalf("foo.rs: no Bar decl; decls=%v", foo.Decls)
	}
	if !fooBar.Exported {
		t.Errorf("foo.rs Bar: Exported=false, want true")
	}
	refs := refsByName(main, "Bar")
	if len(refs) == 0 {
		t.Fatal("main.rs: no refs to Bar")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == fooBar.ID && r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("main.rs: Bar ref not cross-resolved to foo.rs; bindings=%v", bindingsFor(refs))
	}
}

// TestImportGraph_Rust_BracedUse: `use crate::foo::{A, B};` rewrites both.
func TestImportGraph_Rust_BracedUse(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.rs":  "pub struct A {}\npub struct B {}\n",
		"main.rs": "use crate::foo::{A, B};\nfn f() { let _ = A; let _ = B; }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	foo := idx.ResultFor(root, "foo.rs")
	main := idx.ResultFor(root, "main.rs")
	if foo == nil || main == nil {
		t.Fatal("ResultFor nil")
	}
	for _, name := range []string{"A", "B"} {
		srcDecl := findDecl(foo, name)
		if srcDecl == nil {
			t.Fatalf("foo.rs: no %s decl", name)
		}
		refs := refsByName(main, name)
		if len(refs) == 0 {
			t.Errorf("main.rs: no refs to %s", name)
			continue
		}
		matched := false
		for _, r := range refs {
			if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == srcDecl.ID && r.Binding.Reason == "import_export" {
				matched = true
			}
		}
		if !matched {
			t.Errorf("main.rs: %s ref not cross-resolved; bindings=%v", name, bindingsFor(refs))
		}
	}
}

// TestImportGraph_Rust_AliasedUse: `use foo::Bar as Qux;` — refs to Qux
// in main.rs bind to foo.rs's Bar.
func TestImportGraph_Rust_AliasedUse(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.rs":  "pub struct Bar {}\n",
		"main.rs": "use crate::foo::Bar as Qux;\nfn f() { let _ = Qux; }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	foo := idx.ResultFor(root, "foo.rs")
	main := idx.ResultFor(root, "main.rs")
	if foo == nil || main == nil {
		t.Fatal("ResultFor nil")
	}
	fooBar := findDecl(foo, "Bar")
	if fooBar == nil {
		t.Fatalf("foo.rs: no Bar decl")
	}
	refs := refsByName(main, "Qux")
	if len(refs) == 0 {
		t.Fatal("main.rs: no refs to Qux")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == fooBar.ID && r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("main.rs: Qux ref not cross-resolved to foo.rs Bar; bindings=%v", bindingsFor(refs))
	}
}

// TestImportGraph_Rust_NonPubStaysLocal: a non-pub decl doesn't resolve
// as an export target; the importer's ref stays on the local Import.
func TestImportGraph_Rust_NonPubStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"foo.rs":  "struct Hidden {}\n",
		"main.rs": "use crate::foo::Hidden;\nfn f() { let _ = Hidden; }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	main := idx.ResultFor(root, "main.rs")
	if main == nil {
		t.Fatal("ResultFor main.rs nil")
	}
	imp := findDecl(main, "Hidden")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("main.rs: no Hidden import decl; got %+v", imp)
	}
	refs := refsByName(main, "Hidden")
	if len(refs) == 0 {
		t.Fatal("main.rs: no refs to Hidden")
	}
	for _, r := range refs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("non-pub decl should not cross-resolve: %+v", r.Binding)
		}
	}
}

// TestImportGraph_Rust_ExternalCrateStaysLocal: `use std::fmt::Display;`
// has no corresponding repo file; the resolver must leave the binding on
// the local Import decl (no import_export reason).
func TestImportGraph_Rust_ExternalCrateStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"main.rs": "use std::fmt::Display;\nfn f() { let _ = Display; }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	main := idx.ResultFor(root, "main.rs")
	if main == nil {
		t.Fatal("ResultFor main.rs nil")
	}
	imp := findDecl(main, "Display")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("main.rs: no Display import decl; got %+v", imp)
	}
	refs := refsByName(main, "Display")
	if len(refs) == 0 {
		t.Fatal("main.rs: no refs to Display")
	}
	for _, r := range refs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("external crate should not cross-resolve: %+v", r.Binding)
		}
		if r.Binding.Kind != scope.BindResolved || r.Binding.Decl != imp.ID {
			t.Errorf("expected binding to local import %d, got %+v", imp.ID, r.Binding)
		}
	}
}
