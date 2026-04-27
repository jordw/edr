package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// rustFixture writes files under a stub Cargo.toml so the Rust
// resolver picks the dir as a crate root.
func rustFixture(t *testing.T, files map[string]string) (index.SymbolStore, string) {
	t.Helper()
	tmp := t.TempDir()
	if _, ok := files["Cargo.toml"]; !ok {
		files["Cargo.toml"] = `[package]
name = "test"
version = "0.0.1"
edition = "2021"
`
	}
	for rel, body := range files {
		full := filepath.Join(tmp, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	db := index.NewOnDemand(tmp)
	output.SetRoot(db.Root())
	t.Cleanup(func() { db.Close() })
	return db, tmp
}

// TestRename_Rust_HierarchySameFileImpl: same-file `impl Trait for
// Foo` — renaming Trait.method must propagate to the impl's method.
func TestRename_Rust_HierarchySameFileImpl(t *testing.T) {
	db, dir := rustFixture(t, map[string]string{
		"src/lib.rs": `pub trait Greeter {
    fn greet(&self);
}

pub struct Hello;

impl Greeter for Hello {
    fn greet(&self) {
        println!("hi");
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"src/lib.rs:greet"},
		map[string]any{"new_name": "say_hello", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "src/lib.rs"))
	src := string(data)
	if got := strings.Count(src, "fn say_hello("); got != 2 {
		t.Errorf("expected exactly 2 (Trait + impl) renamed, got %d:\n%s", got, src)
	}
	if strings.Contains(src, "fn greet(") {
		t.Errorf("file still contains original greet; got:\n%s", src)
	}
}

// TestRename_Rust_HierarchyMultipleImpls: a trait with multiple
// implementing structs. Renaming the trait method (target = the
// trait decl, isolated in its own file so the resolver picks it
// unambiguously) propagates down to every impl.
func TestRename_Rust_HierarchyMultipleImpls(t *testing.T) {
	db, dir := rustFixture(t, map[string]string{
		"src/speak.rs": `pub trait Speak {
    fn say(&self);
}
`,
		"src/main.rs": `use crate::speak::Speak;

pub struct Dog;
pub struct Cat;

impl Speak for Dog {
    fn say(&self) {
        println!("woof");
    }
}

impl Speak for Cat {
    fn say(&self) {
        println!("meow");
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"src/speak.rs:say"},
		map[string]any{"new_name": "vocalize", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	speak, _ := os.ReadFile(filepath.Join(dir, "src/speak.rs"))
	main, _ := os.ReadFile(filepath.Join(dir, "src/main.rs"))
	if !strings.Contains(string(speak), "fn vocalize(") {
		t.Errorf("speak.rs trait method not renamed: %s", speak)
	}
	if got := strings.Count(string(main), "fn vocalize("); got != 2 {
		t.Errorf("main.rs: expected exactly 2 (Dog + Cat) impls renamed, got %d:\n%s", got, main)
	}
}

// TestRename_Rust_HierarchyUpWalk: rename a method on the impl side
// — propagates up to the trait's method.
func TestRename_Rust_HierarchyUpWalk(t *testing.T) {
	db, dir := rustFixture(t, map[string]string{
		"src/main.rs": `pub trait Greeter {
    fn greet(&self);
}

pub struct Hello;

impl Greeter for Hello {
    fn greet(&self) {
        println!("hi");
    }
}
`,
	})
	_, err := dispatch.Dispatch(context.Background(), db, "rename",
		[]string{"src/main.rs:greet"},
		map[string]any{"new_name": "say_hello", "cross_file": true})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "src/main.rs"))
	src := string(data)
	if got := strings.Count(src, "fn say_hello("); got != 2 {
		t.Errorf("expected exactly 2 methods renamed, got %d:\n%s", got, src)
	}
}
