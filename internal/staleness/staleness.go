// Package staleness consolidates freshness detection and dirty-file
// tracking for the idx and scope index stacks.
//
// Motivation: idx/, scope/store/, and session/ each re-implemented
// overlapping primitives: per-file metadata snapshots, directory walks
// to detect new files, mark-rebuild cycles for edits. Scattering the
// logic produced recurring bugs (phantom symbols after file deletion,
// silent-replace on size-only changes with the same mtime).
//
// This package exposes the canonical per-file `Entry` (path + mtime +
// size) and a `Diff` with Added / Modified / Deleted for callers to
// act on. A file-backed append-only `Tracker` replaces the
// read-merge-rewrite dirty lists that preceded it.
//
// Persistence: Snapshot is gob-serializable. idx and scope already
// embed per-file metadata in their own on-disk formats (FileEntry on
// idx, RecordMeta on scope), so neither needs a sidecar — they pass
// their own records into Check directly. A future session-delta
// consumer could add Save/Load helpers; none exist today.
package staleness

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
)

// Entry captures per-file metadata sufficient to detect modifications.
// The Size field is load-bearing: a silent-replace with the same mtime
// but a different size must register as Modified.
//
// Known limitation: mtime has 1-second granularity on some filesystems
// (notably HFS+). Rapid writes within the same tick may not trip the
// check; documented and accepted — a cheap content hash would double
// the staleness cost.
type Entry struct {
	Path  string
	Mtime int64 // os.FileInfo.ModTime().UnixNano()
	Size  int64
}

// Snapshot is a point-in-time view of a set of files.
// Taken is the unix-nano timestamp when Capture ran. Entries is keyed
// by repo-relative path. Snapshot is gob-serializable.
type Snapshot struct {
	Taken   int64
	Entries map[string]Entry
}

// Diff is the result of comparing a Snapshot against the current
// filesystem state.
//
//	Added    — present on disk, absent from the snapshot
//	Modified — mtime or size differs
//	Deleted  — present in the snapshot, absent from disk
//
// All paths are repo-relative. Empty returns true when all three
// slices are empty.
type Diff struct {
	Added    []string
	Modified []string
	Deleted  []string
}

// Empty reports whether no changes were detected.
func (d *Diff) Empty() bool {
	return len(d.Added) == 0 && len(d.Modified) == 0 && len(d.Deleted) == 0
}

// WalkFn is the repo-walker shape shared with idx and scope. Every
// caller in edr passes index.WalkRepoFiles; the package-level type
// exists so staleness itself doesn't import the walker implementation.
// The walker emits absolute paths.
type WalkFn func(root string, fn func(path string) error) error

// Capture walks the repo and returns a Snapshot keyed by repo-relative
// path. Files the walker skips (gitignored, binary) are simply absent
// from the result. On walk failure Capture still returns whatever it
// collected — a partial snapshot is more useful than none.
func Capture(root string, walk WalkFn) *Snapshot {
	snap := &Snapshot{
		Taken:   nowUnixNano(),
		Entries: make(map[string]Entry, 1024),
	}
	if walk == nil {
		return snap
	}
	var mu sync.Mutex
	_ = walk(root, func(abs string) error {
		info, err := os.Lstat(abs)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || rel == "" {
			return nil
		}
		mu.Lock()
		snap.Entries[rel] = Entry{
			Path:  rel,
			Mtime: info.ModTime().UnixNano(),
			Size:  info.Size(),
		}
		mu.Unlock()
		return nil
	})
	return snap
}

// IsFresh reports whether the file at root/e.Path matches Entry e.
// Mismatched size — even with the same mtime — is treated as stale.
// Missing files are stale.
func IsFresh(root string, e Entry) bool {
	abs := e.Path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, e.Path)
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return false
	}
	return info.ModTime().UnixNano() == e.Mtime && info.Size() == e.Size
}

// Check compares a Snapshot against the filesystem and returns the
// Diff. Stat calls are parallel; total cost on 93k-file repos is
// ~60–70 ms.
//
// If snap is nil Check treats the snapshot as empty: every file the
// walker sees is Added. If walk is nil Check only stats the files
// already in snap (so it can report Modified + Deleted but never
// Added).
//
// Callers that want the git-index mtime fast-path — skip everything if
// .git/index hasn't moved — should call that externally; staleness
// deliberately doesn't know about git.
func Check(root string, snap *Snapshot, walk WalkFn) *Diff {
	diff := &Diff{}
	entries := map[string]Entry{}
	if snap != nil {
		entries = snap.Entries
	}

	// Phase 1: parallel-stat every known file for Modified/Deleted.
	type result struct {
		rel     string
		deleted bool
		changed bool
	}
	refs := make([]Entry, 0, len(entries))
	for _, e := range entries {
		refs = append(refs, e)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Path < refs[j].Path })

	results := make([]result, len(refs))
	if len(refs) > 0 {
		workers := runtime.GOMAXPROCS(0)
		if workers < 4 {
			workers = 4
		}
		ch := make(chan int, 256)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for ri := range ch {
					ref := refs[ri]
					abs := filepath.Join(root, ref.Path)
					info, err := os.Lstat(abs)
					if err != nil {
						results[ri] = result{rel: ref.Path, deleted: true}
					} else if info.ModTime().UnixNano() != ref.Mtime || info.Size() != ref.Size {
						results[ri] = result{rel: ref.Path, changed: true}
					}
				}
			}()
		}
		for i := range refs {
			ch <- i
		}
		close(ch)
		wg.Wait()
	}
	for _, r := range results {
		switch {
		case r.deleted:
			diff.Deleted = append(diff.Deleted, r.rel)
		case r.changed:
			diff.Modified = append(diff.Modified, r.rel)
		}
	}

	// Phase 2: walk for Added. Callers that don't supply a walker pay
	// nothing but also get no new-file detection.
	if walk != nil {
		known := make(map[string]struct{}, len(entries))
		for p := range entries {
			known[p] = struct{}{}
		}
		var mu sync.Mutex
		_ = walk(root, func(abs string) error {
			rel, err := filepath.Rel(root, abs)
			if err != nil || rel == "" {
				return nil
			}
			mu.Lock()
			if _, ok := known[rel]; !ok {
				diff.Added = append(diff.Added, rel)
			}
			mu.Unlock()
			return nil
		})
		sort.Strings(diff.Added)
	}
	sort.Strings(diff.Modified)
	sort.Strings(diff.Deleted)
	return diff
}
