package namespace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRustNamespace_E2E(t *testing.T) {
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
	write("src/runtime.rs", `
pub struct Handle;
pub fn spawn() {}
`)
	write("src/task.rs", `
use crate::runtime::Handle;
use crate::runtime::spawn;

pub fn run() {
    let h = Handle;
    spawn();
}
`)

	r := NewRustResolver(dir)
	pop := RustPopulator(r)
	taskFile := filepath.Join(dir, "src", "task.rs")
	taskRes := r.Result(taskFile)
	if taskRes == nil {
		t.Fatal("task.rs did not parse")
	}
	ns := Build(taskFile, taskRes, r, pop)

	runtimeFile := filepath.Join(dir, "src", "runtime.rs")
	rtRes := r.Result(runtimeFile)
	var handleID, spawnID uint64
	for _, d := range rtRes.Decls {
		if d.Name == "Handle" && d.Scope == 1 {
			handleID = uint64(d.ID)
		}
		if d.Name == "spawn" && d.Scope == 1 {
			spawnID = uint64(d.ID)
		}
	}
	if handleID == 0 || spawnID == 0 {
		t.Fatalf("runtime decls not found: handle=%d spawn=%d", handleID, spawnID)
	}

	entries := ns.Lookup("Handle")
	matched := false
	for _, e := range entries {
		if uint64(e.DeclID) == handleID {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("Handle namespace entry does not match runtime.rs DeclID; got %v, expected %d", entries, handleID)
	}
	entries = ns.Lookup("spawn")
	matched = false
	for _, e := range entries {
		if uint64(e.DeclID) == spawnID {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("spawn namespace entry does not match runtime.rs DeclID; got %v", entries)
	}
}
