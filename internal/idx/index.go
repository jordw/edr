package idx

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// MainFile is the index filename within the edr repo directory.
const MainFile = "trigram.idx"

// DirtyFile tracks which files have been edited since the last index build.
// Contains one relative path per line. Empty or absent = clean.
const DirtyFile = "trigram.dirty"

// MarkDirty signals that specific files were edited and the index may be stale.
// Appends the given relative paths to the dirty set.
func MarkDirty(edrDir string, files ...string) {
	path := filepath.Join(edrDir, DirtyFile)
	existing := DirtyFiles(edrDir)
	set := make(map[string]bool, len(existing)+len(files))
	for _, f := range existing {
		set[f] = true
	}
	for _, f := range files {
		set[f] = true
	}
	var lines []string
	for f := range set {
		lines = append(lines, f)
	}
	sort.Strings(lines)
	os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600)
}

// ClearDirty removes the dirty marker after a full index build.
func ClearDirty(edrDir string) {
	os.Remove(filepath.Join(edrDir, DirtyFile))
}

// IsDirty returns true if any files have been edited since the last index build.
func IsDirty(edrDir string) bool {
	info, err := os.Stat(filepath.Join(edrDir, DirtyFile))
	return err == nil && info.Size() > 0
}

// IsDirtyFile returns true if a specific file has been edited since the last build.
func IsDirtyFile(edrDir, relPath string) bool {
	for _, f := range DirtyFiles(edrDir) {
		if f == relPath {
			return true
		}
	}
	return false
}

// DirtyFiles returns the set of files edited since the last index build.
func DirtyFiles(edrDir string) []string {
	data, err := os.ReadFile(filepath.Join(edrDir, DirtyFile))
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		// Skip empty lines and legacy boolean markers ("1")
		if line == "" || line == "1" {
			continue
		}
		// Basic validation: must look like a relative path
		if !strings.Contains(line, "/") && !strings.Contains(line, ".") {
			continue
		}
		files = append(files, line)
	}
	return files
}

// BuildFull builds a complete trigram index from the given absolute paths.
func BuildFull(root string, paths []string, gitMtime int64) *IndexData {
	d := &IndexData{}
	d.Header.GitMtime = gitMtime

	type indexedFile struct {
		entry FileEntry
		data  []byte
	}
	var indexed []indexedFile
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		if rel == "" {
			rel = p
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if isBinary(data) {
			continue
		}
		indexed = append(indexed, indexedFile{
			entry: FileEntry{
				Path:  rel,
				Mtime: info.ModTime().UnixNano(),
				Size:  info.Size(),
			},
			data: data,
		})
	}

	files := make([]FileEntry, len(indexed))
	triMap := make(map[Trigram][]uint32)
	for i, f := range indexed {
		files[i] = f.entry
		fileID := uint32(i)
		for _, t := range ExtractTrigrams(f.data) {
			triMap[t] = append(triMap[t], fileID)
		}
	}

	d.Files = files
	d.Header.NumFiles = uint32(len(files))
	d.Postings, d.Trigrams = BuildPostings(triMap)
	d.Header.NumTrigrams = uint32(len(d.Trigrams))
	return d
}

// Query returns candidate file paths that might contain all query trigrams.
// Returns nil, false if no index exists.
func Query(edrDir string, queryTrigrams []Trigram) ([]string, bool) {
	if len(queryTrigrams) == 0 {
		return nil, false
	}

	f, err := os.Open(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < int64(v2HeaderSize) {
		return nil, false
	}

	// Mmap the entire file — OS pages in only what we touch.
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()),
		syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil, false
	}
	defer syscall.Munmap(data)

	h, err := ReadHeaderBytes(data)
	if err != nil || h.NumTrigrams == 0 {
		return nil, false
	}

	// Locate trigram table.
	trigramTableSize := uint64(h.NumTrigrams) * 16
	var trigramTableOff uint64
	if h.Version >= 3 && h.SymbolOff > 0 {
		trigramTableOff = h.SymbolOff - trigramTableSize
	} else {
		trigramTableOff = uint64(len(data)) - trigramTableSize
	}
	triData := data[trigramTableOff : trigramTableOff+trigramTableSize]

	// Posting data between PostingOff and trigram table.
	var postData []byte
	if h.PostingOff <= uint64(len(data)) && trigramTableOff >= h.PostingOff {
		postData = data[h.PostingOff:trigramTableOff]
	}

	// Binary search + intersect — only touches the pages we need.
	candidates := queryRaw(triData, postData, int(h.NumTrigrams), queryTrigrams)
	if candidates == nil {
		return nil, false
	}

	// Resolve matched file IDs from file table.
	ftData := data[h.FileTableOff:h.PostingOff]
	paths := resolveFileIDsFromTable(ftData, h.NumFiles, candidates)
	sort.Strings(paths)
	return paths, true
}

// ReadHeaderBytes parses a header from an already-read byte slice.
func ReadHeaderBytes(data []byte) (*Header, error) {
	if len(data) < v2HeaderSize {
		return nil, fmt.Errorf("too small")
	}
	if data[0] != 'E' || data[1] != 'D' || data[2] != 'R' {
		return nil, fmt.Errorf("bad magic")
	}
	h := &Header{
		Version:      binary.LittleEndian.Uint32(data[8:12]),
		NumFiles:     binary.LittleEndian.Uint32(data[12:16]),
		NumTrigrams:  binary.LittleEndian.Uint32(data[16:20]),
		GitMtime:     int64(binary.LittleEndian.Uint64(data[20:28])),
		FileTableOff: binary.LittleEndian.Uint64(data[28:36]),
		PostingOff:   binary.LittleEndian.Uint64(data[36:44]),
	}
	if h.Version >= 3 && len(data) >= headerSize {
		h.NumSymbols = binary.LittleEndian.Uint32(data[44:48])
		h.SymbolOff = binary.LittleEndian.Uint64(data[48:56])
		h.NamePostOff = binary.LittleEndian.Uint64(data[56:64])
		h.NumNameKeys = binary.LittleEndian.Uint32(data[64:68])
	}
	return h, nil
}

// queryRaw does trigram lookup + posting intersection on mmap'd bytes.
func queryRaw(triData, postData []byte, numTri int, queryTrigrams []Trigram) []uint32 {
	var lists [][]uint32
	for _, qt := range queryTrigrams {
		i := trigramBinarySearch(triData, numTri, qt)
		if i < 0 {
			return []uint32{} // trigram not in index → no matches
		}
		off := uint64(i) * 16
		count := binary.LittleEndian.Uint32(triData[off+4:])
		postOff := binary.LittleEndian.Uint64(triData[off+8:])
		ids := DecodePosting(postData, postOff, count)
		lists = append(lists, ids)
	}
	if len(lists) == 0 {
		return nil
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	result := lists[0]
	for _, list := range lists[1:] {
		result = intersect(result, list)
		if len(result) == 0 {
			return result
		}
	}
	return result
}

// trigramBinarySearch finds a trigram in the raw 16-byte-entry table.
func trigramBinarySearch(triData []byte, n int, t Trigram) int {
	target := t.ToUint32()
	lo, hi := 0, n-1
	for lo <= hi {
		mid := (lo + hi) / 2
		off := mid * 16
		v := uint32(triData[off])<<16 | uint32(triData[off+1])<<8 | uint32(triData[off+2])
		if v == target {
			return mid
		}
		if v < target {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return -1
}

// resolveFileIDsFromTable extracts paths for given file IDs from a
// file table byte slice (starting at offset 0 within the slice).
func resolveFileIDsFromTable(ftData []byte, numFiles uint32, ids []uint32) []string {
	need := make(map[uint32]bool, len(ids))
	for _, id := range ids {
		need[id] = true
	}
	paths := make([]string, 0, len(ids))
	pos := uint64(0)
	for i := uint32(0); i < numFiles; i++ {
		if pos+2 > uint64(len(ftData)) {
			break
		}
		pathLen := binary.LittleEndian.Uint16(ftData[pos:])
		pos += 2
		if pos+uint64(pathLen)+16 > uint64(len(ftData)) {
			break
		}
		if need[i] {
			paths = append(paths, string(ftData[pos:pos+uint64(pathLen)]))
		}
		pos += uint64(pathLen) + 16 // skip path + mtime(8) + size(8)
	}
	return paths
}

// queryIndex intersects posting lists for the given trigrams.
// Missing trigram → empty slice (no file can match).
// Empty trigram table → nil (can't filter).
func queryIndex(d *IndexData, queryTrigrams []Trigram) []uint32 {
	if len(d.Trigrams) == 0 {
		return nil
	}
	var lists [][]uint32
	for _, qt := range queryTrigrams {
		te := findTrigram(d.Trigrams, qt)
		if te == nil {
			return []uint32{}
		}
		ids := DecodePosting(d.Postings, te.Offset, te.Count)
		lists = append(lists, ids)
	}
	if len(lists) == 0 {
		return nil
	}
	sort.Slice(lists, func(i, j int) bool { return len(lists[i]) < len(lists[j]) })
	result := lists[0]
	for _, list := range lists[1:] {
		result = intersect(result, list)
		if len(result) == 0 {
			return result
		}
	}
	return result
}

func findTrigram(table []TrigramEntry, t Trigram) *TrigramEntry {
	target := t.ToUint32()
	lo, hi := 0, len(table)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		v := table[mid].Tri.ToUint32()
		if v == target {
			return &table[mid]
		}
		if v < target {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return nil
}

func intersect(a, b []uint32) []uint32 {
	out := make([]uint32, 0, len(a))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			out = append(out, a[i])
			i++
			j++
		} else if a[i] < b[j] {
			i++
		} else {
			j++
		}
	}
	return out
}

// Staleness returns true if the index is out of date with .git/index.
func Staleness(repoRoot, edrDir string) bool {
	h, err := ReadHeader(edrDir)
	if err != nil {
		return true
	}
	return gitIndexMtime(repoRoot) != h.GitMtime
}

// IsComplete returns true if the index exists and is not stale, meaning
// it covers all repo files and the unindexed-file walk can be skipped.
func IsComplete(repoRoot, edrDir string) bool {
	if IsDirty(edrDir) {
		return false
	}
	h, err := ReadHeader(edrDir)
	if err != nil {
		return false
	}
	return h.GitMtime != 0 && gitIndexMtime(repoRoot) == h.GitMtime
}

// Status holds index stats for edr index --status.
type Status struct {
	Exists    bool
	Files     int
	Trigrams  int
	SizeBytes int64
	Stale     bool
	GitMtime  int64
}

// GetStatus returns the current index status.
func GetStatus(repoRoot, edrDir string) Status {
	s := Status{}
	mainPath := filepath.Join(edrDir, MainFile)
	info, err := os.Stat(mainPath)
	if err != nil {
		s.Stale = true
		return s
	}
	s.Exists = true
	s.SizeBytes = info.Size()
	if h, err := ReadHeader(edrDir); err == nil {
		s.Files = int(h.NumFiles)
		s.Trigrams = int(h.NumTrigrams)
		s.GitMtime = h.GitMtime
		s.Stale = gitIndexMtime(repoRoot) != h.GitMtime || IsDirty(edrDir)
	} else {
		s.Stale = true
	}
	return s
}

// IncrementalTick rebuilds the index if .git/index has changed.
// Reuses trigrams from unchanged files. Time-capped at ~1s so the agent
// never blocks; partial progress is saved and the next tick continues.
func IncrementalTick(root, edrDir string, walkFn func(root string, fn func(path string) error) error) {
	if !Staleness(root, edrDir) {
		return
	}
	// The index is stale (git mtime changed). Stamp the new mtime so we
	// stop re-checking on every command. The trigram index is still valid
	// for queries — dirty files are already tracked separately and Query
	// callers verify candidates against actual file contents.
	// Full rebuild (walk + re-index) happens only via explicit `edr index`.
	stampMtime(root, edrDir)
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

// patchDirtyFiles re-indexes only the files in the dirty list, then patches
// them into the existing index. Avoids walking or re-parsing the whole repo.
func PatchDirtyFiles(root, edrDir string, dirty []string) {
	old := loadIndexTrigrams(edrDir)
	if old == nil {
		return
	}

	// Build lookup for old file entries by path.
	oldByPath := make(map[string]int, len(old.Files))
	for i, f := range old.Files {
		oldByPath[f.Path] = i
	}

	// Invert postings only for dirty files (not all files).
	dirtySet := make(map[string]bool, len(dirty))
	for _, d := range dirty {
		dirtySet[d] = true
	}
	// Collect old trigrams for dirty files so we can remove them.
	oldDirtyTris := make(map[int][]Trigram) // fileID → trigrams
	for _, te := range old.Trigrams {
		ids := DecodePosting(old.Postings, te.Offset, te.Count)
		for _, id := range ids {
			if int(id) < len(old.Files) && dirtySet[old.Files[id].Path] {
				oldDirtyTris[int(id)] = append(oldDirtyTris[int(id)], te.Tri)
			}
		}
	}

	// Re-extract trigrams for dirty files.
	type patchEntry struct {
		fileID int
		entry  FileEntry
		tris   []Trigram
	}
	var patches []patchEntry
	for _, rel := range dirty {
		absPath := filepath.Join(root, rel)
		info, err := os.Stat(absPath)
		if err != nil {
			continue // file deleted — will be absent from new index
		}
		data, err := os.ReadFile(absPath)
		if err != nil || isBinary(data) {
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
		patches = append(patches, patchEntry{fileID: id, entry: entry, tris: tris})
	}

	// Rebuild: start from old files, replace/add dirty ones.
	files := make([]FileEntry, len(old.Files))
	copy(files, old.Files)
	for _, p := range patches {
		if p.fileID >= 0 {
			files[p.fileID] = p.entry
		} else {
			// New file — append
			p.fileID = len(files)
			files = append(files, p.entry)
		}
	}

	// Rebuild trigram map: reuse old postings, patch dirty file entries.
	triMap := make(map[Trigram][]uint32)
	for _, te := range old.Trigrams {
		ids := DecodePosting(old.Postings, te.Offset, te.Count)
		// Filter out dirty file IDs (they get re-added with new trigrams).
		var kept []uint32
		for _, id := range ids {
			if int(id) < len(old.Files) && !dirtySet[old.Files[id].Path] {
				kept = append(kept, id)
			}
		}
		if len(kept) > 0 {
			triMap[te.Tri] = kept
		}
	}
	// Add new trigrams for patched files.
	for _, p := range patches {
		for _, t := range p.tris {
			triMap[t] = append(triMap[t], uint32(p.fileID))
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
	// Symbols from dirty files are dropped (they're stale).
	if old.Header.NumSymbols > 0 {
		if full := loadIndex(edrDir); full != nil && len(full.Symbols) > 0 {
			remapped := remapSymbols(full.Symbols, full.Files, files, dirtySet)
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

	atomicWrite(filepath.Join(edrDir, MainFile), d.Marshal())
	InvalidateSymbolCache()
	// Popularity scores are stale after patching — remove so they get
	// recomputed on the next full index build.
	os.Remove(filepath.Join(edrDir, PopularityFile))
	ClearDirty(edrDir)
}

// rebuildSmart walks the repo and rebuilds the index, reusing cached trigrams
// for files whose mtime hasn't changed. Stops re-indexing new/stale files
// after the time limit but always writes a complete index with what it has.
func rebuildSmart(root, edrDir string, walkFn func(root string, fn func(path string) error) error, limit time.Duration) {
	// Load old index (trigrams only — skip symbol parsing which is
	// 40MB+ of allocations on large repos and dominates IncrementalTick).
	old := loadIndexTrigrams(edrDir)
	oldByPath := make(map[string]FileEntry)
	oldTris := make(map[string][]Trigram)
	if old != nil {
		for _, f := range old.Files {
			oldByPath[f.Path] = f
		}
		for _, te := range old.Trigrams {
			ids := DecodePosting(old.Postings, te.Offset, te.Count)
			for _, id := range ids {
				if int(id) < len(old.Files) {
					oldTris[old.Files[id].Path] = append(oldTris[old.Files[id].Path], te.Tri)
				}
			}
		}
	}

	// Walk repo
	var paths []string
	walkFn(root, func(path string) error {
		paths = append(paths, path)
		return nil
	})
	if len(paths) == 0 {
		return
	}

	gitMt := gitIndexMtime(root)
	deadline := time.Now().Add(limit)

	type fileResult struct {
		entry FileEntry
		tris  []Trigram
	}

	// Phase 1: classify files as reusable or needing re-index.
	// This is just stat calls — no file reads yet.
	var reusable []fileResult
	var needIndex []struct {
		path  string
		entry FileEntry
	}
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		if rel == "" {
			rel = p
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		entry := FileEntry{
			Path:  rel,
			Mtime: info.ModTime().UnixNano(),
			Size:  info.Size(),
		}
		if oldEntry, ok := oldByPath[rel]; ok && oldEntry.Mtime == entry.Mtime {
			if tris, ok := oldTris[rel]; ok {
				reusable = append(reusable, fileResult{entry: entry, tris: tris})
				continue
			}
		}
		needIndex = append(needIndex, struct {
			path  string
			entry FileEntry
		}{path: p, entry: entry})
	}

	// Phase 2: re-index changed files, checking deadline between each.
	var indexed []fileResult
	timedOut := false
	for i := 0; i < len(needIndex); i++ {
		if time.Now().After(deadline) {
			timedOut = true
			for _, rem := range needIndex[i:] {
				if tris, ok := oldTris[rem.entry.Path]; ok {
					reusable = append(reusable, fileResult{entry: rem.entry, tris: tris})
				}
			}
			break
		}
		f := needIndex[i]
		data, err := os.ReadFile(f.path)
		if err != nil || isBinary(data) {
			continue
		}
		tris := ExtractTrigrams(data)
		indexed = append(indexed, fileResult{entry: f.entry, tris: tris})
	}

	results := append(reusable, indexed...)

	// Sort for deterministic output
	sort.Slice(results, func(i, j int) bool {
		return results[i].entry.Path < results[j].entry.Path
	})

	files := make([]FileEntry, len(results))
	triMap := make(map[Trigram][]uint32)
	for i, r := range results {
		files[i] = r.entry
		fileID := uint32(i)
		for _, t := range r.tris {
			triMap[t] = append(triMap[t], fileID)
		}
	}

	postings, entries := BuildPostings(triMap)
	d := &IndexData{
		Header: Header{
			NumFiles:    uint32(len(files)),
			NumTrigrams: uint32(len(entries)),
			GitMtime:    gitMt,
		},
		Files:    files,
		Trigrams: entries,
		Postings: postings,
	}

	// Preserve symbol data from old index if available, remapping FileIDs.
	// rebuildSmart only updates trigrams; full symbol rebuild requires edr index.
	if old != nil && old.Header.NumSymbols > 0 {
		if full := loadIndex(edrDir); full != nil && len(full.Symbols) > 0 {
			remapped := remapSymbols(full.Symbols, full.Files, d.Files)
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

	// If timed out, zero the git mtime so next tick retries the remaining files
	if timedOut {
		d.Header.GitMtime = 0
	}

	atomicWrite(filepath.Join(edrDir, MainFile), d.Marshal())
	InvalidateSymbolCache()
	if !timedOut {
		ClearDirty(edrDir)
	}
}

// SymbolExtractFn extracts symbols from a file's content.
type SymbolExtractFn func(path string, data []byte) []SymbolEntry

// ImportExtractFn extracts raw import strings from file content.
// Called from worker goroutines — must be safe for concurrent use.
type ImportExtractFn func(relPath string, data []byte) []string

// ImportRecord holds raw imports extracted from a single file.
// Idents is populated by the walk from per-symbol identifier extraction.
type ImportRecord struct {
	ImporterRel string
	Ext         string
	Raws        []string
	Idents      map[string]bool // file-level identifier set (union of symbol idents)
}

// ImportResolveFn resolves raw imports into (importer, imported) edge pairs.
// Called once after the walk completes with the full file list and symbol table.
type ImportResolveFn func(allFiles []string, imports []ImportRecord, allSymbols []SymbolEntry, symFiles []FileEntry) [][2]string

// extractSymIdents tokenizes each symbol body into a set of identifiers.
// Returns a slice parallel to syms — idents[i] contains tokens from syms[i].
func extractSymIdents(data []byte, syms []SymbolEntry) [][]string {
	idents := make([][]string, len(syms))
	for i, s := range syms {
		if int(s.EndByte) > len(data) || s.StartByte >= s.EndByte {
			continue
		}
		idents[i] = tokenizeIdents(data[s.StartByte:s.EndByte])
	}
	return idents
}

// tokenizeIdents extracts unique identifier tokens (word-like sequences >= 2 chars).
func tokenizeIdents(body []byte) []string {
	seen := make(map[string]struct{}, 32)
	word := make([]byte, 0, 64)
	for _, b := range body {
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
			word = append(word, b)
		} else {
			if len(word) >= 2 {
				seen[string(word)] = struct{}{}
			}
			word = word[:0]
		}
	}
	if len(word) >= 2 {
		seen[string(word)] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for s := range seen {
		result = append(result, s)
	}
	return result
}

// BuildFullFromWalk builds a complete index by walking the repo. No time limit.
func BuildFullFromWalk(root, edrDir string, walkFn func(root string, fn func(path string) error) error, progress func(int, int), extractSymbols ...SymbolExtractFn) error {
	var symFn SymbolExtractFn
	if len(extractSymbols) > 0 {
		symFn = extractSymbols[0]
	}
	return BuildFullFromWalkWithImports(root, edrDir, walkFn, progress, symFn, nil, nil)
}

// BuildFullFromWalkWithImports builds a complete trigram index and optionally
// an import graph in a single walk. Import extraction runs in the worker pool
// (no redundant file reads) and the graph is written atomically with the index.
func BuildFullFromWalkWithImports(root, edrDir string, walkFn func(root string, fn func(path string) error) error, progress func(int, int), extractSymbols SymbolExtractFn, extractImports ImportExtractFn, resolveImports ImportResolveFn) error {
	var paths []string
	if err := walkFn(root, func(path string) error {
		paths = append(paths, path)
		return nil
	}); err != nil {
		return fmt.Errorf("walking repo: %w", err)
	}
	if len(paths) == 0 {
		return nil
	}

	gitMt := gitIndexMtime(root)
	type fileResult struct {
		entry  FileEntry
		tris   []Trigram
		syms   []SymbolEntry
		raws      []string   // raw import strings (nil if no import extraction)
		symIdents [][]string  // identifier tokens per symbol (parallel to syms)
	}
	workers := runtime.NumCPU()
	if workers < 8 {
		workers = 8
	}
	resultCh := make(chan fileResult, workers*4)
	pathCh := make(chan int, workers*4)
	var done atomic.Int64
	total := len(paths)

	// Load old index to reuse trigrams+symbols for unchanged files.
	old := loadIndex(edrDir)
	type oldFileData struct {
		entry FileEntry
		tris  []Trigram
		syms  []SymbolEntry
	}
	var oldByPath map[string]*oldFileData
	if old != nil {
		oldByPath = make(map[string]*oldFileData, len(old.Files))
		for i := range old.Files {
			oldByPath[old.Files[i].Path] = &oldFileData{entry: old.Files[i]}
		}
		// Reconstruct per-file trigrams from postings.
		for _, te := range old.Trigrams {
			ids := DecodePosting(old.Postings, te.Offset, te.Count)
			for _, id := range ids {
				if int(id) < len(old.Files) {
					d := oldByPath[old.Files[id].Path]
					d.tris = append(d.tris, te.Tri)
				}
			}
		}
		// Reconstruct per-file symbols.
		// Validate byte ranges: skip symbols whose offsets exceed the
		// file size (ghosts from prior corrupt indices).
		for _, s := range old.Symbols {
			if int(s.FileID) < len(old.Files) {
				f := old.Files[s.FileID]
				if int64(s.EndByte) > f.Size {
					continue
				}
				d := oldByPath[f.Path]
				d.syms = append(d.syms, s)
			}
		}
	}

	// Load old import graph to carry forward edges for reused files.
	var oldGraph *ImportGraphData
	if extractImports != nil {
		oldGraph = ReadImportGraph(edrDir)
	}
	var oldForward map[uint32][]uint32
	if oldGraph != nil {
		oldForward = make(map[uint32][]uint32, len(oldGraph.Files))
		for _, e := range oldGraph.Edges {
			oldForward[e.Importer] = append(oldForward[e.Importer], e.Imported)
		}
	}

	// Pre-compute relative paths and stat info. Classify as reusable or needing re-index.
	type pathInfo struct {
		rel  string
		abs  string
		info os.FileInfo
	}
	var needIndex []pathInfo
	var reused []fileResult
	var oldEdges [][2]string // carried-forward import edges for reused files
	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		if rel == "" {
			rel = p
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		// Reuse old data if mtime matches — skip trigram/symbol extraction.
		// Still read file to extract per-symbol identifiers for the ref graph.
		// Only reuse symbols if the old index actually had them (rebuildSmart omits symbols).
		hasOldSyms := old != nil && old.Header.NumSymbols > 0
		if od, ok := oldByPath[rel]; ok && od.entry.Mtime == info.ModTime().UnixNano() && len(od.tris) > 0 && (hasOldSyms || extractSymbols == nil) {
			fr := fileResult{
				entry: FileEntry{Path: rel, Mtime: info.ModTime().UnixNano(), Size: info.Size()},
				tris:  od.tris,
				syms:  od.syms,
			}
			if len(fr.syms) > 0 {
				if data, err := os.ReadFile(p); err == nil {
					fr.symIdents = extractSymIdents(data, fr.syms)
				}
			}
			reused = append(reused, fr)
			// Carry forward import edges for this unchanged file.
			if oldGraph != nil {
				if id, ok := oldGraph.fileIdx[rel]; ok {
					for _, importedID := range oldForward[id] {
						if int(importedID) < len(oldGraph.Files) {
							oldEdges = append(oldEdges, [2]string{rel, oldGraph.Files[importedID]})
						}
					}
				}
			}
			continue
		}
		needIndex = append(needIndex, pathInfo{rel: rel, abs: p, info: info})
	}
	total = len(needIndex) + len(reused)

	// Fast path: nothing changed — skip rebuild entirely.
	if len(needIndex) == 0 && old != nil && old.Header.NumSymbols > 0 {
		ClearDirty(edrDir)
		return nil
	}

	// Worker pool — only processes files that actually need re-indexing.
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range pathCh {
				pi := needIndex[idx]
				data, err := os.ReadFile(pi.abs)
				if err != nil || isBinary(data) {
					if progress != nil {
						n := int(done.Add(1))
						progress(n, total)
					}
					continue
				}
				entry := FileEntry{Path: pi.rel, Mtime: pi.info.ModTime().UnixNano(), Size: pi.info.Size()}
				fr := fileResult{entry: entry, tris: ExtractTrigrams(data)}
				if extractSymbols != nil {
					fr.syms = extractSymbols(pi.abs, data)
				}
				if extractImports != nil {
					fr.raws = extractImports(pi.rel, data)
				}
				if len(fr.syms) > 0 {
					fr.symIdents = extractSymIdents(data, fr.syms)
				}
				resultCh <- fr
				if progress != nil {
					n := int(done.Add(1))
					progress(n, total)
				}
			}
		}()
	}

	// Feed paths to workers
	go func() {
		for i := range needIndex {
			pathCh <- i
		}
		close(pathCh)
		wg.Wait()
		close(resultCh)
	}()

	type collected struct {
		entry     FileEntry
		tris      []Trigram
		syms      []SymbolEntry
		raws      []string
		symIdents [][]string // per-symbol identifier tokens (parallel to syms)
	}
	// Start with reused results, append newly indexed ones.
	results := make([]collected, 0, len(reused)+len(needIndex))
	for _, r := range reused {
		results = append(results, collected{entry: r.entry, tris: r.tris, syms: r.syms, symIdents: r.symIdents})
	}
	for r := range resultCh {
		results = append(results, collected{entry: r.entry, tris: r.tris, syms: r.syms, raws: r.raws, symIdents: r.symIdents})
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].entry.Path < results[j].entry.Path
	})

	files := make([]FileEntry, len(results))
	triMap := make(map[Trigram][]uint32)
	for i, r := range results {
		files[i] = r.entry
		fileID := uint32(i)
		for _, t := range r.tris {
			triMap[t] = append(triMap[t], fileID)
		}
	}

	postings, entries := BuildPostings(triMap)

	// Build symbol table with correct file IDs (after sort)
	var allSymbols []SymbolEntry
	for i, r := range results {
		fileID := uint32(i)
		for _, s := range r.syms {
			s.FileID = fileID
			allSymbols = append(allSymbols, s)
		}
	}

	// Build name postings
	var namePostData []byte
	var namePosts []NamePostEntry
	if len(allSymbols) > 0 {
		namePostData, namePosts = BuildNamePostings(allSymbols)
	}
	d := &IndexData{
		Header: Header{
			NumFiles:    uint32(len(files)),
			NumTrigrams: uint32(len(entries)),
			GitMtime:    gitMt,
			NumSymbols:  uint32(len(allSymbols)),
			NumNameKeys: uint32(len(namePosts)),
		},
		Files:        files,
		Trigrams:     entries,
		Postings:     postings,
		Symbols:      allSymbols,
		NamePosts:    namePosts,
		NamePostings: namePostData,
	}
	if err := atomicWrite(filepath.Join(edrDir, MainFile), d.Marshal()); err != nil {
		return fmt.Errorf("writing index: %w", err)
	}

	// --- Post-walk phase: resolve imports and references from collected data ---

	allRelPaths := make([]string, len(files))
	for i := range files {
		allRelPaths[i] = files[i].Path
	}

	// Build and write import graph.
	if extractImports != nil && resolveImports != nil {
		var importRecords []ImportRecord
		for _, r := range results {
			if len(r.raws) == 0 {
				continue
			}
			// Compute file-level identifier set from per-symbol idents.
			var fileIdents map[string]bool
			if len(r.symIdents) > 0 {
				fileIdents = make(map[string]bool, 64)
				for _, si := range r.symIdents {
					for _, id := range si {
						fileIdents[id] = true
					}
				}
			}
			importRecords = append(importRecords, ImportRecord{
				ImporterRel: r.entry.Path,
				Ext:         strings.ToLower(filepath.Ext(r.entry.Path)),
				Raws:        r.raws,
				Idents:      fileIdents,
			})
		}
		var resolvedEdges [][2]string
		if len(importRecords) > 0 {
			resolvedEdges = resolveImports(allRelPaths, importRecords, allSymbols, files)
		}
		allEdges := append(oldEdges, resolvedEdges...)
		if len(allEdges) > 0 {
			graph := BuildImportGraph(allRelPaths, allEdges)
			WriteImportGraph(edrDir, graph)
		}
	}

	// Build and write symbol reference graph (v2: name-based).
	if len(allSymbols) > 0 {
		// Flatten per-symbol idents into a single slice parallel to allSymbols.
		perSymIdents := make([][]string, len(allSymbols))
		symIdx := 0
		for _, r := range results {
			for j := range r.syms {
				if j < len(r.symIdents) {
					perSymIdents[symIdx] = r.symIdents[j]
				}
				symIdx++
			}
		}
		rg := BuildRefGraphV2(uint32(len(allSymbols)), perSymIdents)
		WriteRefGraph(edrDir, rg)
	}

	// Compute and write per-symbol popularity scores.
	// Requires both import graph and ref graph.
	importGraph := ReadImportGraph(edrDir)
	refGraph := ReadRefGraph(edrDir)
	if importGraph != nil && refGraph != nil && len(allSymbols) > 0 {
		popScores := ComputePopularity(allSymbols, files, importGraph, refGraph)
		WritePopularity(edrDir, popScores)
	}

	ClearDirty(edrDir)
	return nil
}

// --- Internal helpers ---

// IndexedPaths returns the set of file paths in the index.
// Returns nil if no index exists.
func IndexedPaths(edrDir string) map[string]struct{} {
	d := loadIndex(edrDir)
	if d == nil {
		return nil
	}
	m := make(map[string]struct{}, len(d.Files))
	for _, f := range d.Files {
		m[f.Path] = struct{}{}
	}
	return m
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

func loadIndex(edrDir string) *IndexData {
	data, err := os.ReadFile(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil
	}
	d, err := Unmarshal(data)
	if err != nil {
		return nil
	}
	return d
}

// loadIndexTrigrams loads only file table + trigrams + postings, skipping
// symbol parsing. ~2x faster than loadIndex on large repos.
func loadIndexTrigrams(edrDir string) *IndexData {
	data, err := os.ReadFile(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil
	}
	d, err := UnmarshalTrigrams(data)
	if err != nil {
		return nil
	}
	return d
}

func gitIndexMtime(repoRoot string) int64 {
	info, err := os.Stat(filepath.Join(repoRoot, ".git", "index"))
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Changes holds the result of comparing indexed file metadata against the
// filesystem. Modified files have a different mtime or size than the index.
// Deleted files no longer exist on disk. New files exist in directories
// whose mtime changed but are not in the index.
type Changes struct {
	Modified []string // relative paths — mtime or size differs
	Deleted  []string // relative paths — file no longer exists
	New      []string // relative paths — not in index
}

// Empty returns true if no changes were detected.
func (c *Changes) Empty() bool {
	return len(c.Modified) == 0 && len(c.Deleted) == 0 && len(c.New) == 0
}

// StatChanges loads the file table from the index and parallel-stats every
// file to find modifications, deletions, and new files. Costs ~66ms on a
// 93K-file repo (Linux kernel). Returns nil if no index exists.
func StatChanges(root, edrDir string) *Changes {
	// Mmap the index and walk the file table in-place — avoids reading
	// 5MB into heap and allocating 93K FileEntry structs.
	f, err := os.Open(filepath.Join(edrDir, MainFile))
	if err != nil {
		return nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.Size() < int64(v2HeaderSize) {
		return nil
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()),
		syscall.PROT_READ, syscall.MAP_PRIVATE)
	if err != nil {
		return nil
	}
	defer syscall.Munmap(data)

	h, err := ReadHeaderBytes(data)
	if err != nil || h.NumFiles == 0 {
		return nil
	}

	// Parse file entries from mmap into lightweight path+mtime+size slices.
	type fileRef struct {
		path  string
		mtime int64
		size  int64
	}
	numFiles := int(h.NumFiles)
	refs := make([]fileRef, 0, numFiles)
	ftData := data[h.FileTableOff:h.PostingOff]
	pos := 0
	for i := 0; i < numFiles; i++ {
		if pos+2 > len(ftData) {
			break
		}
		pathLen := int(binary.LittleEndian.Uint16(ftData[pos:]))
		pos += 2
		if pos+pathLen+16 > len(ftData) {
			break
		}
		p := string(ftData[pos : pos+pathLen])
		pos += pathLen
		mtime := int64(binary.LittleEndian.Uint64(ftData[pos:]))
		pos += 8
		size := int64(binary.LittleEndian.Uint64(ftData[pos:]))
		pos += 8
		refs = append(refs, fileRef{path: p, mtime: mtime, size: size})
	}

	if len(refs) == 0 {
		return nil
	}

	type statResult struct {
		rel     string
		deleted bool
		changed bool
	}

	// Parallel stat all indexed files.
	workers := runtime.GOMAXPROCS(0)
	if workers < 4 {
		workers = 4
	}
	ch := make(chan int, 256)
	results := make([]statResult, len(refs))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ri := range ch {
				ref := &refs[ri]
				abs := filepath.Join(root, ref.path)
				info, err := os.Lstat(abs)
				if err != nil {
					results[ri] = statResult{rel: ref.path, deleted: true}
				} else if info.ModTime().UnixNano() != ref.mtime || info.Size() != ref.size {
					results[ri] = statResult{rel: ref.path, changed: true}
				}
			}
		}()
	}
	for i := range refs {
		ch <- i
	}
	close(ch)
	wg.Wait()

	// Build directory mtime map and indexed set for new-file detection.
	indexedDirs := make(map[string]int64, 4096)
	indexedSet := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		indexedSet[ref.path] = struct{}{}
		dir := filepath.Dir(ref.path)
		if ref.mtime > indexedDirs[dir] {
			indexedDirs[dir] = ref.mtime
		}
	}

	c := &Changes{}
	for _, r := range results {
		switch {
		case r.deleted:
			c.Deleted = append(c.Deleted, r.rel)
		case r.changed:
			c.Modified = append(c.Modified, r.rel)
		}
	}

	// Scan directories for new files. A directory with a changed mtime
	// has had files created or deleted. We check every indexed directory.
	for dir, maxMtime := range indexedDirs {
		info, err := os.Stat(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		if info.ModTime().UnixNano() <= maxMtime {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			rel := filepath.Join(dir, e.Name())
			if _, indexed := indexedSet[rel]; !indexed {
				c.New = append(c.New, rel)
			}
		}
	}

	return c
}

func isBinary(data []byte) bool {
	check := data
	if len(check) > 512 {
		check = check[:512]
	}
	for _, b := range check {
		if b == 0 {
			return true
		}
	}
	return false
}
