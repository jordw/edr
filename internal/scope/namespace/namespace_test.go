package namespace

import (
	"testing"

	"github.com/jordw/edr/internal/scope"
)

// fakeResolver implements Resolver for tests. It maps import specs to
// files and files to scope.Result deterministically.
type fakeResolver struct {
	imports map[string][]string         // importingFile → candidate files
	results map[string]*scope.Result    // file → parsed result
}

func (f *fakeResolver) Result(file string) *scope.Result {
	return f.results[file]
}

func (f *fakeResolver) FilesForImport(spec, importingFile string) []string {
	return f.imports[importingFile+"|"+spec]
}

// decl is a tiny helper for building scope.Decls in tests.
func decl(id scope.DeclID, name string, kind scope.DeclKind, file string) scope.Decl {
	return scope.Decl{ID: id, Name: name, Kind: kind, File: file}
}

func TestBuild_LocalDeclsOnly(t *testing.T) {
	// With nil populator, only SourceLocal entries appear.
	r := &scope.Result{
		File: "a.go",
		Decls: []scope.Decl{
			decl(100, "Foo", scope.KindFunction, "a.go"),
			decl(200, "Bar", scope.KindFunction, "a.go"),
			decl(300, "fmt", scope.KindImport, "a.go"), // must be skipped
		},
	}
	ns := Build("a.go", r, nil, nil)
	if len(ns.Entries) != 2 {
		t.Fatalf("want 2 local entries, got %d: %v", len(ns.Entries), ns.Entries)
	}
	if !ns.Matches("Foo", 100) {
		t.Errorf("Foo should match DeclID 100")
	}
	if !ns.Matches("Bar", 200) {
		t.Errorf("Bar should match DeclID 200")
	}
	if ns.Matches("Foo", 999) {
		t.Errorf("Foo should NOT match DeclID 999")
	}
	if _, ok := ns.Entries["fmt"]; ok {
		t.Errorf("KindImport decls must not appear as SourceLocal; got fmt entry")
	}
	for _, e := range ns.Entries["Foo"] {
		if e.Source != SourceLocal {
			t.Errorf("Foo entry source = %d, want SourceLocal (%d)", e.Source, SourceLocal)
		}
	}
}

func TestBuild_PopulatorAddsImported(t *testing.T) {
	// A fake populator that adds a SourceImported entry for every
	// KindImport decl, pretending each one brings in a `Baz` decl
	// with a fixed DeclID. Exercises the contract without needing a
	// real language implementation.
	r := &scope.Result{
		File: "caller.go",
		Decls: []scope.Decl{
			decl(10, "libImport", scope.KindImport, "caller.go"),
			decl(20, "localFn", scope.KindFunction, "caller.go"),
		},
	}
	fakePop := func(ns *Namespace, r *scope.Result, _ Resolver) {
		for _, d := range r.Decls {
			if d.Kind != scope.KindImport {
				continue
			}
			ns.Entries["Baz"] = append(ns.Entries["Baz"], Entry{
				DeclID: 500, Source: SourceImported, File: "lib.go",
			})
		}
	}
	ns := Build("caller.go", r, &fakeResolver{}, fakePop)
	if !ns.Matches("localFn", 20) {
		t.Errorf("local decl missing after populator ran")
	}
	if !ns.Matches("Baz", 500) {
		t.Errorf("imported decl Baz should match DeclID 500")
	}
	bazEntries := ns.Lookup("Baz")
	if len(bazEntries) != 1 || bazEntries[0].Source != SourceImported {
		t.Errorf("Baz lookup = %+v, want one SourceImported entry", bazEntries)
	}
}

func TestBuild_AmbiguousMultipleEntries(t *testing.T) {
	// Two imports bringing in the same name — classic ambiguity case.
	// Lookup must return both so the consumer can decide policy.
	r := &scope.Result{File: "x.go"}
	fakePop := func(ns *Namespace, _ *scope.Result, _ Resolver) {
		ns.Entries["Foo"] = []Entry{
			{DeclID: 1, Source: SourceImported, File: "pkg1.go"},
			{DeclID: 2, Source: SourceImported, File: "pkg2.go"},
		}
	}
	ns := Build("x.go", r, nil, fakePop)
	entries := ns.Lookup("Foo")
	if len(entries) != 2 {
		t.Fatalf("want 2 ambiguous Foo entries, got %d", len(entries))
	}
	if !ns.Matches("Foo", 1) || !ns.Matches("Foo", 2) {
		t.Errorf("Matches should return true for either candidate DeclID")
	}
}

func TestNamespace_NilSafety(t *testing.T) {
	var ns *Namespace
	if ns.Matches("x", 1) {
		t.Errorf("Matches on nil Namespace should be false")
	}
	if ns.Lookup("x") != nil {
		t.Errorf("Lookup on nil Namespace should be nil")
	}
}

func TestBuild_NilResult(t *testing.T) {
	// Robust against missing scope results (unparseable files).
	ns := Build("missing.xyz", nil, nil, nil)
	if ns == nil || ns.Entries == nil {
		t.Fatalf("Build must return non-nil Namespace even with nil result")
	}
	if len(ns.Entries) != 0 {
		t.Errorf("want empty entries, got %v", ns.Entries)
	}
}
