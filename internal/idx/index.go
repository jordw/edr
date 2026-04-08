package idx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
		for _, t := range ExtractTrigrams(bytes.ToLower(f.data)) {
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
	d := loadIndex(edrDir)
	if d == nil {
		return nil, false
	}

	candidates := queryIndex(d, queryTrigrams)
	if candidates == nil {
		// Empty trigram table — can't filter, return all files.
		result := make([]string, len(d.Files))
		for i, f := range d.Files {
			result[i] = f.Path
		}
		return result, true
	}

	result := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if int(id) < len(d.Files) {
			result = append(result, d.Files[id].Path)
		}
	}
	sort.Strings(result)
	return result, true
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
	rebuildSmart(root, edrDir, walkFn, time.Second)
}

// rebuildSmart walks the repo and rebuilds the index, reusing cached trigrams
// for files whose mtime hasn't changed. Stops re-indexing new/stale files
// after the time limit but always writes a complete index with what it has.
func rebuildSmart(root, edrDir string, walkFn func(root string, fn func(path string) error) error, limit time.Duration) {
	// Load old index to reuse trigrams for unchanged files
	old := loadIndex(edrDir)
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
		tris := ExtractTrigrams(bytes.ToLower(data))
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

	// Preserve symbol data from old index if available.
	// rebuildSmart only updates trigrams; full symbol rebuild requires edr index.
	if old != nil && len(old.Symbols) > 0 {
		d.Symbols = old.Symbols
		d.NamePosts = old.NamePosts
		d.NamePostings = old.NamePostings
		d.Header.NumSymbols = old.Header.NumSymbols
		d.Header.NumNameKeys = old.Header.NumNameKeys
	}

	// If timed out, zero the git mtime so next tick retries the remaining files
	if timedOut {
		d.Header.GitMtime = 0
	}

	atomicWrite(filepath.Join(edrDir, MainFile), d.Marshal())
	if !timedOut {
		ClearDirty(edrDir)
	}
}

// BuildFullFromWalk builds a complete index by walking the repo. No time limit.
// SymbolExtractFn extracts symbols from a file. Returns (name, kind, startLine, endLine, startByte, endByte) tuples.
type SymbolExtractFn func(path string, data []byte) []SymbolEntry

func BuildFullFromWalk(root, edrDir string, walkFn func(root string, fn func(path string) error) error, progress func(int, int), extractSymbols ...SymbolExtractFn) error {
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
		entry FileEntry
		tris  []Trigram
		syms  []SymbolEntry // v3: extracted symbols
	}
	resultCh := make(chan fileResult, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	var done int
	var mu sync.Mutex

	for _, p := range paths {
		rel, _ := filepath.Rel(root, p)
		if rel == "" {
			rel = p
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		entry := FileEntry{Path: rel, Mtime: info.ModTime().UnixNano(), Size: info.Size()}
		wg.Add(1)
		go func(path string, entry FileEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := os.ReadFile(path)
			if err != nil || isBinary(data) {
				return
			}
			fr := fileResult{entry: entry, tris: ExtractTrigrams(bytes.ToLower(data))}
			if len(extractSymbols) > 0 && extractSymbols[0] != nil {
				fr.syms = extractSymbols[0](path, data)
			}
			resultCh <- fr
			if progress != nil {
				mu.Lock()
				done++
				progress(done, len(paths))
				mu.Unlock()
			}
		}(p, entry)
	}
	go func() { wg.Wait(); close(resultCh) }()

	type collected struct {
		entry FileEntry
		tris  []Trigram
		syms  []SymbolEntry
	}
	var results []collected
	for r := range resultCh {
		results = append(results, collected{entry: r.entry, tris: r.tris, syms: r.syms})
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
