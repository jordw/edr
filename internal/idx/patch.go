package idx

import (
	"os"
	"path/filepath"

	atomicio "github.com/jordw/edr/internal/atomic"
	"github.com/jordw/edr/internal/staleness"
)

// patchEntry holds a single file's re-extracted data in the middle of
// a PatchDirtyFiles run. FileIDs are not assigned yet — rebuildFileTable
// resolves paths to IDs once the file table has settled.
type patchEntry struct {
	entry FileEntry
	tris  []Trigram
	syms  []SymbolEntry // nil when PatchDirtyFiles was called with a nil extractor
}

// PatchDirtyFiles re-indexes only the files in the dirty list, then patches
// them into the existing index. Avoids walking or re-parsing the whole repo.
//
// Dirty files split three ways:
//   - still on disk, readable  → re-index, replace the old entry
//   - still on disk, unreadable/binary → keep the old entry untouched
//   - missing from disk → DROP the file entry and remap symbols
//
// Dropping on delete closes the phantom-symbols bug class. Prior
// behavior preserved the stale FileEntry and symbols that referenced
// it, so queries would keep finding names for files that no longer
// existed.
//
// When extractSymbols is non-nil, symbols for modified and added files
// are re-extracted and merged into the surviving symbol table. This
// keeps symbol coverage consistent across ticks — the sparse-symbols
// bug class. When nil, the legacy drop-only behavior is preserved so
// non-dispatch callers (bench, tests) don't need to know about the
// extractor.
func PatchDirtyFiles(root, edrDir string, dirty []string, extractSymbols SymbolExtractFn) {
	old := loadIndexTrigrams(edrDir)
	if old == nil {
		return
	}
	dirtySet := make(map[string]bool, len(dirty))
	for _, d := range dirty {
		dirtySet[d] = true
	}
	patches, deletedSet := collectPatches(root, dirty, extractSymbols)
	files, oldIDToNewID, newByPath := rebuildFileTable(old.Files, deletedSet, patches)
	triMap := rebuildTrigramMap(old, dirtySet, oldIDToNewID, patches, newByPath)
	postings, entries := BuildPostings(triMap)
	d := &Snapshot{
		Header: Header{
			NumFiles:    uint32(len(files)),
			NumTrigrams: uint32(len(entries)),
			GitMtime:    gitIndexMtime(root),
		},
		Files:    files,
		Trigrams: entries,
		Postings: postings,
	}
	if syms, namePosts, namePostings := rebuildSymbolTable(edrDir, old, files, dirtySet, patches, newByPath); syms != nil {
		d.Symbols = syms
		d.NamePosts = namePosts
		d.NamePostings = namePostings
		d.Header.NumSymbols = uint32(len(syms))
		d.Header.NumNameKeys = uint32(len(namePosts))
	}
	atomicio.WriteFile(filepath.Join(edrDir, MainFile), d.Marshal())
	InvalidateSymbolCache()
	// Auxiliary indices are invalidated by any patch. None of them
	// survive a remap in place:
	//   - popularity.bin: scores computed against the old symbol table.
	//   - refs.bin: stores symbol IDs directly (ForwardOffsets indexed
	//     by ID, InvSymIDs packed IDs). rebuildSymbolTable renumbers
	//     IDs, so old graph IDs now point at different symbols.
	//   - import_graph.bin: frozen path list. Deleted files stay as
	//     phantom importers; new files are missing.
	// Invalidation trades freshness for correctness — next full
	// `edr index` rebuilds them.
	os.Remove(filepath.Join(edrDir, PopularityFile))
	os.Remove(filepath.Join(edrDir, RefGraphFile))
	os.Remove(filepath.Join(edrDir, ImportGraphFile))
	staleness.OpenTracker(edrDir, DirtyTrackerName).Clear()
}

// collectPatches stats+reads each dirty file and re-extracts trigrams
// (and, when extract is non-nil, symbols). Files that couldn't be
// stat'd land in deletedSet — they're pruned from the index. Files
// that exist but are unreadable/binary are silently skipped, leaving
// the old entry in place on the theory that a transient IO error
// shouldn't evict a known-good record.
func collectPatches(root string, dirty []string, extract SymbolExtractFn) (patches []patchEntry, deletedSet map[string]bool) {
	deletedSet = make(map[string]bool, len(dirty))
	for _, rel := range dirty {
		absPath := filepath.Join(root, rel)
		info, err := os.Stat(absPath)
		if err != nil {
			deletedSet[rel] = true
			continue
		}
		data, err := os.ReadFile(absPath)
		if err != nil || isBinary(data) {
			// File exists but became unreadable/binary. Preserve the
			// old entry — the user can always force a full rebuild.
			continue
		}
		pe := patchEntry{
			entry: FileEntry{
				Path:  rel,
				Mtime: info.ModTime().UnixNano(),
				Size:  info.Size(),
			},
			tris: ExtractTrigrams(data),
		}
		if extract != nil {
			pe.syms = extract(absPath, data)
		}
		patches = append(patches, pe)
	}
	return patches, deletedSet
}

// rebuildFileTable produces the new file table. It drops entries in
// deletedSet (closing the phantom bug), then either replaces or
// appends each patch by path — replace when the path already existed
// in the surviving table, append for brand-new files. IDs get
// renumbered; the returned oldIDToNewID and newByPath maps let
// rebuildTrigramMap and rebuildSymbolTable translate references.
func rebuildFileTable(oldFiles []FileEntry, deletedSet map[string]bool, patches []patchEntry) (files []FileEntry, oldIDToNewID map[uint32]uint32, newByPath map[string]uint32) {
	oldIDToNewID = make(map[uint32]uint32, len(oldFiles))
	for i, f := range oldFiles {
		if deletedSet[f.Path] {
			continue
		}
		oldIDToNewID[uint32(i)] = uint32(len(files))
		files = append(files, f)
	}
	// newByPath lets us do replace-by-path in O(1). It's rebuilt once
	// more at the end so appended entries are visible to callers.
	byPath := make(map[string]uint32, len(files))
	for i, f := range files {
		byPath[f.Path] = uint32(i)
	}
	for _, p := range patches {
		if existing, ok := byPath[p.entry.Path]; ok {
			files[existing] = p.entry
		} else {
			byPath[p.entry.Path] = uint32(len(files))
			files = append(files, p.entry)
		}
	}
	return files, oldIDToNewID, byPath
}

// rebuildTrigramMap walks the old trigram postings, keeping IDs that
// refer to unchanged surviving files (remapped to new IDs) and
// dropping IDs for deleted or modified files. Fresh trigrams from
// patches are appended with their new file IDs.
func rebuildTrigramMap(old *Snapshot, dirtySet map[string]bool, oldIDToNewID map[uint32]uint32, patches []patchEntry, newByPath map[string]uint32) map[Trigram][]uint32 {
	triMap := make(map[Trigram][]uint32)
	for _, te := range old.Trigrams {
		ids := DecodePosting(old.Postings, te.Offset, te.Count)
		var kept []uint32
		for _, oldID := range ids {
			if int(oldID) >= len(old.Files) {
				continue
			}
			p := old.Files[oldID].Path
			if dirtySet[p] {
				continue // re-added below with fresh trigrams, or dropped
			}
			if newID, ok := oldIDToNewID[oldID]; ok {
				kept = append(kept, newID)
			}
		}
		if len(kept) > 0 {
			triMap[te.Tri] = kept
		}
	}
	for _, p := range patches {
		newID, ok := newByPath[p.entry.Path]
		if !ok {
			continue
		}
		for _, t := range p.tris {
			triMap[t] = append(triMap[t], newID)
		}
	}
	return triMap
}

// rebuildSymbolTable preserves symbols from the old index, remapping
// FileIDs to the new file table. Symbols from dirty files are dropped
// (they're stale); if an extractor ran during collectPatches, the
// fresh symbols are merged back in with their new FileIDs.
//
// Returns (nil, nil, nil) when the old index had no symbols — ticks
// are coverage-preserving, never coverage-initiating. Starting a
// symbol table from a patch is the full-index path's job.
func rebuildSymbolTable(edrDir string, old *Snapshot, files []FileEntry, dirtySet map[string]bool, patches []patchEntry, newByPath map[string]uint32) ([]SymbolEntry, []NamePostEntry, []byte) {
	if old.Header.NumSymbols == 0 {
		return nil, nil, nil
	}
	full := loadIndex(edrDir)
	if full == nil || len(full.Symbols) == 0 {
		return nil, nil, nil
	}
	remapped := remapSymbols(full.Symbols, full.Files, files, dirtySet)
	for _, p := range patches {
		if len(p.syms) == 0 {
			continue
		}
		newID, ok := newByPath[p.entry.Path]
		if !ok {
			continue
		}
		for _, s := range p.syms {
			s.FileID = newID
			remapped = append(remapped, s)
		}
	}
	if len(remapped) == 0 {
		return nil, nil, nil
	}
	namePostings, namePosts := BuildNamePostings(remapped)
	return remapped, namePosts, namePostings
}

// remapSymbols translates symbol FileIDs from oldFiles to newFiles.
// Symbols whose file was removed or is in the skip set are dropped.
func remapSymbols(symbols []SymbolEntry, oldFiles, newFiles []FileEntry, skip ...map[string]bool) []SymbolEntry {
	newIDByPath := make(map[string]uint32, len(newFiles))
	for i, f := range newFiles {
		newIDByPath[f.Path] = uint32(i)
	}
	var skipSet map[string]bool
	if len(skip) > 0 {
		skipSet = skip[0]
	}
	remapped := make([]SymbolEntry, 0, len(symbols))
	for _, s := range symbols {
		if int(s.FileID) >= len(oldFiles) {
			continue
		}
		oldPath := oldFiles[s.FileID].Path
		if skipSet != nil && skipSet[oldPath] {
			continue
		}
		newID, ok := newIDByPath[oldPath]
		if !ok {
			continue // file removed
		}
		s.FileID = newID
		remapped = append(remapped, s)
	}
	return remapped
}
