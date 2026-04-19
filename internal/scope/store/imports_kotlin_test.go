package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// TestImportGraph_Kotlin_CrossFileClass: an import of a class from
// another file in the same repo rewrites refs to the imported class
// in the importer to point at the originating DeclID.
func TestImportGraph_Kotlin_CrossFileClass(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/Foo.kt": `package com.acme

class Foo {
    fun bar() = 1
}
`,
		"b/Main.kt": `package com.acme.main

import com.acme.Foo

fun use() {
    val x = Foo()
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
	ra := idx.ResultFor(root, "a/Foo.kt")
	rb := idx.ResultFor(root, "b/Main.kt")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil || aFoo.Kind != scope.KindClass {
		t.Fatalf("a/Foo.kt: no Foo class decl; got %+v", aFoo)
	}
	if !aFoo.Exported {
		t.Errorf("a/Foo.kt Foo: Exported=false, want true (default public)")
	}
	bImport := findDecl(rb, "Foo")
	if bImport == nil || bImport.Kind != scope.KindImport {
		t.Fatalf("b/Main.kt: no Foo import decl; got %+v", bImport)
	}
	refs := refsByName(rb, "Foo")
	if len(refs) == 0 {
		t.Fatal("b/Main.kt: no refs to Foo")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aFoo.ID {
			found = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("b/Main.kt Foo ref reason = %q, want \"import_export\"", r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("b/Main.kt: no ref to Foo resolved to a/Foo.kt's DeclID %d; got bindings=%v",
			aFoo.ID, bindingsFor(refs))
	}
}

// TestImportGraph_Kotlin_TopLevelFun: importing a top-level fun rewrites
// refs — Kotlin-specific vs Java (which only has class members).
func TestImportGraph_Kotlin_TopLevelFun(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"util/Util.kt": `package com.acme.util

fun greet(name: String): String = "hi, " + name
`,
		"app/App.kt": `package com.acme.app

import com.acme.util.greet

fun run() {
    greet("world")
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
	ra := idx.ResultFor(root, "util/Util.kt")
	rb := idx.ResultFor(root, "app/App.kt")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	aGreet := findDecl(ra, "greet")
	if aGreet == nil {
		t.Fatalf("util/Util.kt: no greet decl; decls=%v", ra.Decls)
	}
	if aGreet.Kind != scope.KindFunction {
		t.Errorf("util/Util.kt greet: Kind=%v, want function", aGreet.Kind)
	}
	if !aGreet.Exported {
		t.Errorf("util/Util.kt greet: Exported=false, want true")
	}
	refs := refsByName(rb, "greet")
	if len(refs) == 0 {
		t.Fatal("app/App.kt: no refs to greet")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aGreet.ID {
			found = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("greet ref reason = %q, want \"import_export\"", r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("app/App.kt: greet ref not rewritten to util/Util.kt greet (DeclID %d); bindings=%v",
			aGreet.ID, bindingsFor(refs))
	}
}

// TestImportGraph_Kotlin_AliasedImport: `import com.acme.Foo as Bar`;
// refs to Bar in the importer bind to Foo's DeclID from the source file.
func TestImportGraph_Kotlin_AliasedImport(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/Foo.kt": `package com.acme

class Foo
`,
		"b/Main.kt": `package com.acme.main

import com.acme.Foo as MyFoo

fun use() {
    val x = MyFoo()
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
	ra := idx.ResultFor(root, "a/Foo.kt")
	rb := idx.ResultFor(root, "b/Main.kt")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	aFoo := findDecl(ra, "Foo")
	if aFoo == nil {
		t.Fatalf("a/Foo.kt: no Foo decl")
	}
	// The local binding in b is MyFoo, not Foo.
	if findDecl(rb, "Foo") != nil {
		t.Errorf("b/Main.kt: unexpected `Foo` decl when aliased to MyFoo; decls=%v", rb.Decls)
	}
	refs := refsByName(rb, "MyFoo")
	if len(refs) == 0 {
		t.Fatal("b/Main.kt: no refs to MyFoo")
	}
	matched := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aFoo.ID &&
			r.Binding.Reason == "import_export" {
			matched = true
		}
	}
	if !matched {
		t.Errorf("b/Main.kt: MyFoo ref not rewritten to a/Foo.kt Foo (ID %d); bindings=%v",
			aFoo.ID, bindingsFor(refs))
	}
}

// TestImportGraph_Kotlin_PrivateStaysLocal: a `private class` is not
// exported, so an import cannot resolve to it. The ref stays bound to
// the local import decl (or the fallback scope-chain binding), not to
// the other file's private decl.
func TestImportGraph_Kotlin_PrivateStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/Secret.kt": `package com.acme

private class Secret
`,
		"b/Main.kt": `package com.acme.main

import com.acme.Secret

fun use() {
    val x = Secret()
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
	ra := idx.ResultFor(root, "a/Secret.kt")
	rb := idx.ResultFor(root, "b/Main.kt")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	aSecret := findDecl(ra, "Secret")
	if aSecret == nil {
		t.Fatalf("a/Secret.kt: no Secret decl")
	}
	if aSecret.Exported {
		t.Errorf("a/Secret.kt Secret: Exported=true, want false (private)")
	}
	imp := findDecl(rb, "Secret")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("b/Main.kt: no Secret import decl; got %+v", imp)
	}
	refs := refsByName(rb, "Secret")
	if len(refs) == 0 {
		t.Fatal("b/Main.kt: no refs to Secret")
	}
	// The ref must NOT be rewritten to aSecret (it's private). It
	// should stay bound to the local import decl with a non-
	// import_export reason.
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == aSecret.ID {
			t.Errorf("b/Main.kt: Secret ref incorrectly rewrote to private a/Secret.kt DeclID %d",
				aSecret.ID)
		}
		if r.Binding.Reason == "import_export" {
			t.Errorf("b/Main.kt: Secret ref has import_export reason despite private source: %+v",
				r.Binding)
		}
	}
}

// TestImportGraph_Kotlin_MultipleTopLevel: Kotlin allows multiple
// top-level decls per file. Each must be individually importable by
// FQN.
func TestImportGraph_Kotlin_MultipleTopLevel(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/Helpers.kt": `package com.acme

class One
class Two
fun helper() = 0
`,
		"b/Main.kt": `package com.acme.main

import com.acme.One
import com.acme.Two
import com.acme.helper

fun use() {
    val a = One()
    val b = Two()
    val c = helper()
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
	ra := idx.ResultFor(root, "a/Helpers.kt")
	rb := idx.ResultFor(root, "b/Main.kt")
	if ra == nil || rb == nil {
		t.Fatalf("ResultFor nil: a=%v b=%v", ra, rb)
	}
	for _, name := range []string{"One", "Two", "helper"} {
		src := findDecl(ra, name)
		if src == nil {
			t.Errorf("a/Helpers.kt: no %s decl", name)
			continue
		}
		if !src.Exported {
			t.Errorf("a/Helpers.kt %s: Exported=false, want true", name)
			continue
		}
		refs := refsByName(rb, name)
		if len(refs) == 0 {
			t.Errorf("b/Main.kt: no refs to %s", name)
			continue
		}
		found := false
		for _, r := range refs {
			if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == src.ID &&
				r.Binding.Reason == "import_export" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("b/Main.kt: %s ref not rewritten to a/Helpers.kt DeclID %d; bindings=%v",
				name, src.ID, bindingsFor(refs))
		}
	}
}

// TestImportGraph_Kotlin_WildcardPunted: `import com.acme.*` is the
// v1 punt — no decl is emitted for the wildcard, and the resolver does
// not enumerate the target package's exports. Importer refs to those
// names remain unresolved or scope-chain-bound.
func TestImportGraph_Kotlin_WildcardPunted(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"a/Foo.kt": `package com.acme

class Foo
`,
		"b/Main.kt": `package com.acme.main

import com.acme.*

fun use() {
    val x = Foo()
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
	rb := idx.ResultFor(root, "b/Main.kt")
	if rb == nil {
		t.Fatalf("ResultFor b/Main.kt = nil")
	}
	// Confirm no wildcard decl emitted in b.
	for _, d := range rb.Decls {
		if d.Name == "*" {
			t.Errorf("wildcard should not emit a `*` decl; got %+v", d)
		}
	}
	// The Foo ref in b should NOT have reason="import_export" since the
	// wildcard path is punted. This documents current behavior.
	for _, r := range refsByName(rb, "Foo") {
		if r.Binding.Reason == "import_export" {
			t.Errorf("wildcard punt: Foo ref unexpectedly rewritten: %+v", r.Binding)
		}
	}
}
