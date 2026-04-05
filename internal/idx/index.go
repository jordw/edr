package idx

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
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

	// Build file entries and trigram map in a single pass.
	// Use append (not pre-allocate by index) to avoid ghost entries from stat failures.
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
		// Index lowercased content so both case-sensitive and case-insensitive
		// queries can use the trigram index (queries also lowercase).
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
	return int(d.Header.NumFiles), nil
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
			// Empty index (no trigram data) — can't filter, add all its files.
			for _, f := range idx.Files {
				allCandidates[f.Path] = struct{}{}
			}
			continue
		}
		// candidates may be empty (no matches in this index) — that's fine,
		// we just don't add any files from it.
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
// within a single index. A missing trigram means no file in this index can
// match, so we return an empty (non-nil) slice. Returns nil only when the
// index has no trigram data at all (empty index).
func queryIndex(d *IndexData, queryTrigrams []Trigram) []uint32 {
	if len(d.Trigrams) == 0 {
		return nil
	}

	// For each query trigram, find its posting list via binary search.
	var lists [][]uint32
	for _, qt := range queryTrigrams {
		te := findTrigram(d.Trigrams, qt)
		if te == nil {
			// Trigram absent — no file in this index contains all query trigrams.
			return []uint32{}
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
	// Check across all indices for the latest git mtime
	var latest int64
	for _, d := range loadAllIndices(edrDir) {
		if d.Header.GitMtime > latest {
			latest = d.Header.GitMtime
		}
	}
	return staleness(repoRoot, latest)
}

// staleness compares a stored git mtime against the current .git/index mtime.
func staleness(repoRoot string, storedMtime int64) bool {
	if storedMtime == 0 {
		return true // no index data
	}
	return gitIndexMtime(repoRoot) != storedMtime
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
	if !acquireLock(lockPath) {
		return
	}
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

	// Count files across all indices (main + journals) using deduplicated paths,
	// matching what Query actually sees.
	allFiles := make(map[string]struct{})
	var latestGitMtime int64

	if info, err := os.Stat(mainPath); err == nil {
		s.Exists = true
		s.SizeBytes = info.Size()
		if data, err := os.ReadFile(mainPath); err == nil {
			if d, err := Unmarshal(data); err == nil {
				s.Trigrams = int(d.Header.NumTrigrams)
				latestGitMtime = d.Header.GitMtime
				for _, f := range d.Files {
					allFiles[f.Path] = struct{}{}
				}
			}
		}
	}

	// Journals — always count their files (they may cover files not in main)
	jnls, _ := filepath.Glob(filepath.Join(edrDir, JournalPfx+"*"+JournalSfx))
	s.Journals = len(jnls)
	for _, j := range jnls {
		if info, err := os.Stat(j); err == nil {
			s.SizeBytes += info.Size()
			if data, err := os.ReadFile(j); err == nil {
				if d, err := Unmarshal(data); err == nil {
					s.Exists = true
					if d.Header.GitMtime > latestGitMtime {
						latestGitMtime = d.Header.GitMtime
					}
					for _, f := range d.Files {
						allFiles[f.Path] = struct{}{}
					}
				}
			}
		}
	}

	s.Files = len(allFiles)
	s.GitMtime = latestGitMtime

	// Staleness: check across all indices, not just main
	s.Stale = staleness(repoRoot, latestGitMtime)
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

// acquireLock tries to create a lockfile exclusively. If it already exists,
// checks if the owning PID is still alive. Removes stale locks from crashed processes.
func acquireLock(path string) bool {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		// Got the lock — write our PID
		fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), time.Now().UnixNano())
		f.Close()
		return true
	}
	// Lock exists — check if owner is still alive
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var pid int
	var ts int64
	if n, _ := fmt.Sscanf(string(data), "%d\n%d\n", &pid, &ts); n == 2 {
		// Stale if PID is dead or lock is older than 5 minutes
		if !processAlive(pid) || time.Since(time.Unix(0, ts)) > 5*time.Minute {
			os.Remove(path)
			// Retry once
			f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err == nil {
				fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), time.Now().UnixNano())
				f.Close()
				return true
			}
		}
	}
	return false
}

// processAlive checks if a process with the given PID exists.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds. Signal 0 tests existence.
	return p.Signal(syscall.Signal(0)) == nil
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

// IncrementalTick catches up the trigram index on each invocation.
// Re-indexes all stale files and indexes all new files in one pass.
// Rate-limited to once per 5 seconds to avoid redundant work.
func IncrementalTick(root, edrDir string, walkFn func(root string, fn func(path string) error) error) int {
	// Rate limit: check marker file
	marker := filepath.Join(edrDir, "trigram.tick")
	if info, err := os.Stat(marker); err == nil {
		if time.Since(info.ModTime()) < 5*time.Second {
			return 0
		}
	}
	os.WriteFile(marker, nil, 0600)

	gitMt := gitIndexMtime(root)

	// Re-index stale files
	stale := StaleFiles(root, edrDir)
	indexed := 0
	if len(stale) > 0 {
		n, _ := BuildIncremental(root, edrDir, stale, gitMt)
		indexed += n
	}

	// Index new files
	unindexed, err := UnindexedFiles(root, edrDir, walkFn)
	if err == nil && len(unindexed) > 0 {
		n, _ := BuildIncremental(root, edrDir, unindexed, gitMt)
		indexed += n
	}

	if indexed > 0 {
		MaybeCompact(edrDir, 10)
	}
	return indexed
}

// BuildFullFromWalk builds a complete index by walking the repo.
// progress is called with (indexed, total) after each file. Can be nil.
func BuildFullFromWalk(root, edrDir string, walkFn func(root string, fn func(path string) error) error, progress func(int, int)) error {
	// Enumerate all files
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

	// Read and extract trigrams in parallel. Each goroutine produces a result
	// only for files that are successfully read and not binary.
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
		entry := FileEntry{
			Path:  rel,
			Mtime: info.ModTime().UnixNano(),
			Size:  info.Size(),
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
			resultCh <- fileResult{entry: entry, tris: tris}

			if progress != nil {
				mu.Lock()
				done++
				progress(done, len(paths))
				mu.Unlock()
			}
		}(p, entry)
	}
	go func() { wg.Wait(); close(resultCh) }()

	// Collect results — only successfully indexed files get IDs.
	// Sort by path for deterministic file table order across runs.
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

