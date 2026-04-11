package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
)

// runIndex handles "edr index" and "edr index --status".
func runIndex(_ context.Context, db index.SymbolStore, root string, _ []string, flags map[string]any) (any, error) {
	edrDir := db.EdrDir()

	if flagBool(flags, "status", false) {
		s := idx.GetStatus(root, edrDir)

		// Use index file count when available, fall back to walk.
		total := s.Files
		if !s.Exists || s.Stale {
			total = 0
			index.WalkRepoFiles(root, func(_ string) error {
				total++
				return nil
			})
		}
		result := map[string]any{
			"status": "ok",
			"mode":   "status",
		}
		if s.Exists {
			result["files_indexed"] = s.Files
			result["files_total"] = total
			result["trigrams"] = s.Trigrams
			result["size_bytes"] = s.SizeBytes
			result["stale"] = s.Stale
			if h, err := idx.ReadHeader(edrDir); err == nil && h.NumSymbols > 0 {
				result["symbols"] = int(h.NumSymbols)
			}
			if total > 0 {
				result["coverage"] = fmt.Sprintf("%.0f%%", float64(s.Files)/float64(total)*100)
			}
		} else {
			result["files_indexed"] = 0
			result["files_total"] = total
			result["coverage"] = "0%"
		}
		return result, nil
	}

	// Collect imports during the symbol extraction walk (no second pass).
	var importMu sync.Mutex
	var rawImports [][2]string // (importer_rel, raw_import_string, ext)
	type rawImp struct {
		importerRel string
		raw         string
		ext         string
	}
	var collectedImports []rawImp

	symbolExtractor := func(path string, data []byte) []idx.SymbolEntry {
		syms := index.RegexParse(path, data)
		entries := make([]idx.SymbolEntry, len(syms))
		for i, s := range syms {
			entries[i] = idx.SymbolEntry{
				Name:      s.Name,
				Kind:      idx.ParseKind(s.Type),
				StartLine: s.StartLine,
				EndLine:   s.EndLine,
				StartByte: s.StartByte,
				EndByte:   s.EndByte,
			}
		}
		// Extract imports from the same file data (piggybacking on the walk)
		ext := filepath.Ext(path)
		imports := index.ExtractImports(data, ext)
		if len(imports) > 0 {
			rel, err := filepath.Rel(root, path)
			if err == nil {
				importMu.Lock()
				for _, imp := range imports {
					collectedImports = append(collectedImports, rawImp{rel, imp.Raw, ext})
				}
				importMu.Unlock()
			}
		}
		return entries
	}
	err := idx.BuildFullFromWalk(root, edrDir, index.WalkRepoFiles, nil, symbolExtractor)
	if err != nil {
		return nil, err
	}

	// Resolve imports and build graph (no file I/O — just resolution)
	if len(collectedImports) > 0 {
		indexed := idx.IndexedPaths(edrDir)
		var allFiles []string
		for rel := range indexed {
			allFiles = append(allFiles, rel)
		}
		sort.Strings(allFiles)
		suffixIdx := index.BuildSuffixIndex(allFiles)

		for _, imp := range collectedImports {
			resolved := index.ResolveImport(suffixIdx, imp.raw, imp.importerRel, imp.ext)
			for _, target := range resolved {
				rawImports = append(rawImports, [2]string{imp.importerRel, target})
			}
		}

		if len(rawImports) > 0 {
			graph := idx.BuildImportGraph(allFiles, rawImports)
			idx.WriteImportGraph(edrDir, graph)
		}
	}

	s := idx.GetStatus(root, edrDir)
	result := map[string]any{
		"status":        "built",
		"files_indexed": s.Files,
		"trigrams":      s.Trigrams,
		"size_bytes":    s.SizeBytes,
	}
	if h, err := idx.ReadHeader(edrDir); err == nil && h.NumSymbols > 0 {
		result["symbols"] = int(h.NumSymbols)
	}
	if idx.HasImportGraph(edrDir) {
		graph := idx.ReadImportGraph(edrDir)
		if graph != nil {
			result["import_edges"] = len(graph.Edges)
		}
	}
	return result, nil
}

// buildImportGraph is no longer used — imports are extracted during BuildFullFromWalk.
// Kept as dead code reference until confirmed removable.
func _unused_buildImportGraph(root string) ([][2]string, []string) {
	// Collect all file paths
	var allFiles []string
	index.WalkRepoFiles(root, func(path string) error {
		rel, err := filepath.Rel(root, path)
		if err == nil {
			allFiles = append(allFiles, rel)
		}
		return nil
	})
	sort.Strings(allFiles)

	// Build suffix index for import resolution
	suffixIdx := index.BuildSuffixIndex(allFiles)

	// Extract imports from each file and resolve
	type fileImport struct {
		importer string
		imported []string
	}

	var mu sync.Mutex
	var edges [][2]string

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	ch := make(chan string, workers*4)

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for rel := range ch {
				abs := filepath.Join(root, rel)
				data, err := os.ReadFile(abs)
				if err != nil {
					continue
				}
				ext := filepath.Ext(rel)
				imports := index.ExtractImports(data, ext)
				if len(imports) == 0 {
					continue
				}
				var localEdges [][2]string
				for _, imp := range imports {
					resolved := index.ResolveImport(suffixIdx, imp.Raw, rel, ext)
					for _, target := range resolved {
						localEdges = append(localEdges, [2]string{rel, target})
					}
				}
				if len(localEdges) > 0 {
					mu.Lock()
					edges = append(edges, localEdges...)
					mu.Unlock()
				}
			}
		}()
	}

	for _, f := range allFiles {
		ch <- f
	}
	close(ch)
	wg.Wait()

	return edges, allFiles
}
