package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPythonResolver_CanonicalPath(t *testing.T) {
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
	write("foo/__init__.py")
	write("foo/bar/__init__.py")
	write("foo/bar/baz.py")
	write("standalone.py")
	write("noinit/mod.py") // no __init__.py above → canonical == "mod"

	r := NewPythonResolver(dir)
	cases := []struct {
		file string
		want string
	}{
		{"foo/bar/baz.py", "foo.bar.baz"},
		{"foo/bar/__init__.py", "foo.bar"},
		{"foo/__init__.py", "foo"},
		{"standalone.py", "standalone"},
		{"noinit/mod.py", "mod"},
	}
	for _, c := range cases {
		got := r.CanonicalPath(filepath.Join(dir, c.file))
		if got != c.want {
			t.Errorf("CanonicalPath(%s) = %q, want %q", c.file, got, c.want)
		}
	}
}

func TestPythonResolver_FilesForImport(t *testing.T) {
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
	write("pkg/__init__.py")
	write("pkg/mod.py")
	write("pkg/sub/__init__.py")
	write("pkg/sub/a.py")
	write("pkg/sub/b.py")

	r := NewPythonResolver(dir)

	// Absolute.
	got := r.FilesForImport("pkg.mod", filepath.Join(dir, "main.py"))
	if len(got) != 1 || !filepath.IsAbs(got[0]) || filepath.Base(got[0]) != "mod.py" {
		t.Errorf("pkg.mod absolute: got %v", got)
	}
	// Absolute subpackage via __init__.
	got = r.FilesForImport("pkg.sub", filepath.Join(dir, "main.py"))
	if len(got) != 1 || filepath.Base(got[0]) != "__init__.py" {
		t.Errorf("pkg.sub (dir package): got %v", got)
	}
	// Relative from pkg/sub/a.py → sibling b.
	got = r.FilesForImport(".b", filepath.Join(dir, "pkg", "sub", "a.py"))
	if len(got) != 1 || filepath.Base(got[0]) != "b.py" {
		t.Errorf("relative .b from sub/a.py: got %v", got)
	}
	// Parent-level: from pkg/sub/a.py `from ..mod import X`.
	got = r.FilesForImport("..mod", filepath.Join(dir, "pkg", "sub", "a.py"))
	if len(got) != 1 || filepath.Base(got[0]) != "mod.py" {
		t.Errorf("..mod from sub/a.py: got %v", got)
	}
}

func TestPythonPopulator_CrossFile(t *testing.T) {
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
	write("pkg/__init__.py", "")
	write("pkg/lib.py", `def compute(x):
    return x * 2
`)
	write("pkg/app.py", `from pkg.lib import compute

def run():
    return compute(5)
`)

	r := NewPythonResolver(dir)
	pop := PythonPopulator(r)
	app := filepath.Join(dir, "pkg", "app.py")
	appRes := r.Result(app)
	if appRes == nil {
		t.Fatal("app.py parse failed")
	}
	ns := Build(app, appRes, r, pop)

	libRes := r.Result(filepath.Join(dir, "pkg", "lib.py"))
	var libID uint64
	for _, d := range libRes.Decls {
		if d.Name == "compute" && d.Scope == 1 {
			libID = uint64(d.ID)
		}
	}
	if libID == 0 {
		t.Fatal("compute decl in lib.py not found")
	}
	entries := ns.Lookup("compute")
	matched := false
	for _, e := range entries {
		if uint64(e.DeclID) == libID {
			matched = true
		}
	}
	if !matched {
		t.Errorf("compute namespace entry should match lib.py DeclID; got %v, expected %d", entries, libID)
	}
}
