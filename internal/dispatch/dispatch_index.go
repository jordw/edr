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
		syms := index.Parse(path, data)
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

	importExtractor := func(relPath string, data []byte) []string {
		ext := strings.ToLower(filepath.Ext(relPath))
		if !hasImportPatterns(ext) {
			return nil
		}
		imports := extractImportsForFile(data, ext)
		if len(imports) == 0 {
			return nil
		}
		raws := make([]string, len(imports))
		for i, imp := range imports {
			raws[i] = imp.Raw
		}
		return raws
	}

	importResolver := func(allFiles []string, imports []idx.ImportRecord, allSymbols []idx.SymbolEntry, symFiles []idx.FileEntry) [][2]string {
		suffixIdx := index.BuildSuffixIndex(allFiles)
		// Build symbol names per file for narrowing.
		symsByFile := make(map[string][]string)
		for _, s := range allSymbols {
			if int(s.FileID) < len(symFiles) {
				rel := symFiles[s.FileID].Path
				symsByFile[rel] = append(symsByFile[rel], s.Name)
			}
		}

		var edges [][2]string
		for _, imp := range imports {
			for _, raw := range imp.Raws {
				resolved := index.ResolveImport(suffixIdx, raw, imp.ImporterRel, imp.Ext)
				if !needsSymbolNarrowing(imp.Ext) || len(resolved) <= 1 {
					for _, target := range resolved {
						edges = append(edges, [2]string{imp.ImporterRel, target})
					}
					continue
				}
				if imp.Idents == nil {
					for _, target := range resolved {
						edges = append(edges, [2]string{imp.ImporterRel, target})
					}
					continue
				}
				matched := false
				for _, target := range resolved {
					for _, sym := range symsByFile[target] {
						if imp.Idents[sym] {
							edges = append(edges, [2]string{imp.ImporterRel, target})
							matched = true
							break
						}
					}
				}
				if !matched && len(resolved) > 0 {
					edges = append(edges, [2]string{imp.ImporterRel, resolved[0]})
				}
			}
		}
		return edges
	}

	err := idx.BuildFullFromWalkWithImports(root, edrDir, index.WalkRepoFiles, nil, symbolExtractor, importExtractor, importResolver)
	if err != nil {
		return nil, err
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
	if idx.HasRefGraph(edrDir) {
		rg := idx.ReadRefGraph(edrDir)
		if rg != nil {
			result["ref_edges"] = len(rg.Edges)
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
		".rs", ".rb", ".java", ".cs", ".swift", ".php":
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
	case ".cs":
		r := index.ParseCSharp(data)
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
	case ".swift":
		r := index.ParseSwift(data)
		out := make([]index.ImportEntry, len(r.Imports))
		for i, imp := range r.Imports {
			out[i] = index.ImportEntry{Raw: imp.Path}
		}
		return out
	case ".php":
		r := index.ParsePHP(data)
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
