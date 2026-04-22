package namespace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRustResolver_CanonicalPath(t *testing.T) {
	// Build a minimal crate layout in a temp dir.
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
	write("Cargo.toml", `[package]
name = "demo"
version = "0.1.0"
`)
	write("src/lib.rs", "")
	write("src/runtime/mod.rs", "")
	write("src/runtime/handle.rs", "")
	write("src/task.rs", "")

	r := NewRustResolver(dir)
	cases := []struct {
		file string
		want string
	}{
		{"src/lib.rs", "demo"},
		{"src/task.rs", "demo::task"},
		{"src/runtime/mod.rs", "demo::runtime"},
		{"src/runtime/handle.rs", "demo::runtime::handle"},
	}
	for _, c := range cases {
		got := r.CanonicalPath(filepath.Join(dir, c.file))
		if got != c.want {
			t.Errorf("CanonicalPath(%s) = %q, want %q", c.file, got, c.want)
		}
	}
}

func TestRustResolver_FilesForImport(t *testing.T) {
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
	write("Cargo.toml", "[package]\nname = \"demo\"\n")
	write("src/lib.rs", "")
	write("src/runtime.rs", "")
	write("src/runtime/handle.rs", "")
	write("src/task.rs", "")

	r := NewRustResolver(dir)
	importing := filepath.Join(dir, "src", "task.rs")

	// crate::runtime::Handle → runtime.rs (or runtime/mod.rs if it existed)
	got := r.FilesForImport("crate::runtime::Handle", importing)
	if len(got) != 1 || !strings.HasSuffix(got[0], "runtime.rs") {
		t.Errorf("crate::runtime::Handle resolved to %v", got)
	}

	// crate::runtime::handle::Handle → runtime/handle.rs
	got = r.FilesForImport("crate::runtime::handle::Handle", importing)
	if len(got) != 1 || !strings.HasSuffix(got[0], filepath.Join("runtime", "handle.rs")) {
		t.Errorf("crate::runtime::handle::Handle resolved to %v", got)
	}

	// crate::task::Foo from lib.rs → task.rs
	got = r.FilesForImport("crate::task::Foo", filepath.Join(dir, "src", "lib.rs"))
	if len(got) != 1 || !strings.HasSuffix(got[0], "task.rs") {
		t.Errorf("crate::task::Foo resolved to %v", got)
	}

	// External crate → nil.
	got = r.FilesForImport("std::collections::HashMap", importing)
	if got != nil {
		t.Errorf("external crate should resolve to nil, got %v", got)
	}
}

func TestRustResolver_SamePackageFiles(t *testing.T) {
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
	write("Cargo.toml", "[package]\nname = \"demo\"\n")
	write("src/lib.rs", "")
	write("src/a.rs", "")
	write("src/b/mod.rs", "")
	write("src/b/c.rs", "")
	write("target/debug/build.rs", "") // should be ignored — not under src

	r := NewRustResolver(dir)
	got := r.SamePackageFiles(filepath.Join(dir, "src", "a.rs"))
	// Expect all src/*.rs files except a.rs.
	want := map[string]bool{
		filepath.Join(dir, "src", "lib.rs"):     true,
		filepath.Join(dir, "src", "b", "mod.rs"): true,
		filepath.Join(dir, "src", "b", "c.rs"):   true,
	}
	if len(got) != len(want) {
		t.Errorf("got %d files, want %d: %v", len(got), len(want), got)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected file in same-crate set: %s", f)
		}
	}
}
