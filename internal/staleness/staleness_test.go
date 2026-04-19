package staleness

import (
	"bytes"
	"encoding/gob"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"
)

// walker mirrors the shape the rest of edr passes around — walk every
// file under root, invoke fn with the absolute path.
func walker() WalkFn {
	return func(root string, fn func(string) error) error {
		return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			return fn(p)
		})
	}
}

// write creates a file at root/rel with the given content.
func write(t *testing.T, root, rel, content string) string {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

// setMtime forces mtime on p to an explicit value. We use a far-future
// time so tests never hit same-tick flakes.
func setMtime(t *testing.T, p string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
}

func TestCapture_ProducesEntriesPerFile(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n")
	write(t, root, "sub/b.go", "package sub\n")

	snap := Capture(root, walker())
	if len(snap.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(snap.Entries))
	}
	if _, ok := snap.Entries["a.go"]; !ok {
		t.Errorf("missing a.go; entries = %v", snap.Entries)
	}
	if _, ok := snap.Entries[filepath.Join("sub", "b.go")]; !ok {
		t.Errorf("missing sub/b.go; entries = %v", snap.Entries)
	}
	for _, e := range snap.Entries {
		if e.Mtime == 0 {
			t.Errorf("zero mtime in entry %+v", e)
		}
	}
}

func TestCheck_MtimeChange(t *testing.T) {
	root := t.TempDir()
	p := write(t, root, "a.go", "package a\n")
	snap := Capture(root, walker())

	setMtime(t, p, time.Now().Add(5*time.Second))

	d := Check(root, snap, walker())
	if len(d.Modified) != 1 || d.Modified[0] != "a.go" {
		t.Fatalf("Modified = %v, want [a.go]", d.Modified)
	}
	if len(d.Deleted)+len(d.Added) != 0 {
		t.Errorf("unexpected Deleted/Added: %+v", d)
	}
}

// Silent-replace: the filesystem returns the same mtime but a larger
// body. Pre-refactor scope.ResultFor would happily return stale data.
func TestCheck_SizeChangeWithSameMtime(t *testing.T) {
	root := t.TempDir()
	p := write(t, root, "a.go", "package a\n")
	snap := Capture(root, walker())
	orig := snap.Entries["a.go"].Mtime

	// Rewrite with different content, then force the mtime back to the
	// original value. Size changes; mtime does not.
	if err := os.WriteFile(p, []byte("package a\n\nfunc Foo() {}\n"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	setMtime(t, p, time.Unix(0, orig))

	d := Check(root, snap, walker())
	if len(d.Modified) != 1 || d.Modified[0] != "a.go" {
		t.Fatalf("size-change: Modified = %v, want [a.go]", d.Modified)
	}
}

func TestCheck_Deleted(t *testing.T) {
	root := t.TempDir()
	p := write(t, root, "a.go", "package a\n")
	snap := Capture(root, walker())

	if err := os.Remove(p); err != nil {
		t.Fatalf("remove: %v", err)
	}

	d := Check(root, snap, walker())
	if len(d.Deleted) != 1 || d.Deleted[0] != "a.go" {
		t.Fatalf("Deleted = %v, want [a.go]", d.Deleted)
	}
}

func TestCheck_Added(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n")
	snap := Capture(root, walker())

	write(t, root, "b.go", "package b\n")

	d := Check(root, snap, walker())
	if len(d.Added) != 1 || d.Added[0] != "b.go" {
		t.Fatalf("Added = %v, want [b.go]", d.Added)
	}
}

// Permission changes don't touch mtime or size, so the diff must
// stay clean. Catches a pitfall where someone adds a mode check to
// IsFresh and breaks chmod-heavy workflows.
func TestCheck_PermissionOnlyChange_IsClean(t *testing.T) {
	root := t.TempDir()
	p := write(t, root, "a.go", "package a\n")
	snap := Capture(root, walker())

	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	d := Check(root, snap, walker())
	if !d.Empty() {
		t.Errorf("perm change produced diff: %+v", d)
	}
}

// Symlink retargeting: os.Lstat sees the symlink itself — mtime of the
// link is updated on retarget. We rely on that: a retarget counts as
// Modified.
func TestCheck_SymlinkRetargetIsModified(t *testing.T) {
	root := t.TempDir()
	write(t, root, "target1.go", "package t1\n")
	write(t, root, "target2.go", "package t2\n")
	linkPath := filepath.Join(root, "link.go")
	if err := os.Symlink("target1.go", linkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}

	snap := Capture(root, walker())
	if _, ok := snap.Entries["link.go"]; !ok {
		t.Fatalf("capture missed symlink")
	}

	// Remove and re-create pointing elsewhere; the new link's mtime
	// will be later than the captured one (even on 1-second mtime
	// filesystems, since we sleep across the boundary).
	time.Sleep(1100 * time.Millisecond)
	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if err := os.Symlink("target2.go", linkPath); err != nil {
		t.Fatalf("re-symlink: %v", err)
	}

	d := Check(root, snap, walker())
	found := false
	for _, m := range d.Modified {
		if m == "link.go" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("symlink retarget not detected; diff = %+v", d)
	}
}

func TestSnapshot_GobRoundTrip(t *testing.T) {
	root := t.TempDir()
	write(t, root, "a.go", "package a\n")
	write(t, root, "b.go", "package b\n")
	snap := Capture(root, walker())

	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(snap); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var decoded Snapshot
	if err := gob.NewDecoder(&buf).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Check against the decoded snapshot — should see nothing changed.
	d := Check(root, &decoded, walker())
	if !d.Empty() {
		t.Errorf("round-trip snapshot produced diff: %+v", d)
	}

	// Flip one file, re-check — only the flipped one should be Modified.
	p := filepath.Join(root, "a.go")
	setMtime(t, p, time.Now().Add(5*time.Second))
	d = Check(root, &decoded, walker())
	if len(d.Modified) != 1 || d.Modified[0] != "a.go" {
		t.Errorf("post-flip Modified = %v, want [a.go]", d.Modified)
	}
}

// Rapid writes within the same mtime tick are a documented limitation:
// mtime is 1-second granular on some filesystems, and without a content
// hash we can't distinguish two same-tick writes of different bodies
// unless Size happens to differ. If the user needs sub-second safety
// they should rebuild explicitly. This is recorded as a Skip so a
// future tightening of the contract flags the deliberate miss.
func TestCheck_RapidSameTickWrite_KnownLimitation(t *testing.T) {
	t.Skip("documented: 1-second mtime granularity; use Size as a coarse backstop")
}

func TestTracker_MarkDirtyClear(t *testing.T) {
	edrDir := t.TempDir()
	tr := OpenTracker(edrDir, "test")
	if tr.IsDirty() {
		t.Errorf("fresh tracker reports dirty")
	}

	tr.Mark("a.go", "b.go")
	if !tr.IsDirty() {
		t.Errorf("after Mark, IsDirty = false")
	}
	got := tr.Dirty()
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
		t.Errorf("Dirty = %v, want [a.go b.go]", got)
	}

	// Duplicate mark is deduped on read.
	tr.Mark("a.go")
	got = tr.Dirty()
	if len(got) != 2 {
		t.Errorf("after duplicate Mark, Dirty = %v (len=%d), want 2", got, len(got))
	}

	tr.Clear()
	if tr.IsDirty() {
		t.Errorf("after Clear, IsDirty = true")
	}
	if got := tr.Dirty(); len(got) != 0 {
		t.Errorf("after Clear, Dirty = %v, want empty", got)
	}
}

func TestTracker_IgnoresLegacyBoolAndEmptyLines(t *testing.T) {
	edrDir := t.TempDir()
	tr := OpenTracker(edrDir, "test")
	// Simulate a legacy dirty file.
	if err := os.WriteFile(tr.Path(), []byte("1\n\nfoo.go\n\n"), 0o600); err != nil {
		t.Fatalf("write legacy: %v", err)
	}
	got := tr.Dirty()
	if len(got) != 1 || got[0] != "foo.go" {
		t.Errorf("Dirty = %v, want [foo.go]", got)
	}
}

// Load-bearing: edr is run in parallel from multiple agent processes
// against the same repo. Concurrent O_APPEND writes under PIPE_BUF
// must not lose markers. This test exercises the in-process goroutine
// case; it also guards against future "optimizations" that replace
// O_APPEND with read-merge-rewrite (which is what the old idx.MarkDirty
// did, and which silently dropped markers under contention).
func TestTracker_ConcurrentMark_Union(t *testing.T) {
	edrDir := t.TempDir()
	tr := OpenTracker(edrDir, "test")

	const goroutines = 8
	const perG = 25
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			runtime.Gosched()
			for i := 0; i < perG; i++ {
				tr.Mark(markName(g, i))
			}
		}(g)
	}
	wg.Wait()

	got := tr.Dirty()
	// Build expected.
	expected := make([]string, 0, goroutines*perG)
	for g := 0; g < goroutines; g++ {
		for i := 0; i < perG; i++ {
			expected = append(expected, markName(g, i))
		}
	}
	sort.Strings(expected)

	if len(got) != len(expected) {
		t.Fatalf("concurrent Mark: got %d unique paths, want %d (union must be preserved)", len(got), len(expected))
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

func markName(g, i int) string {
	return filepath.Join("g", itoa(g), itoa(i)+".go")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [16]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
