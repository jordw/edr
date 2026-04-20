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

// TestImportGraph_Go_ModFilePrefixResolution: a go.mod declaring
// `module example.com/proj` lets an import of
// `example.com/proj/internal/pkg` resolve to the repo-relative dir
// `internal/pkg/` — the k8s.io/kubernetes / monorepo case. Without
// the go.mod parse, the existing suffix heuristic would also find
// `internal/pkg` because the full dir happens to be a suffix — so
// the diagnostic value of this test is that resolution still works
// when the module path is NOT the suffix of any in-repo dir. The
// `deep/nested` dir below has no "example.com/proj" suffix to match,
// only the full module path does.
func TestImportGraph_Go_ModFilePrefixResolution(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"go.mod": "module example.com/proj\n\ngo 1.22\n",
		"deep/nested/lib.go": `package nested

func Helper() {}
`,
		"cmd/main.go": `package main

import "example.com/proj/deep/nested"

func main() {
	nested.Helper()
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
	rmain := idx.ResultFor(root, "cmd/main.go")
	rlib := idx.ResultFor(root, "deep/nested/lib.go")
	if rmain == nil || rlib == nil {
		t.Fatalf("ResultFor nil: main=%v lib=%v", rmain, rlib)
	}
	helper := findDecl(rlib, "Helper")
	if helper == nil {
		t.Fatalf("lib.go: no Helper decl; decls=%v", declsOf(rlib))
	}
	refs := refsByName(rmain, "Helper")
	if len(refs) == 0 {
		t.Fatal("main.go: no refs to Helper")
	}
	var found bool
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == helper.ID && r.Binding.Reason == "import_export" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("main.go: Helper ref did not resolve to lib.go via go.mod prefix; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Go_NestedModuleWinsOverOuter: a monorepo with an
// outer go.mod and a nested go.mod. An import whose path sits under
// the nested module should resolve against the nested go.mod's dir,
// not the outer. Validates the deepest-first sort of readGoModules.
func TestImportGraph_Go_NestedModuleWinsOverOuter(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"go.mod": "module example.com/outer\n",
		"sub/go.mod": "module example.com/outer/sub\n",
		"sub/pkg/lib.go": `package pkg

func Inner() {}
`,
		"cmd/main.go": `package main

import "example.com/outer/sub/pkg"

func main() { pkg.Inner() }
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rmain := idx.ResultFor(root, "cmd/main.go")
	rlib := idx.ResultFor(root, "sub/pkg/lib.go")
	if rmain == nil || rlib == nil {
		t.Fatalf("ResultFor nil: main=%v lib=%v", rmain, rlib)
	}
	inner := findDecl(rlib, "Inner")
	if inner == nil {
		t.Fatalf("sub/pkg/lib.go: no Inner decl")
	}
	refs := refsByName(rmain, "Inner")
	found := false
	for _, r := range refs {
		if r.Binding.Decl == inner.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("main.go: Inner ref did not resolve to sub/pkg/lib.go; bindings=%v",
			bindingsFor(refs))
	}
}

// TestImportGraph_Go_ModFileWithComments: go.mod files may have
// comments before the module directive and trailing line comments on
// the directive line. Both must be tolerated by parseGoModulePath.
func TestImportGraph_Go_ModFileWithComments(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"go.mod": "// top-of-file comment\n\nmodule example.com/cmt // trailing\n\ngo 1.22\n",
		"api/api.go": `package api
func Ping() {}
`,
		"cmd/m.go": `package main

import "example.com/cmt/api"

func main() { api.Ping() }
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rapi := idx.ResultFor(root, "api/api.go")
	rm := idx.ResultFor(root, "cmd/m.go")
	ping := findDecl(rapi, "Ping")
	if ping == nil {
		t.Fatalf("api.go: no Ping decl")
	}
	found := false
	for _, r := range refsByName(rm, "Ping") {
		if r.Binding.Decl == ping.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("m.go: Ping ref did not resolve across go.mod-with-comments import")
	}
}

// TestImportGraph_Go_SuffixFallbackStillWorks: a repo with no go.mod
// must still resolve via the suffix heuristic — preserves the v0
// behavior tests depended on.
func TestImportGraph_Go_SuffixFallbackStillWorks(t *testing.T) {
	// Intentionally no go.mod.
	root, edrDir := setupRepo(t, map[string]string{
		"a/a.go": `package a
import "github.com/any/b"
func M() { b.Exp() }
`,
		"b/b.go": `package b
func Exp() {}
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
	exp := findDecl(rb, "Exp")
	if exp == nil {
		t.Fatalf("b.go: no Exp decl")
	}
	found := false
	for _, r := range refsByName(ra, "Exp") {
		if r.Binding.Decl == exp.ID && r.Binding.Reason == "import_export" {
			found = true
		}
	}
	if !found {
		t.Errorf("a.go: Exp ref did not resolve via suffix fallback")
	}
}

// TestImportGraph_Go_ModulePathBelongsExternal: an import path that
// starts with a known module prefix but points to a subdir not
// present in the repo (e.g. a sibling package that simply isn't
// checked in) must NOT fall back to the suffix heuristic — the
// deepest module owning the path is authoritative. Otherwise a
// typo or a genuinely-external subpath could get silently bound to
// an unrelated in-repo dir that happens to share a suffix.
func TestImportGraph_Go_ModulePathBelongsExternal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"go.mod": "module example.com/proj\n",
		// note: `trap` has a suffix-match hook for `example.com/proj/trap`,
		// but the module says proj owns it and there's no `trap/` in
		// the repo layout under the module. Expectation: unresolved.
		"other/trap/trap.go": `package trap
func Pulled() {}
`,
		"cmd/m.go": `package main
import "example.com/proj/trap"
func main() { trap.Pulled() }
`,
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rm := idx.ResultFor(root, "cmd/m.go")
	for _, r := range refsByName(rm, "Pulled") {
		if r.Binding.Reason == "import_export" {
			t.Errorf("external-to-module path should NOT bind via suffix fallback; got %+v", r.Binding)
		}
	}
}

// TestParseGoModulePath: direct unit coverage for the tiny parser so
// future edits don't regress edge cases silently.
func TestParseGoModulePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "module example.com/foo\n", "example.com/foo"},
		{"leading_blank_lines", "\n\nmodule example.com/foo\n", "example.com/foo"},
		{"top_comment", "// copyright\nmodule example.com/foo\n", "example.com/foo"},
		{"trailing_comment", "module example.com/foo // v2\n", "example.com/foo"},
		{"quoted", "module \"example.com/foo\"\n", "example.com/foo"},
		{"tab_separator", "module\texample.com/foo\n", "example.com/foo"},
		{"with_require", "module example.com/foo\nrequire a.b/c v1.0.0\n", "example.com/foo"},
		{"empty_file", "", ""},
		{"no_directive", "go 1.22\n", ""},
		{"modulesomething_ignored", "modulething foo\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseGoModulePath([]byte(tc.in))
			if got != tc.want {
				t.Errorf("parseGoModulePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
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
