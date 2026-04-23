package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCResolver_CanonicalMergeAcrossHeaderSource(t *testing.T) {
	dir := t.TempDir()
	write := func(p, content string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("foo.h", "int compute(int x);\n")
	write("foo.c", "int compute(int x) { return x * 2; }\n")

	r := NewCResolver(dir)
	hRes := r.Result(filepath.Join(dir, "foo.h"))
	cRes := r.Result(filepath.Join(dir, "foo.c"))
	if hRes == nil || cRes == nil {
		t.Fatal("parse failed")
	}
	// Both should have a `compute` decl with the same DeclID.
	var hID, cID uint64
	for _, d := range hRes.Decls {
		if d.Name == "compute" && d.Scope == 1 {
			hID = uint64(d.ID)
		}
	}
	for _, d := range cRes.Decls {
		if d.Name == "compute" && d.Scope == 1 {
			cID = uint64(d.ID)
		}
	}
	if hID == 0 || cID == 0 {
		t.Fatalf("compute decls missing: h=%d c=%d", hID, cID)
	}
	if hID != cID {
		t.Errorf("canonical merge failed: h=%d c=%d (should match)", hID, cID)
	}
}

func TestCResolver_StaticDoesNotMerge(t *testing.T) {
	dir := t.TempDir()
	write := func(p, content string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.c", "static int helper(void) { return 1; }\n")
	write("b.c", "static int helper(void) { return 2; }\n")

	r := NewCResolver(dir)
	aRes := r.Result(filepath.Join(dir, "a.c"))
	bRes := r.Result(filepath.Join(dir, "b.c"))
	var aID, bID uint64
	for _, d := range aRes.Decls {
		if d.Name == "helper" && d.Scope == 1 {
			aID = uint64(d.ID)
		}
	}
	for _, d := range bRes.Decls {
		if d.Name == "helper" && d.Scope == 1 {
			bID = uint64(d.ID)
		}
	}
	if aID == 0 || bID == 0 {
		t.Fatalf("helper decls missing: a=%d b=%d", aID, bID)
	}
	if aID == bID {
		t.Errorf("static decls must NOT merge across files: a=%d b=%d", aID, bID)
	}
}

func TestCResolver_FilesForImportQuoted(t *testing.T) {
	dir := t.TempDir()
	write := func(p string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("src/a.c")
	write("src/a.h")
	write("include/lib.h")

	r := NewCResolver(dir)
	aC := filepath.Join(dir, "src", "a.c")

	// Relative to including file's dir.
	got := r.FilesForImport("a.h", aC)
	if len(got) == 0 {
		t.Fatal("a.h not resolved relative to src/")
	}
	if filepath.Base(got[0]) != "a.h" {
		t.Errorf("unexpected resolution: %v", got)
	}

	// Relative to repo root.
	got = r.FilesForImport("include/lib.h", aC)
	if len(got) == 0 || filepath.Base(got[0]) != "lib.h" {
		t.Errorf("include/lib.h not resolved: %v", got)
	}
}

func TestCPopulator_HeaderDeclSurfacedToCaller(t *testing.T) {
	dir := t.TempDir()
	write := func(p, content string) {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("foo.h", "int compute(int x);\n")
	write("foo.c", "int compute(int x) { return x * 2; }\n")
	write("main.c", `#include "foo.h"

int run(void) { return compute(5); }
`)

	r := NewCResolver(dir)
	pop := CPopulator(r)
	main := filepath.Join(dir, "main.c")
	mainRes := r.Result(main)
	if mainRes == nil {
		t.Fatal("main.c parse failed")
	}
	ns := Build(main, mainRes, r, pop)

	cRes := r.Result(filepath.Join(dir, "foo.c"))
	var cID uint64
	for _, d := range cRes.Decls {
		if d.Name == "compute" && d.Scope == 1 {
			cID = uint64(d.ID)
		}
	}
	entries := ns.Lookup("compute")
	matched := false
	for _, e := range entries {
		if uint64(e.DeclID) == cID {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("main.c namespace should surface foo.c's compute via #include; got %v, expected %d", entries, cID)
	}
}
