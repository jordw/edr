package store

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/java"
)

// TestImportGraph_Java_ResolverDirect drives resolveImportsJava without
// the Build() machinery so failures localize to the resolver itself.
func TestImportGraph_Java_ResolverDirect(t *testing.T) {
	rUtil := java.Parse("com/acme/Util.java", []byte(`package com.acme;

public class Util {
    public int answer() { return 42; }
}
`))
	rMain := java.Parse("com/example/Main.java", []byte(`package com.example;

import com.acme.Util;

public class Main {
    public void go() {
        Util u = new Util();
    }
}
`))
	parsed := []parsedFile{
		{rel: "com/acme/Util.java", result: rUtil},
		{rel: "com/example/Main.java", result: rMain},
	}
	resolveImportsJava(parsed)

	utilDecl := findDecl(rUtil, "Util")
	if utilDecl == nil {
		t.Fatalf("Util.java: no Util class decl")
	}

	got := false
	for _, r := range rMain.Refs {
		if r.Name == "Util" && r.Binding.Kind == scope.BindResolved &&
			r.Binding.Decl == utilDecl.ID && r.Binding.Reason == "import_export" {
			got = true
			break
		}
	}
	if !got {
		t.Errorf("resolver did not rewrite Util ref to Util.java DeclID %d; bindings=%v",
			utilDecl.ID, bindingsFor(rMain.Refs))
	}
}

// TestImportGraph_Java_CrossFilePublicClass: Main.java in package
// com.example imports com.acme.Util; Util is defined in
// com/acme/Util.java. After Build, the ref to Util in Main.java must
// resolve to Util.java's class decl, not to Main.java's local Import
// decl.
func TestImportGraph_Java_CrossFilePublicClass(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"com/acme/Util.java": `package com.acme;

public class Util {
    public int answer() { return 42; }
}
`,
		"com/example/Main.java": `package com.example;

import com.acme.Util;

public class Main {
    public void go() {
        Util u = new Util();
    }
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
	rUtil := idx.ResultFor(root, "com/acme/Util.java")
	rMain := idx.ResultFor(root, "com/example/Main.java")
	if rUtil == nil || rMain == nil {
		t.Fatalf("ResultFor nil: util=%v main=%v", rUtil, rMain)
	}
	utilDecl := findDecl(rUtil, "Util")
	if utilDecl == nil {
		t.Fatalf("Util.java: no Util class decl")
	}
	if !utilDecl.Exported {
		t.Errorf("Util.java Util: Exported=false, want true")
	}
	mainImp := findDecl(rMain, "Util")
	if mainImp == nil || mainImp.Kind != scope.KindImport {
		t.Fatalf("Main.java: no Util import decl; got %+v", mainImp)
	}
	refs := refsByName(rMain, "Util")
	if len(refs) == 0 {
		t.Fatal("Main.java: no refs to Util")
	}
	found := false
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl == utilDecl.ID {
			found = true
			if r.Binding.Reason != "import_export" {
				t.Errorf("Main.java Util ref reason = %q, want \"import_export\"", r.Binding.Reason)
			}
		}
	}
	if !found {
		t.Errorf("Main.java: no Util ref resolved to Util.java DeclID %d (imp=%d); bindings=%v",
			utilDecl.ID, mainImp.ID, bindingsFor(refs))
	}
}

// TestImportGraph_Java_PrivateStaysLocal: a package-private (no
// modifier) top-level class is not Exported, so an import of it from
// another package leaves the ref bound to the local Import decl.
func TestImportGraph_Java_PrivateStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"com/acme/Secret.java": `package com.acme;

class Secret {
    int x;
}
`,
		"com/example/Main.java": `package com.example;

import com.acme.Secret;

public class Main {
    public void go() {
        Secret s;
    }
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
	rMain := idx.ResultFor(root, "com/example/Main.java")
	imp := findDecl(rMain, "Secret")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("Main.java: no Secret import decl; got %+v", imp)
	}
	refs := refsByName(rMain, "Secret")
	if len(refs) == 0 {
		t.Fatal("Main.java: no refs to Secret")
	}
	for _, r := range refs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("Secret ref should NOT have resolved across files (package-private source): %+v", r.Binding)
		}
		// Expected behavior: ref binds to the local Import decl.
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl != imp.ID {
			t.Errorf("Secret ref bound to unexpected decl %d (want local import %d)", r.Binding.Decl, imp.ID)
		}
	}
}

// TestImportGraph_Java_ExternalPackageStaysLocal: import of a type
// whose package has no file in the repo (e.g. java.util.List) stays
// bound to the local Import decl.
func TestImportGraph_Java_ExternalPackageStaysLocal(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"com/example/Main.java": `package com.example;

import java.util.List;

public class Main {
    public void go() {
        List items = null;
    }
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
	rMain := idx.ResultFor(root, "com/example/Main.java")
	imp := findDecl(rMain, "List")
	if imp == nil || imp.Kind != scope.KindImport {
		t.Fatalf("Main.java: no List import decl; got %+v", imp)
	}
	refs := refsByName(rMain, "List")
	if len(refs) == 0 {
		t.Fatal("Main.java: no refs to List")
	}
	for _, r := range refs {
		if r.Binding.Kind == scope.BindResolved && r.Binding.Decl != imp.ID {
			t.Errorf("List ref bound to unexpected decl %d (want local import %d)", r.Binding.Decl, imp.ID)
		}
		if r.Binding.Reason == "import_export" {
			t.Errorf("external import should not resolve: %+v", r.Binding)
		}
	}
}

// TestImportGraph_Java_WildcardPunted documents v1 behavior: a
// `import com.acme.*;` decl is emitted (name "*", Signature
// "com.acme\x00*") but the resolver does NOT widen unqualified refs
// to the package's public types. The wildcard Import decl itself
// never has a real ref pointing at it (no source code references the
// literal name "*"), so this test mainly verifies the resolver does
// not crash and does not cross-resolve in the absence of an explicit
// import.
func TestImportGraph_Java_WildcardPunted(t *testing.T) {
	root, edrDir := setupRepo(t, map[string]string{
		"com/acme/Util.java": `package com.acme;

public class Util {
    public int answer() { return 42; }
}
`,
		"com/example/Main.java": `package com.example;

import com.acme.*;

public class Main {
    public void go() {
        Util u;
    }
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
	rMain := idx.ResultFor(root, "com/example/Main.java")
	// v1: `Util` is NOT cross-resolved via the wildcard. The ref
	// remains unresolved or bound locally — the test is that nothing
	// crashes and no ref gets Reason="import_export".
	refs := refsByName(rMain, "Util")
	for _, r := range refs {
		if r.Binding.Reason == "import_export" {
			t.Errorf("wildcard should not have widened Util ref in v1: %+v", r.Binding)
		}
	}
	// Also verify the "*" import decl is present (builder contract).
	wild := findDecl(rMain, "*")
	if wild == nil || wild.Kind != scope.KindImport {
		t.Errorf("Main.java: expected wildcard KindImport decl named \"*\"; got %+v", wild)
	}
}
