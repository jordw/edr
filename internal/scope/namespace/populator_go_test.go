package namespace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// goPkgFixture writes a tiny Go module rooted at root with the given
// files. Returns the absolute path to the module root.
func goPkgFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestGoPopulator_CrossPackageImport(t *testing.T) {
	root := goPkgFixture(t, map[string]string{
		"go.mod":              "module example.com/m\n\ngo 1.21\n",
		"output/output.go":    "package output\n\nfunc Rel(p string) string { return p }\n",
		"main.go":             "package main\n\nimport \"example.com/m/output\"\n\nfunc main() { _ = output.Rel(\"x\") }\n",
	})
	r := NewGoResolver(root)
	mainPath := filepath.Join(root, "main.go")
	mainRes := r.Result(mainPath)
	if mainRes == nil {
		t.Fatal("main.go did not parse")
	}
	ns := Build(mainPath, mainRes, r, GoPopulator(r))

	// "Rel" should be in the namespace as a SourceImported entry.
	rels := ns.Lookup("Rel")
	if len(rels) == 0 {
		t.Fatalf("expected at least one Rel entry, got none. Namespace: %+v", ns.Entries)
	}
	gotImported := false
	for _, e := range rels {
		if e.Source == SourceImported {
			gotImported = true
		}
	}
	if !gotImported {
		t.Errorf("no SourceImported Rel entry; got %+v", rels)
	}

	// Critical: the DeclID in the namespace entry must equal the
	// DeclID assigned to Rel in the output package, when output.go
	// is parsed via the same resolver. This is the cross-file
	// matching invariant the whole abstraction depends on.
	outputPath := filepath.Join(root, "output", "output.go")
	outputRes := r.Result(outputPath)
	if outputRes == nil {
		t.Fatal("output.go did not parse")
	}
	var targetID scope.DeclID
	for _, d := range outputRes.Decls {
		if d.Name == "Rel" && d.Kind == scope.KindFunction {
			targetID = d.ID
		}
	}
	if targetID == 0 {
		t.Fatal("did not find Rel decl in output.go")
	}
	if !ns.Matches("Rel", targetID) {
		t.Errorf("namespace Rel entries do not include target DeclID %d. Entries: %+v",
			targetID, rels)
	}
}

func TestGoPopulator_SamePackageVisible(t *testing.T) {
	root := goPkgFixture(t, map[string]string{
		"go.mod":     "module example.com/m\n",
		"pkg/a.go":   "package pkg\n\nfunc Helper() int { return 1 }\n",
		"pkg/b.go":   "package pkg\n\nfunc Caller() int { return Helper() }\n",
	})
	r := NewGoResolver(root)
	bPath := filepath.Join(root, "pkg", "b.go")
	bRes := r.Result(bPath)
	ns := Build(bPath, bRes, r, GoPopulator(r))

	if entries := ns.Lookup("Helper"); len(entries) == 0 {
		t.Fatalf("expected Helper in namespace via SourceSamePackage; entries=%+v", ns.Entries)
	}
	gotSamePkg := false
	for _, e := range ns.Lookup("Helper") {
		if e.Source == SourceSamePackage {
			gotSamePkg = true
		}
	}
	if !gotSamePkg {
		t.Errorf("Helper not marked SourceSamePackage")
	}

	// And the cross-file DeclID match: Helper in a.go has the same
	// DeclID as the namespace entry in b.go.
	aPath := filepath.Join(root, "pkg", "a.go")
	aRes := r.Result(aPath)
	var helperID scope.DeclID
	for _, d := range aRes.Decls {
		if d.Name == "Helper" {
			helperID = d.ID
		}
	}
	if helperID == 0 {
		t.Fatal("did not find Helper in a.go")
	}
	if !ns.Matches("Helper", helperID) {
		t.Errorf("namespace does not contain Helpers canonical DeclID %d", helperID)
	}
}

func TestGoPopulator_StdlibIgnored(t *testing.T) {
	// Stdlib imports (no dot in first segment) should not surface as
	// namespace entries — theyre effectively builtins and would
	// pollute the namespace with thousands of names.
	root := goPkgFixture(t, map[string]string{
		"go.mod":  "module example.com/m\n",
		"main.go": "package main\n\nimport \"path/filepath\"\n\nvar _ = filepath.Rel\n",
	})
	r := NewGoResolver(root)
	mainPath := filepath.Join(root, "main.go")
	ns := Build(mainPath, r.Result(mainPath), r, GoPopulator(r))
	if entries := ns.Lookup("Rel"); len(entries) > 0 {
		t.Errorf("stdlib filepath.Rel should not pollute namespace; got %+v", entries)
	}
}
