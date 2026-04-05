package idx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// MainFile is the index filename within the edr repo directory.
const MainFile = "trigram.idx"

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
	d := loadIndex(edrDir)
	if d == nil {
		return true
	}
	return gitIndexMtime(repoRoot) != d.Header.GitMtime
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
	if data, err := os.ReadFile(mainPath); err == nil {
		if d, err := Unmarshal(data); err == nil {
			s.Files = int(d.Header.NumFiles)
			s.Trigrams = int(d.Header.NumTrigrams)
			s.GitMtime = d.Header.GitMtime
		}
	}
	s.Stale = Staleness(repoRoot, edrDir)
	return s
}

// IncrementalTick rebuilds the index if .git/index has changed.
// Reuses trigrams from unchanged files. Time-capped at ~1s so the agent
// never blocks; partial progress is saved and the next tick continues.
// Rate-limited to once per 5 seconds.
func IncrementalTick(root, edrDir string, walkFn func(root string, fn func(path string) error) error) {
	marker := filepath.Join(edrDir, "trigram.tick")
	if info, err := os.Stat(marker); err == nil {
		if time.Since(info.ModTime()) < 5*time.Second {
			return
		}
	}
	os.WriteFile(marker, nil, 0600)

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

	var results []fileResult
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	timedOut := false

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

		// Reuse old trigrams if mtime unchanged
		if oldEntry, ok := oldByPath[rel]; ok && oldEntry.Mtime == entry.Mtime {
			if tris, ok := oldTris[rel]; ok {
				mu.Lock()
				results = append(results, fileResult{entry: entry, tris: tris})
				mu.Unlock()
				continue
			}
		}

		// Need to re-index — check time limit
		if time.Now().After(deadline) {
			timedOut = true
			// Keep old trigrams if available (stale but better than nothing)
			if tris, ok := oldTris[rel]; ok {
				mu.Lock()
				results = append(results, fileResult{entry: entry, tris: tris})
				mu.Unlock()
			}
			continue
		}

		wg.Add(1)
		go func(path string, entry FileEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			data, err := os.ReadFile(path)
			if err != nil || isBinary(data) {
				return
			}
			tris := ExtractTrigrams(bytes.ToLower(data))
			mu.Lock()
			results = append(results, fileResult{entry: entry, tris: tris})
			mu.Unlock()
		}(p, entry)
	}
	wg.Wait()

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

	// If timed out, keep old git mtime so next tick retries the remaining files
	if timedOut && old != nil {
		d.Header.GitMtime = old.Header.GitMtime
	}

	atomicWrite(filepath.Join(edrDir, MainFile), d.Marshal())
}

// BuildFullFromWalk builds a complete index by walking the repo. No time limit.
func BuildFullFromWalk(root, edrDir string, walkFn func(root string, fn func(path string) error) error, progress func(int, int)) error {
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
			resultCh <- fileResult{entry: entry, tris: ExtractTrigrams(bytes.ToLower(data))}
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
	}
	var results []collected
	for r := range resultCh {
		results = append(results, collected{entry: r.entry, tris: r.tris})
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
	if err := atomicWrite(filepath.Join(edrDir, MainFile), d.Marshal()); err != nil {
		return fmt.Errorf("writing index: %w", err)
	}
	return nil
}

// --- Internal helpers ---

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
