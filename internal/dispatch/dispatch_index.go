package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
		return entries
	}
	// Collect imports from every file via the onFile hook.
	// This fires for all files including reused ones (no second walk needed).
	var importMu sync.Mutex
	var rawImports [][2]string
	type rawImp struct {
		importerRel string
		raw         string
		ext         string
	}
	var collectedImports []rawImp
	// Cache importer source content for symbol narrowing
	importerContent := make(map[string][]byte) // rel path → first 8KB
	var contentMu sync.Mutex

	onFile := func(path string) {
		ext := strings.ToLower(filepath.Ext(path))
		// Skip extensions we don't have import patterns for
		if !hasImportPatterns(ext) {
			return
		}
		// Read only first 8KB — imports are at the top of the file
		f, err := os.Open(path)
		if err != nil {
			return
		}
		data := make([]byte, 8192)
		n, _ := f.Read(data)
		f.Close()
		if n == 0 {
			return
		}
		data = data[:n]
		imports := index.ExtractImports(data, ext)
		if len(imports) > 0 {
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return
			}
			importMu.Lock()
			for _, imp := range imports {
				collectedImports = append(collectedImports, rawImp{rel, imp.Raw, ext})
			}
			importMu.Unlock()
			// Cache content for symbol narrowing (package-level imports)
			if needsSymbolNarrowing(ext) {
				contentMu.Lock()
				importerContent[rel] = data
				contentMu.Unlock()
			}
		}
	}

	err := idx.BuildFullFromWalkWithHook(root, edrDir, index.WalkRepoFiles, nil, onFile, symbolExtractor)
	if err != nil {
		return nil, err
	}

	// Resolve imports and build graph
	if len(collectedImports) > 0 {
		indexed := idx.IndexedPaths(edrDir)
		var allFiles []string
		for rel := range indexed {
			allFiles = append(allFiles, rel)
		}
		sort.Strings(allFiles)
		suffixIdx := index.BuildSuffixIndex(allFiles)

		// Load symbols per file for narrowing package-level imports.
		// For Go/Java/Python, only create edges to files whose symbols
		// are actually referenced in the importer's source.
		allSyms, symFiles := idx.LoadAllSymbols(edrDir)
		symsByFile := make(map[string][]string) // rel path → symbol names
		if allSyms != nil {
			for _, s := range allSyms {
				if int(s.FileID) < len(symFiles) {
					rel := symFiles[s.FileID].Path
					symsByFile[rel] = append(symsByFile[rel], s.Name)
				}
			}
		}

		for _, imp := range collectedImports {
			resolved := index.ResolveImport(suffixIdx, imp.raw, imp.importerRel, imp.ext)
			if !needsSymbolNarrowing(imp.ext) || len(resolved) <= 1 {
				// File-level import (C, TS, Ruby) or single target — keep as-is
				for _, target := range resolved {
					rawImports = append(rawImports, [2]string{imp.importerRel, target})
				}
				continue
			}
			// Package-level import: narrow to files whose symbols appear in importer
			src := importerContent[imp.importerRel]
			if src == nil {
				// No cached content — keep all edges as fallback
				for _, target := range resolved {
					rawImports = append(rawImports, [2]string{imp.importerRel, target})
				}
				continue
			}
			srcStr := string(src)
			matched := false
			for _, target := range resolved {
				for _, sym := range symsByFile[target] {
					if len(sym) >= 2 && strings.Contains(srcStr, sym) {
						rawImports = append(rawImports, [2]string{imp.importerRel, target})
						matched = true
						break
					}
				}
			}
			if !matched {
				// No symbol matches — keep first file as fallback (likely the package entry point)
				if len(resolved) > 0 {
					rawImports = append(rawImports, [2]string{imp.importerRel, resolved[0]})
				}
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
// needsSymbolNarrowing returns true for languages with package-level imports
// (Go, Java, Python) where one import resolves to multiple files.
func needsSymbolNarrowing(ext string) bool {
	switch ext {
	case ".go", ".java", ".py":
		return true
	}
	return false
}

// hasImportPatterns returns true if we have import extraction for this extension.
func hasImportPatterns(ext string) bool {
	switch ext {
	case ".c", ".h", ".cc", ".cpp", ".hpp",
		".go", ".py", ".js", ".ts", ".tsx", ".jsx",
		".rs", ".rb", ".java":
		return true
	}
	return false
}

// extractImportsForFile uses hand-written parsers for languages that have
// them, falling back to regex-based ExtractImports for the rest. Returns
// []ImportEntry for compatibility with ResolveImport.
func extractImportsForFile(data []byte, ext string) []index.ImportEntry {
	switch ext {
	case ".rb":
		r := index.ParseRuby(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	case ".ts", ".tsx", ".mts", ".cts":
		r := index.ParseTS(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	case ".go":
		r := index.ParseGo(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	case ".py", ".pyi":
		r := index.ParsePython(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	case ".rs":
		r := index.ParseRust(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	case ".java":
		r := index.ParseJava(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	case ".c", ".h", ".cc", ".cpp", ".cxx", ".hpp", ".hxx", ".hh":
		r := index.ParseCpp(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	default:
		return index.ExtractImports(data, ext)
	}
}

func extractAllImports(root string) ([][2]string, []string) {
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
				ext := strings.ToLower(filepath.Ext(rel))
				imports := extractImportsForFile(data, ext)
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
