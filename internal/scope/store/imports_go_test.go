package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Go_CrossFilePackageCall: file a.go imports pkg b;
// `b.Foo()` in a.go rewrites to b.Foo in b/b.go. This is the core
// cross-package resolution case.
func TestImportGraph_Go_CrossFilePackageCall(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/a.go": `package a

import "github.com/example/b"

func Main() {
	b.Foo()
}
`,
		"b/b.go": `package b

func Foo() {}
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a/a.go")
	rb := idx.ResultFor(root, "b/b.go")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	bFoo := findDecl(rb, "Foo")
	if bFoo == nil {
		t.Fatalf("b.go: no Foo decl; decls=%v", declsOf(rb))
	}
	if !bFoo.Exported {
		t.Errorf("b.go Foo: Exported=false, want true (uppercase name)")
	}
	// The Foo ref in a.go should resolve to b.go's Foo.
	fooRefs := refsByName(ra, "Foo")
	if len(fooRefs) == 0 {
		t.Fatal("a.go: no refs to Foo")
	}
	found := false
	for _, r := range fooRefs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == bFoo.ID {
			found = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("a.go Foo ref reason = %q, want \"import_export\"", r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("a.go: Foo ref did not resolve cross-file to b.go Foo (ID %d); bindings=%v",
			bFoo.ID, bindingsFor(fooRefs))
	}
}

// TestImportGraph_Go_UnexportedStaysLocal: lowercase `b.foo()` should
// NOT be rewritten because `foo` is not exported in package b (per
// Go's capitalization rule).
func TestImportGraph_Go_UnexportedStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/a.go": `package a

import "github.com/example/b"

func Main() {
	b.foo()
}
`,
		"b/b.go": `package b

func foo() {}
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a/a.go")
	fooRefs := refsByName(ra, "foo")
	for _, r := range fooRefs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("unexported foo should not rewrite via import graph: %+v", r.Binding)
		}
	}
}

// TestImportGraph_Go_ExternalPackageStaysLocal: import of an external
// package (not in the repo) leaves the property ref bound to the
// local Import decl — the resolver only resolves repo-internal targets.
func TestImportGraph_Go_ExternalPackageStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/a.go": `package a

import "github.com/external/pkg"

func Main() {
	pkg.Thing()
}
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a/a.go")
	thingRefs := refsByName(ra, "Thing")
	for _, r := range thingRefs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("external-package property ref should not rewrite: %+v", r.Binding)
		}
	}
}

// declsOf is a tiny debug helper so failing tests show what decls exist.
func declsOf(r *scope.Result) []string {
	out := make([]string, len(r.Decls))
	for i, d := range r.Decls {
		out[i] = d.Name
	}
	return out
}
