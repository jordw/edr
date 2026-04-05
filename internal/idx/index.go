package idx

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Index file names.
const (
	MainFile    = "trigram.idx"
	LockFile    = "trigram.compact.lock"
	JournalPfx  = "trigram."
	JournalSfx  = ".jnl"
)

// BuildFull builds a complete trigram index from the given files.
// root is the repo root; paths are absolute. Returns IndexData ready to marshal.
func BuildFull(root string, paths []string, gitMtime int64) *IndexData {
	d := &IndexData{}
	d.Header.GitMtime = gitMtime

	files := make([]FileEntry, len(paths))
	for i, p := range paths {
		rel, _ := filepath.Rel(root, p)
		if rel == "" {
			rel = p
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		files[i] = FileEntry{
			Path:  rel,
			Mtime: info.ModTime().UnixNano(),
			Size:  info.Size(),
		}
	}

	d.Files = files
	d.Header.NumFiles = uint32(len(files))

	// Build trigram map: trigram → []fileID
	triMap := make(map[Trigram][]uint32)
	for i, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if isBinary(data) {
			continue
		}
		tris := ExtractTrigrams(data)
		fileID := uint32(i)
		for _, t := range tris {
			triMap[t] = append(triMap[t], fileID)
		}
	}

	d.Postings, d.Trigrams = BuildPostings(triMap)
	d.Header.NumTrigrams = uint32(len(d.Trigrams))

	return d
}

// BuildIncremental indexes a batch of files and writes a journal.
// edrDir is the per-repo edr directory. Returns number of files indexed.
func BuildIncremental(root, edrDir string, paths []string, gitMtime int64) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}

	d := BuildFull(root, paths, gitMtime)
	data := d.Marshal()

	jnlPath := filepath.Join(edrDir, fmt.Sprintf("%s%d%s", JournalPfx, os.Getpid(), JournalSfx))

	// If a journal already exists for this PID, merge with it.
	if existing, err := os.ReadFile(jnlPath); err == nil {
		if old, err := Unmarshal(existing); err == nil {
			merged := Merge(old, d)
			data = merged.Marshal()
		}
	}

	if err := atomicWrite(jnlPath, data); err != nil {
		return 0, err
	}
	return len(paths), nil
}

// Query returns candidate file paths (relative to root) that might contain
// all the given trigrams. Returns nil, false if no index is available.
func Query(edrDir string, queryTrigrams []Trigram) ([]string, bool) {
	if len(queryTrigrams) == 0 {
		return nil, false
	}

	// Load main index + all journals
	indices := loadAllIndices(edrDir)
	if len(indices) == 0 {
		return nil, false
	}

	// For each index, find candidate file IDs, convert to paths, union across indices.
	allCandidates := make(map[string]struct{})

	for _, idx := range indices {
		candidates := queryIndex(idx, queryTrigrams)
		if candidates == nil {
			// This index has no data for some trigram — it can't eliminate anything.
			// Add all its files as candidates.
			for _, f := range idx.Files {
				allCandidates[f.Path] = struct{}{}
			}
			continue
		}
		for _, id := range candidates {
			if int(id) < len(idx.Files) {
				allCandidates[idx.Files[id].Path] = struct{}{}
			}
		}
	}

	result := make([]string, 0, len(allCandidates))
	for p := range allCandidates {
		result = append(result, p)
	}
	sort.Strings(result)
	return result, true
}

// queryIndex returns the intersection of posting lists for the given trigrams
// within a single index. Returns nil if any trigram is missing (can't filter).
func queryIndex(d *IndexData, queryTrigrams []Trigram) []uint32 {
	if len(d.Trigrams) == 0 {
		return nil
	}

	// For each query trigram, find its posting list via binary search.
	var lists [][]uint32
	for _, qt := range queryTrigrams {
		te := findTrigram(d.Trigrams, qt)
		if te == nil {
			// Trigram not in this index — this index has nothing to say about it.
			// Return nil to signal "no filtering possible from this index."
			return nil
		}
		ids := DecodePosting(d.Postings, te.Offset, te.Count)
		lists = append(lists, ids)
	}

	if len(lists) == 0 {
		return nil
	}

	// Intersect smallest-first for efficiency
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

// findTrigram does binary search on the sorted trigram table.
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

// intersect returns sorted IDs present in both a and b (both must be sorted).
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

// Merge combines two IndexData into one, deduplicating files by path.
// When a file appears in both, the newer one (by mtime) wins.
func Merge(a, b *IndexData) *IndexData {
	// Build path→entry+ID mappings
	type fileRef struct {
		entry FileEntry
		id    uint32
		src   int // 0 = a, 1 = b
	}
	byPath := make(map[string]fileRef)
	for i, f := range a.Files {
		byPath[f.Path] = fileRef{entry: f, id: uint32(i), src: 0}
	}
	for i, f := range b.Files {
		if existing, ok := byPath[f.Path]; ok {
			if f.Mtime > existing.entry.Mtime {
				byPath[f.Path] = fileRef{entry: f, id: uint32(i), src: 1}
			}
		} else {
			byPath[f.Path] = fileRef{entry: f, id: uint32(i), src: 1}
		}
	}

	// Build merged file list in sorted order
	mergedFiles := make([]FileEntry, 0, len(byPath))
	for _, fr := range byPath {
		mergedFiles = append(mergedFiles, fr.entry)
	}
	sort.Slice(mergedFiles, func(i, j int) bool {
		return mergedFiles[i].Path < mergedFiles[j].Path
	})

	// Build old→new ID maps
	newIDMap := make(map[string]uint32)
	for i, f := range mergedFiles {
		newIDMap[f.Path] = uint32(i)
	}

	// Rebuild trigram map with remapped IDs
	triMap := make(map[Trigram][]uint32)
	remapPostings := func(d *IndexData, src int) {
		for _, te := range d.Trigrams {
			ids := DecodePosting(d.Postings, te.Offset, te.Count)
			for _, oldID := range ids {
				if int(oldID) >= len(d.Files) {
					continue
				}
				path := d.Files[oldID].Path
				// Only include if this file's winning source matches
				if fr, ok := byPath[path]; ok && fr.src == src {
					if newID, ok := newIDMap[path]; ok {
						triMap[te.Tri] = append(triMap[te.Tri], newID)
					}
				}
			}
		}
	}
	remapPostings(a, 0)
	remapPostings(b, 1)

	postings, entries := BuildPostings(triMap)

	return &IndexData{
		Header: Header{
			NumFiles:    uint32(len(mergedFiles)),
			NumTrigrams: uint32(len(entries)),
			GitMtime:    max(a.Header.GitMtime, b.Header.GitMtime),
		},
		Files:    mergedFiles,
		Trigrams: entries,
		Postings: postings,
	}
}

// Staleness checks .git/index mtime against the stored value.
// Returns true if the index may be stale.
func Staleness(repoRoot, edrDir string) bool {
	mainPath := filepath.Join(edrDir, MainFile)
	data, err := os.ReadFile(mainPath)
	if err != nil {
		return true // no index = stale
	}
	d, err := Unmarshal(data)
	if err != nil {
		return true
	}
	return gitIndexMtime(repoRoot) != d.Header.GitMtime
}

// MaybeCompact probabilistically merges main + journals.
// Probability is 1/chance per call. Acquires an exclusive lock; skips if locked.
func MaybeCompact(edrDir string, chance int) {
	if chance > 1 && rand.Intn(chance) != 0 {
		return
	}
	Compact(edrDir)
}

// Compact merges main index + all journals → new main index. Deletes journals.
func Compact(edrDir string) {
	lockPath := filepath.Join(edrDir, LockFile)
	// Acquire exclusive lock via O_CREATE|O_EXCL
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return // another process holds it
	}
	f.Close()
	defer os.Remove(lockPath)

	indices := loadAllIndices(edrDir)
	if len(indices) <= 1 {
		return // nothing to compact
	}

	// Merge all
	merged := indices[0]
	for _, idx := range indices[1:] {
		merged = Merge(merged, idx)
	}

	data := merged.Marshal()
	tmpPath := filepath.Join(edrDir, MainFile+".tmp")
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return
	}
	if err := os.Rename(tmpPath, filepath.Join(edrDir, MainFile)); err != nil {
		os.Remove(tmpPath)
		return
	}

	// Remove journals
	jnls, _ := filepath.Glob(filepath.Join(edrDir, JournalPfx+"*"+JournalSfx))
	for _, j := range jnls {
		os.Remove(j)
	}
}

// Status returns stats about the current index state.
type Status struct {
	Exists       bool
	Files        int
	Trigrams     int
	Journals     int
	SizeBytes    int64
	Stale        bool
	GitMtime     int64
}

// GetStatus returns the current index status.
func GetStatus(repoRoot, edrDir string) Status {
	s := Status{}
	mainPath := filepath.Join(edrDir, MainFile)

	// Main index
	if info, err := os.Stat(mainPath); err == nil {
		s.Exists = true
		s.SizeBytes = info.Size()
		if data, err := os.ReadFile(mainPath); err == nil {
			if d, err := Unmarshal(data); err == nil {
				s.Files = int(d.Header.NumFiles)
				s.Trigrams = int(d.Header.NumTrigrams)
				s.GitMtime = d.Header.GitMtime
			}
		}
	}

	// Journals
	jnls, _ := filepath.Glob(filepath.Join(edrDir, JournalPfx+"*"+JournalSfx))
	s.Journals = len(jnls)
	for _, j := range jnls {
		if info, err := os.Stat(j); err == nil {
			s.SizeBytes += info.Size()
			if !s.Exists {
				// Count journal files too if no main index
				if data, err := os.ReadFile(j); err == nil {
					if d, err := Unmarshal(data); err == nil {
						s.Files += int(d.Header.NumFiles)
						s.Exists = true
					}
				}
			}
		}
	}

	s.Stale = Staleness(repoRoot, edrDir)
	return s
}

// UnindexedFiles returns repo files not yet in any index.
// walks the repo, checks against all indexed file paths.
func UnindexedFiles(root, edrDir string, walkFn func(root string, fn func(path string) error) error) ([]string, error) {
	indexed := make(map[string]struct{})
	for _, d := range loadAllIndices(edrDir) {
		for _, f := range d.Files {
			indexed[f.Path] = struct{}{}
		}
	}

	var unindexed []string
	err := walkFn(root, func(path string) error {
		rel, _ := filepath.Rel(root, path)
		if rel == "" {
			rel = path
		}
		if _, ok := indexed[rel]; !ok {
			unindexed = append(unindexed, path)
		}
		return nil
	})
	return unindexed, err
}

// StaleFiles returns indexed files whose mtime has changed.
func StaleFiles(root, edrDir string) []string {
	var stale []string
	for _, d := range loadAllIndices(edrDir) {
		for _, f := range d.Files {
			abs := filepath.Join(root, f.Path)
			info, err := os.Stat(abs)
			if err != nil {
				stale = append(stale, abs)
				continue
			}
			if info.ModTime().UnixNano() != f.Mtime {
				stale = append(stale, abs)
			}
		}
	}
	return stale
}

// loadAllIndices loads the main index + all journal files.
func loadAllIndices(edrDir string) []*IndexData {
	var indices []*IndexData

	mainPath := filepath.Join(edrDir, MainFile)
	if data, err := os.ReadFile(mainPath); err == nil {
		if d, err := Unmarshal(data); err == nil {
			indices = append(indices, d)
		}
	}

	jnls, _ := filepath.Glob(filepath.Join(edrDir, JournalPfx+"*"+JournalSfx))
	for _, j := range jnls {
		if data, err := os.ReadFile(j); err == nil {
			if d, err := Unmarshal(data); err == nil {
				indices = append(indices, d)
			}
		}
	}

	return indices
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
	// Check first 512 bytes for null bytes
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

// IncrementalTick does a small amount of indexing work on each invocation.
// It finds unindexed or stale files and indexes up to batchSize of them.
// Returns the number of files indexed.
func IncrementalTick(root, edrDir string, batchSize int, walkFn func(root string, fn func(path string) error) error) int {
	// Rate limit: check marker file
	marker := filepath.Join(edrDir, "trigram.tick")
	if info, err := os.Stat(marker); err == nil {
		if time.Since(info.ModTime()) < 5*time.Second {
			return 0
		}
	}
	os.WriteFile(marker, nil, 0600)

	gitMt := gitIndexMtime(root)

	// First: re-index stale files
	stale := StaleFiles(root, edrDir)
	if len(stale) > batchSize {
		stale = stale[:batchSize]
	}
	if len(stale) > 0 {
		n, _ := BuildIncremental(root, edrDir, stale, gitMt)
		if n > 0 {
			MaybeCompact(edrDir, 10)
		}
		return n
	}

	// Then: index new files
	unindexed, err := UnindexedFiles(root, edrDir, walkFn)
	if err != nil || len(unindexed) == 0 {
		return 0
	}
	if len(unindexed) > batchSize {
		unindexed = unindexed[:batchSize]
	}
	n, _ := BuildIncremental(root, edrDir, unindexed, gitMt)
	if n > 0 {
		MaybeCompact(edrDir, 10)
	}
	return n
}

// BuildFullFromWalk builds a complete index by walking the repo.
// progress is called with (indexed, total) after each file. Can be nil.
func BuildFullFromWalk(root, edrDir string, walkFn func(root string, fn func(path string) error) error, progress func(int, int)) error {
	// Enumerate all files
	var paths []string
	walkFn(root, func(path string) error {
		paths = append(paths, path)
		return nil
	})

	if len(paths) == 0 {
		return nil
	}

	gitMt := gitIndexMtime(root)

	// Build in parallel
	type fileResult struct {
		id   uint32
		tris []Trigram
	}

	results := make([]fileResult, len(paths))
	files := make([]FileEntry, len(paths))

	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // limit concurrent reads
	var indexed int
	var mu sync.Mutex

	for i, p := range paths {
		rel, _ := filepath.Rel(root, p)
		if rel == "" {
			rel = p
		}
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		files[i] = FileEntry{
			Path:  rel,
			Mtime: info.ModTime().UnixNano(),
			Size:  info.Size(),
		}

		wg.Add(1)
		go func(idx int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			data, err := os.ReadFile(path)
			if err != nil || isBinary(data) {
				return
			}
			tris := ExtractTrigrams(data)
			results[idx] = fileResult{id: uint32(idx), tris: tris}

			if progress != nil {
				mu.Lock()
				indexed++
				progress(indexed, len(paths))
				mu.Unlock()
			}
		}(i, p)
	}
	wg.Wait()

	// Build trigram map
	triMap := make(map[Trigram][]uint32)
	for _, r := range results {
		for _, t := range r.tris {
			triMap[t] = append(triMap[t], r.id)
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

	data := d.Marshal()

	// Write directly as main index (this is a full build)
	mainPath := filepath.Join(edrDir, MainFile)
	if err := atomicWrite(mainPath, data); err != nil {
		return fmt.Errorf("writing index: %w", err)
	}

	// Clean up any journals
	jnls, _ := filepath.Glob(filepath.Join(edrDir, JournalPfx+"*"+JournalSfx))
	for _, j := range jnls {
		os.Remove(j)
	}

	return nil
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// CaseFoldTrigrams returns trigrams for a case-insensitive query.
// It lowercases the query before extracting trigrams, which means the index
// must also contain lowercase trigrams to support this. For now we index
// raw bytes, so case-insensitive search falls back to full scan.
// This is a future optimization hook.
func CaseFoldTrigrams(query string) []Trigram {
	return QueryTrigrams(strings.ToLower(query))
}
