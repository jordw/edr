package idx

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	atomicio "github.com/jordw/edr/internal/atomic"
	"github.com/jordw/edr/internal/staleness"
)

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

// extractSymIdents tokenizes each symbol body into a set of identifiers.
// Returns a slice parallel to syms — idents[i] contains tokens from syms[i].
func extractSymIdents(data []byte, syms []SymbolEntry) [][]string {
	idents := make([][]string, len(syms))
	for i, s := range syms {
		if int(s.EndByte) > len(data) || s.StartByte >= s.EndByte {
			continue
		}
		all := tokenizeIdents(data[s.StartByte:s.EndByte])
		// A symbol isn't a caller of itself. The declaration line
		// (e.g. "func Hello() string {") contains the symbol's own
		// name, which would otherwise produce a spurious self-edge in
		// the ref graph — hiding real callers when the fallback path
		// gates on "ref graph returned zero callers". Filter is
		// case-insensitive because NameHash is case-insensitive;
		// literal text like "hello" in a string would otherwise hash
		// to the same bucket as the symbol name "Hello".
		if s.Name != "" && len(all) > 0 {
			nameLower := strings.ToLower(s.Name)
			n := 0
			for _, id := range all {
				if !strings.EqualFold(id, nameLower) {
					all[n] = id
					n++
				}
			}
			all = all[:n]
		}
		idents[i] = all
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
		entry     FileEntry
		tris      []Trigram
		syms      []SymbolEntry
		raws      []string   // raw import strings (nil if no import extraction)
		symIdents [][]string // identifier tokens per symbol (parallel to syms)
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
		// Only reuse symbols if the old index actually had them (prior trigram-only builds can leave gaps).
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
		staleness.OpenTracker(edrDir, DirtyTrackerName).Clear()
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
	if err := atomicio.WriteFile(filepath.Join(edrDir, MainFile), d.Marshal()); err != nil {
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

	staleness.OpenTracker(edrDir, DirtyTrackerName).Clear()
	return nil
}
