package idx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestStatChanges_PrunesAlwaysIgnoredEntries simulates an index built
// before the always-ignored policy was tightened — the file table
// contains `.claude/worktrees/...` paths even though the walker no
// longer visits them. StatChanges must report those paths as Deleted
// so PatchDirtyFiles can drop them on the next tick. Without this,
// `edr files` and `edr orient` keep surfacing stale worktree files.
func TestStatChanges_PrunesAlwaysIgnoredEntries(t *testing.T) {
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	if err := os.MkdirAll(edrDir, 0o755); err != nil {
		t.Fatalf("mkdir edrDir: %v", err)
	}
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write .git/index: %v", err)
	}

	// Files on disk: one normal, one under `.claude/worktrees/...`.
	// The worktree file EXISTS on disk so a plain Lstat would not flag
	// it as deleted — only the ignore-policy check does.
	files := map[string]string{
		"alive.go": "package a\n",
		".claude/worktrees/agent-foo/AGENTS.md": "stale\n",
	}
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}

	// Custom walker that DOES NOT honor the always-ignored policy —
	// simulates the pre-fix state where these paths got into the index.
	walkFn := func(rootArg string, fn func(string) error) error {
		return filepath.Walk(rootArg, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(rootArg, p)
			// Skip only .git and .edr — not .claude. This is what
			// produces the stale-entry condition the fix targets.
			for _, seg := range strings.Split(rel, string(filepath.Separator)) {
				if seg == ".git" || seg == ".edr" {
					return nil
				}
			}
			return fn(p)
		})
	}

	if err := BuildFullFromWalk(root, edrDir, walkFn, nil); err != nil {
		t.Fatalf("BuildFullFromWalk: %v", err)
	}

	// Confirm the stale entry actually made it into the index — the
	// rest of the test is meaningless otherwise.
	preDirty := IndexedPaths(edrDir)
	if _, ok := preDirty[".claude/worktrees/agent-foo/AGENTS.md"]; !ok {
		t.Fatalf("setup precondition failed: stale .claude path not indexed; got %v", keys(preDirty))
	}

	// Bump .git/index mtime so the Staleness fast path doesn't skip.
	// (StatChanges itself doesn't gate on mtime, but a real workflow
	// reaches it via IncrementalTick.)
	future := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(gitDir, "index"), future, future); err != nil {
		t.Fatalf("chtimes .git/index: %v", err)
	}

	c := StatChanges(root, edrDir)
	if c == nil {
		t.Fatalf("StatChanges returned nil")
	}

	// The .claude path must be flagged for deletion.
	wantDeleted := ".claude/worktrees/agent-foo/AGENTS.md"
	found := false
	for _, d := range c.Deleted {
		if d == wantDeleted {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Deleted should include %q (always-ignored stale entry); got %v", wantDeleted, c.Deleted)
	}

	// alive.go must NOT be flagged — it's a normal file that exists
	// and isn't ignore-policy-tagged.
	for _, d := range c.Deleted {
		if d == "alive.go" {
			t.Errorf("Deleted incorrectly includes %q; should only contain ignored-path entries", d)
		}
	}
}

// TestStatChanges_SkipsIgnoredDirsInNewScan covers the second half of
// the fix: when the indexed-directory mtime scan recurses into a new
// directory that turns out to be in the always-ignored set (e.g. a
// `.claude/` created after the index was built), walkNewDir's hidden-
// dir gate is no longer the only thing keeping its descendants out of
// `New`. The explicit policy check at the call site catches it before
// recursion.
func TestStatChanges_SkipsIgnoredDirsInNewScan(t *testing.T) {
	root := t.TempDir()
	edrDir := filepath.Join(root, ".edr")
	if err := os.MkdirAll(edrDir, 0o755); err != nil {
		t.Fatalf("mkdir edrDir: %v", err)
	}
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "index"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write .git/index: %v", err)
	}

	// Build initial index with one file at root.
	if err := os.WriteFile(filepath.Join(root, "alive.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatalf("write alive.go: %v", err)
	}
	walkFn := func(rootArg string, fn func(string) error) error {
		return filepath.Walk(rootArg, func(p string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(rootArg, p)
			for _, seg := range strings.Split(rel, string(filepath.Separator)) {
				if seg == ".git" || seg == ".edr" || seg == ".claude" {
					return nil
				}
			}
			return fn(p)
		})
	}
	if err := BuildFullFromWalk(root, edrDir, walkFn, nil); err != nil {
		t.Fatalf("BuildFullFromWalk: %v", err)
	}

	// After build: create a .claude/worktrees tree on disk. The root
	// dir's mtime advances, which is what triggers the new-file scan.
	if err := os.MkdirAll(filepath.Join(root, ".claude", "worktrees", "agent-foo"), 0o755); err != nil {
		t.Fatalf("mkdir .claude tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".claude", "worktrees", "agent-foo", "AGENTS.md"), []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}

	c := StatChanges(root, edrDir)
	if c == nil {
		t.Fatalf("StatChanges returned nil")
	}

	// No path under .claude/ may appear in c.New.
	for _, n := range c.New {
		if strings.Contains(n, ".claude") {
			t.Errorf("New must skip always-ignored paths; got %q in %v", n, c.New)
		}
	}
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
