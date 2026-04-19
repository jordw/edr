package idx

import (
	"os"
	"path/filepath"

	atomicio "github.com/jordw/edr/internal/atomic"
)

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

	// Build lookup for old file entries by path.
	oldByPath := make(map[string]int, len(old.Files))
	for i, f := range old.Files {
		oldByPath[f.Path] = i
	}

	// dirtySet has every path the caller asked about — regardless of
	// whether it was modified or deleted on disk.
	dirtySet := make(map[string]bool, len(dirty))
	for _, d := range dirty {
		dirtySet[d] = true
	}

	// Re-extract trigrams for dirty files that still exist. Files that
	// are gone from disk are recorded as deletions and get pruned from
	// the file table and symbol table below.
	type patchEntry struct {
		fileID int
		entry  FileEntry
		tris   []Trigram
		syms   []SymbolEntry // FileID set later once the new table is built
	}
	var patches []patchEntry
	deletedSet := make(map[string]bool, len(dirty))
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
		entry := FileEntry{
			Path:  rel,
			Mtime: info.ModTime().UnixNano(),
			Size:  info.Size(),
		}
		tris := ExtractTrigrams(data)
		id := -1
		if existing, ok := oldByPath[rel]; ok {
			id = existing
		}
		pe := patchEntry{fileID: id, entry: entry, tris: tris}
		if extractSymbols != nil {
			pe.syms = extractSymbols(absPath, data)
		}
		patches = append(patches, pe)
	}

	// Build the new file table. Start by dropping deleted entries —
	// they're not in `patches` (we couldn't stat them) and leaving
	// them in `files` is exactly the phantom bug. This re-numbers
	// file IDs; we remap everything below.
	var files []FileEntry
	oldIDToNewID := make(map[uint32]uint32, len(old.Files))
	for i, f := range old.Files {
		if deletedSet[f.Path] {
			continue
		}
		oldIDToNewID[uint32(i)] = uint32(len(files))
		files = append(files, f)
	}

	// Apply patch entries to the rewritten file table. Appends for
	// brand-new files; overwrites in place when the path already
	// existed. The trigram merge below resolves IDs by path, so we
	// don't need to track them on patchEntry here.
	for _, p := range patches {
		if newID, ok := oldIDToNewID[uint32(p.fileID)]; ok && p.fileID >= 0 {
			files[newID] = p.entry
		} else {
			files = append(files, p.entry)
		}
	}
	newByPath := make(map[string]uint32, len(files))
	for i, f := range files {
		newByPath[f.Path] = uint32(i)
	}

	// Rebuild trigram map: keep old postings for unchanged files
	// (remapped to new IDs), drop entries for deleted + modified
	// files, then add fresh trigrams for the re-indexed ones.
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

	postings, entries := BuildPostings(triMap)
	d := &IndexData{
		Header: Header{
			NumFiles:    uint32(len(files)),
			NumTrigrams: uint32(len(entries)),
			GitMtime:    gitIndexMtime(root),
		},
		Files:    files,
		Trigrams: entries,
		Postings: postings,
	}

	// Preserve symbols from old index, remapping FileIDs to the new file table.
	// Symbols from dirty files are dropped (they're stale); if an extractor
	// was supplied, re-extracted symbols for modified/added files are merged
	// back in with their new FileIDs.
	//
	// If the old index had no symbols we don't start building one here —
	// that's the full-index path's job. Ticks stay coverage-preserving
	// rather than coverage-initiating.
	if old.Header.NumSymbols > 0 {
		if full := loadIndex(edrDir); full != nil && len(full.Symbols) > 0 {
			remapped := remapSymbols(full.Symbols, full.Files, files, dirtySet)
			if extractSymbols != nil {
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
			}
			if len(remapped) > 0 {
				namePostData, namePosts := BuildNamePostings(remapped)
				d.Symbols = remapped
				d.NamePosts = namePosts
				d.NamePostings = namePostData
				d.Header.NumSymbols = uint32(len(remapped))
				d.Header.NumNameKeys = uint32(len(namePosts))
			}
		}
	}

	atomicio.WriteFile(filepath.Join(edrDir, MainFile), d.Marshal())
	InvalidateSymbolCache()
	// Popularity scores are stale after patching — remove so they get
	// recomputed on the next full index build.
	os.Remove(filepath.Join(edrDir, PopularityFile))
	ClearDirty(edrDir)
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
