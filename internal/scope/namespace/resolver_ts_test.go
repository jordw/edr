package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTSResolver_CanonicalPath(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		file string
		want string
	}{
		{"src/foo.ts", "src/foo"},
		{"src/foo/index.tsx", "src/foo/index"},
		{"lib/util.d.ts", "lib/util"},
		{"app.js", "app"},
	}
	for _, c := range cases {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, c.file)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, c.file), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r := NewTSResolver(dir)
	for _, c := range cases {
		got := r.CanonicalPath(filepath.Join(dir, c.file))
		if got != c.want {
			t.Errorf("CanonicalPath(%s) = %q, want %q", c.file, got, c.want)
		}
	}
}

func TestTSResolver_FilesForImport(t *testing.T) {
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
	write("src/a.ts")
	write("src/lib/util.ts")
	write("src/components/Foo/index.tsx")

	r := NewTSResolver(dir)
	from := filepath.Join(dir, "src", "a.ts")

	cases := []struct {
		spec string
		want string
	}{
		{"./lib/util", filepath.Join(dir, "src", "lib", "util.ts")},
		{"./components/Foo", filepath.Join(dir, "src", "components", "Foo", "index.tsx")},
	}
	for _, c := range cases {
		got := r.FilesForImport(c.spec, from)
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("FilesForImport(%q) = %v, want [%s]", c.spec, got, c.want)
		}
	}
	// Bare specifier (node_modules) → nil.
	if got := r.FilesForImport("react", from); got != nil {
		t.Errorf("bare specifier should return nil, got %v", got)
	}
}

func TestTSPopulator_CrossFile(t *testing.T) {
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
	write("src/lib.ts", `export function compute(x: number): number { return x * 2; }
`)
	write("src/app.ts", `import { compute } from './lib';

export function run(): number { return compute(5); }
`)

	r := NewTSResolver(dir)
	pop := TSPopulator(r)
	app := filepath.Join(dir, "src", "app.ts")
	appRes := r.Result(app)
	if appRes == nil {
		t.Fatal("app.ts parse failed")
	}
	ns := Build(app, appRes, r, pop)

	libRes := r.Result(filepath.Join(dir, "src", "lib.ts"))
	var libID uint64
	for _, d := range libRes.Decls {
		if d.Name == "compute" && d.Scope == 1 {
			libID = uint64(d.ID)
		}
	}
	if libID == 0 {
		t.Fatal("compute decl in lib.ts not found")
	}
	entries := ns.Lookup("compute")
	matched := false
	for _, e := range entries {
		if uint64(e.DeclID) == libID {
			matched = true
		}
	}
	if !matched {
		t.Errorf("compute namespace entry should match lib.ts DeclID; got %v, expected %d", entries, libID)
	}
}
