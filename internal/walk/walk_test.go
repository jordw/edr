package walk

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// mkfile writes body under root/rel, creating intermediate dirs.
func mkfile(t *testing.T, root, rel string, body []byte) {
	t.Helper()
	abs := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

// collect walks root and returns repo-relative paths, sorted.
func collect(t *testing.T, root string) []string {
	t.Helper()
	var rels []string
	if err := RepoFiles(root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("RepoFiles: %v", err)
	}
	sort.Strings(rels)
	return rels
}

func TestRepoFiles_GitIgnoreAndAlwaysIgnored(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, ".gitignore", []byte("ignored.txt\ntmp/\n"))
	mkfile(t, root, "kept.txt", []byte("hello"))
	mkfile(t, root, "ignored.txt", []byte("nope"))
	mkfile(t, root, "tmp/inside.txt", []byte("also nope"))
	// Always-ignored directories.
	mkfile(t, root, ".git/config", []byte("x"))
	mkfile(t, root, ".edr/session.json", []byte("x"))
	mkfile(t, root, ".claude/note.md", []byte("x"))
	// Nested subtree.
	mkfile(t, root, "pkg/a.txt", []byte("a"))

	got := collect(t, root)
	want := []string{".gitignore", "kept.txt", "pkg/a.txt"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRepoFiles_SkipsNestedGitWorktrees(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, "src/a.go", []byte("package a"))
	mkfile(t, root, "worktrees/agent/.git", []byte("gitdir: ../../.git/worktrees/agent\n"))
	mkfile(t, root, "worktrees/agent/src/b.go", []byte("package b"))
	mkfile(t, root, "vendor/repo/.git/config", []byte("x"))
	mkfile(t, root, "vendor/repo/src/c.go", []byte("package c"))

	got := collect(t, root)
	want := []string{"src/a.go"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRepoFiles_AllowsRootGitFile(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, ".git", []byte("gitdir: ../.git/worktrees/root\n"))
	mkfile(t, root, "src/a.go", []byte("package a"))

	got := collect(t, root)
	want := []string{"src/a.go"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRepoFiles_MaxSizeExcluded(t *testing.T) {
	root := t.TempDir()
	small := make([]byte, 1024)
	big := make([]byte, (1<<20)+1) // > 1 MiB
	mkfile(t, root, "small.bin", small)
	mkfile(t, root, "big.bin", big)

	got := collect(t, root)
	if len(got) != 1 || got[0] != "small.bin" {
		t.Fatalf("expected only small.bin, got %v", got)
	}
}

func TestRepoFiles_DefaultIgnoreFallback(t *testing.T) {
	// No .gitignore — DefaultIgnore list kicks in.
	root := t.TempDir()
	mkfile(t, root, "src/a.go", []byte("package a"))
	mkfile(t, root, "node_modules/pkg/index.js", []byte("x"))
	mkfile(t, root, "vendor/x.go", []byte("x"))
	mkfile(t, root, "target/out.bin", []byte("x"))

	got := collect(t, root)
	want := []string{"src/a.go"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRepoFiles_SkipsDirsViaGitignore(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, ".gitignore", []byte("build/\n"))
	mkfile(t, root, "build/artifact.o", []byte("x"))
	mkfile(t, root, "src/a.go", []byte("package a"))

	got := collect(t, root)
	want := []string{".gitignore", "src/a.go"}
	if !equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestDirFiles_ScopedWalk(t *testing.T) {
	root := t.TempDir()
	mkfile(t, root, ".gitignore", []byte("build/\n"))
	mkfile(t, root, "pkg/a.txt", []byte("a"))
	mkfile(t, root, "pkg/b.txt", []byte("b"))
	mkfile(t, root, "other/c.txt", []byte("c"))
	mkfile(t, root, "pkg/build/out.o", []byte("x"))

	var rels []string
	if err := DirFiles(root, filepath.Join(root, "pkg"), func(path string) error {
		rel, _ := filepath.Rel(root, path)
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatalf("DirFiles: %v", err)
	}
	sort.Strings(rels)

	want := []string{"pkg/a.txt", "pkg/b.txt"}
	if !equal(rels, want) {
		t.Fatalf("got %v, want %v", rels, want)
	}
}

func TestLoadGitIgnore_MissingReturnsNil(t *testing.T) {
	m := LoadGitIgnore(t.TempDir())
	if m != nil {
		t.Fatalf("expected nil for missing .gitignore, got %+v", m)
	}
	// IsIgnored on nil is a no-op.
	if m.IsIgnored("anything", false) {
		t.Fatalf("nil matcher should ignore nothing")
	}
}

func TestGitIgnoreMatcher_Basics(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"),
		[]byte(strings.Join([]string{
			"*.log",
			"build/",
			"!build/keep.txt",
			"/top.only",
		}, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	m := LoadGitIgnore(root)
	if m == nil {
		t.Fatal("matcher nil")
	}
	cases := []struct {
		path  string
		isDir bool
		want  bool
	}{
		{"a.log", false, true},
		{"sub/b.log", false, true},
		{"build", true, true},
		{"build/keep.txt", false, false},
		{"top.only", false, true},
		{"nested/top.only", false, false}, // anchored: only root
		{"readme.md", false, false},
	}
	for _, tc := range cases {
		if got := m.IsIgnored(tc.path, tc.isDir); got != tc.want {
			t.Errorf("IsIgnored(%q, isDir=%v) = %v, want %v", tc.path, tc.isDir, got, tc.want)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
