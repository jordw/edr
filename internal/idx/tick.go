package idx

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"sort"
)

// Staleness returns true if the index is out of date with .git/index.
func Staleness(repoRoot, edrDir string) bool {
	h, err := ReadHeader(edrDir)
	if err != nil {
		return true
	}
	return gitIndexMtime(repoRoot) != h.GitMtime
}

// IncrementalTick reconciles the index against the filesystem.
//
//  1. If .git/index hasn't moved, the fast path returns immediately.
//  2. Otherwise run staleness.Check (via StatChanges) to find new,
//     modified, and deleted files. If the union is non-empty, call
//     PatchDirtyFiles — it rebuilds only the affected records.
//     Deleted files are pruned (closes the phantom-symbols bug).
//     Modified and added files are re-extracted (closes the sparse-
//     symbols bug class) when extractSymbols is non-nil.
//  3. When extractSymbols is nil, PatchDirtyFiles falls back to the
//     legacy behavior of dropping symbols for dirty files without
//     replacement. Bench code and other non-dispatch callers go
//     through this path.
//  4. Finally stamp the new git mtime so the next tick's fast path
//     trips and we don't re-walk on every command.
//
// Time budget: this is called on the hot path (every command), but
// the stat walk is parallel and bounded (~60–70 ms on 93k files).
// PatchDirtyFiles is cheap when the dirty set is small. When a large
// rewrite hits, the caller should prefer `edr index` for a full rebuild.
func IncrementalTick(root, edrDir string, walkFn func(root string, fn func(path string) error) error, extractSymbols SymbolExtractFn) {
	if !Staleness(root, edrDir) {
		return
	}
	// Find what changed on disk. Deleted files must be pruned from
	// the symbol table or queries will return phantom symbols.
	// New and modified files should have their symbols re-extracted
	// so the index doesn't bleed coverage between full rebuilds.
	changes := StatChanges(root, edrDir)
	if changes != nil {
		var dirty []string
		dirty = append(dirty, changes.Modified...)
		dirty = append(dirty, changes.Deleted...)
		dirty = append(dirty, changes.New...)
		// Include any edit-marked files so the patch covers edits
		// made since the last successful build.
		dirty = append(dirty, DirtyFiles(edrDir)...)
		dirty = dedupStrings(dirty)
		if len(dirty) > 0 {
			PatchDirtyFiles(root, edrDir, dirty, extractSymbols)
			// PatchDirtyFiles stamps the new git mtime via the
			// rewritten header; no further stamp needed.
			return
		}
	}
	// Nothing changed for known files — stamp mtime so the fast path
	// trips next time.
	stampMtime(root, edrDir)
}

// dedupStrings returns a sorted de-duplicated copy. Local helper to
// avoid a stable dep on any single caller's ordering.
func dedupStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// stampMtime updates the git mtime in the index header without rebuilding.
// This is a 68-byte read + write — effectively free.
func stampMtime(root, edrDir string) {
	path := filepath.Join(edrDir, MainFile)
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return
	}
	defer f.Close()
	buf := make([]byte, headerSize)
	if _, err := f.ReadAt(buf, 0); err != nil {
		return
	}
	mt := gitIndexMtime(root)
	binary.LittleEndian.PutUint64(buf[20:28], uint64(mt))
	f.WriteAt(buf[20:28], 20)
}

// gitIndexMtime returns the nanosecond mtime of .git/index. Zero when
// the file is absent (bare repo, or repo missing its index).
func gitIndexMtime(repoRoot string) int64 {
	info, err := os.Stat(filepath.Join(repoRoot, ".git", "index"))
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}
