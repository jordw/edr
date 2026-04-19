package store

import (
	"fmt"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// findDecl returns the first Decl in r whose Name matches, or nil.
func findDecl(r *scope.Result, name string) *scope.Decl {
	for i := range r.Decls {
		if r.Decls[i].Name == name {
			return &r.Decls[i]
		}
	}
	return nil
}

// refsByName returns every Ref in r with the given name, in source order.
func refsByName(r *scope.Result, name string) []scope.Ref {
	var out []scope.Ref
	for _, ref := range r.Refs {
		if ref.Name == name {
			out = append(out, ref)
		}
	}
	return out
}

// bindingsFor formats ref bindings for test error messages.
func bindingsFor(refs []scope.Ref) []string {
	out := make([]string, len(refs))
	for i, r := range refs {
		out[i] = fmt.Sprintf("{decl=%d kind=%d reason=%q}", r.Binding.Decl, r.Binding.Kind, r.Binding.Reason)
	}
	return out
}

// TestImportGraph_CrossFileValueImport: after Build, a ref to an imported
// class in b.ts resolves to the original class's DeclID in a.ts, not to
// b.ts's local KindImport decl.
func TestImportGraph_CrossFileValueImport(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.ts": "export class Foo {\n  bar() { return 1 }\n}\n",
		"b.ts": "import { Foo } from './a'\nconst x = new Foo()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a.ts")
	rb := idx.ResultFor(root, "b.ts")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil {
		t.Fatalf("a.ts: no Foo decl")
	}
	if !aFoo.Exported {
		t.Errorf("a.ts Foo: Exported=false, want true")
	}
	bImport := findDecl(rb, "Foo")
	if bImport == nil || bImport.Kind != scope.KindImport {
		t.Fatalf("b.ts: no Foo import decl; got %+v", bImport)
	}
	// The ref to Foo in b.ts must bind to a.ts's Foo decl, not to the
	// local import decl.
	refs := refsByName(rb, "Foo")
	if len(refs) == 0 {
		t.Fatal("b.ts: no refs to Foo")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aFoo.ID {
			found = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("b.ts Foo ref reason = %q, want \"import_export\"", r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("b.ts: no ref to Foo resolved to a.ts's DeclID %d; got bindings=%v",
			aFoo.ID, bindingsFor(refs))
	}
}

// TestImportGraph_TypeOnlyImport: `import type { X }` resolves the same
// way as a value import.
func TestImportGraph_TypeOnlyImport(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.ts": "export interface Config { debug: boolean }\n",
		"b.ts": "import type { Config } from './a'\nfunction f(c: Config) { return c }\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a.ts")
	rb := idx.ResultFor(root, "b.ts")
	aCfg := findDecl(ra, "Config")
	if aCfg == nil {
		t.Fatalf("a.ts: no Config decl")
	}
	if !aCfg.Exported {
		t.Errorf("a.ts Config: Exported=false, want true")
	}
	refs := refsByName(rb, "Config")
	if len(refs) == 0 {
		t.Fatal("b.ts: no refs to Config")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aCfg.ID && r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("b.ts: no type-only-imported ref to Config resolved to a.ts; bindings=%v", bindingsFor(refs))
	}
}

// TestImportGraph_AliasedImport: `import { Foo as Bar } from './a'`; refs
// to Bar in b.ts bind to a.ts's Foo.
func TestImportGraph_AliasedImport(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.ts": "export class Foo {}\n",
		"b.ts": "import { Foo as Bar } from './a'\nconst x = new Bar()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a.ts")
	rb := idx.ResultFor(root, "b.ts")
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil {
		t.Fatalf("a.ts: no Foo decl")
	}
	refs := refsByName(rb, "Bar")
	if len(refs) == 0 {
		t.Fatal("b.ts: no refs to Bar")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aFoo.ID && r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("b.ts: Bar ref not rewritten to a.ts Foo (ID %d); bindings=%v", aFoo.ID, bindingsFor(refs))
	}
}

// TestImportGraph_NamespaceImport: `import * as m from './a'`. v1 leaves
// the namespace handle bound to the local Import decl (property-access
// rewrite is future work). Just verify m doesn't crash and that it binds
// somewhere sane.
func TestImportGraph_NamespaceImport(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.ts": "export class Foo {}\n",
		"b.ts": "import * as m from './a'\nconst x = m\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rb := idx.ResultFor(root, "b.ts")
	mDecl := findDecl(rb, "m")
	if mDecl == nil || mDecl.Kind != scope.KindImport {
		t.Fatalf("b.ts: no m import decl; got %+v", mDecl)
	}
	refs := refsByName(rb, "m")
	if len(refs) == 0 {
		t.Fatal("b.ts: no refs to m")
	}
	// v1: binding should resolve to the local import decl (direct_scope
	// walk finds it). The import-graph resolver leaves namespace imports
	// alone.
	for _, r := range refs {
		if r.Binding.Kind != scope.BindResolved {
			t.Errorf("b.ts m ref unresolved: %+v", r.Binding)
		}
	}
}

// TestImportGraph_ExtensionFallback: import './a' resolves when only a.ts
// exists (no .d.ts, no a/index.ts).
func TestImportGraph_ExtensionFallback(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a.ts": "export function greet() {}\n",
		"b.ts": "import { greet } from './a'\ngreet()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	ra := idx.ResultFor(root, "a.ts")
	rb := idx.ResultFor(root, "b.ts")
	aGreet := findDecl(ra, "greet")
	if aGreet == nil {
		t.Fatalf("a.ts: no greet decl")
	}
	refs := refsByName(rb, "greet")
	if len(refs) == 0 {
		t.Fatal("b.ts: no refs to greet")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aGreet.ID && r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("b.ts: greet ref did not cross-resolve (extension fallback); bindings=%v", bindingsFor(refs))
	}
}

// TestImportGraph_NonRelativeImport: `import { X } from 'react'` has no
// corresponding file; the resolver must leave the binding as-is (local
// import decl) and not crash.
func TestImportGraph_NonRelativeImport(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"b.ts": "import { useState } from 'react'\nconst x = useState(0)\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rb := idx.ResultFor(root, "b.ts")
	imp := findDecl(rb, "useState")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("b.ts: no useState import decl; got %+v", imp)
	}
	refs := refsByName(rb, "useState")
	if len(refs) == 0 {
		t.Fatal("b.ts: no refs to useState")
	}
	// External: ref binds to the local Import decl (direct_scope).
	for _, r := range refs {
		if r.Binding.Kind != scope.BindResolved || r.Binding.Decl != imp.ID {
			t.Errorf("non-relative import rewrote ref: %+v (want binding to local import %d)",
				r.Binding, imp.ID)
		}
		if r.Binding.Reason == "import_export" {
			t.Errorf("non-relative import should not have import_export reason: %+v", r.Binding)
		}
	}
}

// TestImportGraph_DefaultImport_LiteralDefault: a file exporting a decl
// literally named "default" is bound by default imports. (Pragmatic punt:
// `export default <expr>` is not yet tracked.)
func TestImportGraph_DefaultImport_Punted(t *testing.T) {
	// v1: `export default class Foo {}` is NOT rewritten by the resolver
	// because we don't track the "default" name for that form. The test
	// just documents the current behavior — ref stays on the local import.
	root, edrDir := setupRepo(t, map[string]string{
		"a.ts": "export default class Foo {}\n",
		"b.ts": "import Foo from './a'\nconst x = new Foo()\n",
	})
	if _, err := Build(root, edrDir, walkDir(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	idx, err := Load(edrDir)
	if err != nil || idx == nil {
		t.Fatalf("Load: err=%v idx=%v", err, idx)
	}
	rb := idx.ResultFor(root, "b.ts")
	imp := findDecl(rb, "Foo")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("b.ts: no Foo import decl")
	}
	refs := refsByName(rb, "Foo")
	for _, r := range refs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("default import should not currently resolve via graph: %+v", r.Binding)
		}
	}
}
