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
	scopestore "github.com/jordw/edr/internal/scope/store"
	"github.com/jordw/edr/internal/walk"
)

// runIndex handles "edr index" and "edr index --status".
func runIndex(_ context.Context, db index.SymbolStore, root string, _ []string, flags map[string]any) (any, error) {
	edrDir := db.EdrDir()

	if flagBool(flags, "status", false) {
		rep := idx.NewReporter(root, edrDir).Status()

		// Use index file count when available, fall back to walk.
		total := rep.Files
		if !rep.Exists || rep.Stale {
			total = 0
			walk.RepoFiles(root, func(_ string) error {
				total++
				return nil
			})
		}
		result := map[string]any{
			"status": "ok",
			"mode":   "status",
		}
		if rep.Exists {
			result["files_indexed"] = rep.Files
			result["files_total"] = total
			if tri, ok := rep.Extra["trigrams"].(int); ok {
				result["trigrams"] = tri
			}
			result["size_bytes"] = rep.Bytes
			result["stale"] = rep.Stale
			if syms, ok := rep.Extra["symbols"].(int); ok {
				result["symbols"] = syms
			}
			if total > 0 {
				result["coverage"] = fmt.Sprintf("%.0f%%", float64(rep.Files)/float64(total)*100)
			}
		} else {
			result["files_indexed"] = 0
			result["files_total"] = total
			result["coverage"] = "0%"
		}
		return result, nil
	}

	symbolExtractor := DefaultSymbolExtractor()

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

	err := idx.BuildFullFromWalkWithImports(root, edrDir, walk.RepoFiles, nil, symbolExtractor, importExtractor, importResolver)
	if err != nil {
		return nil, err
	}
	idx.InvalidateSymbolCache()

	// Scope index build: per-file scope.Result data for supported
	// languages (Go, TS/JS/TSX/JSX, Python). Used by refs-to and rename.
	// Separate from the trigram+symbol index
	// so its format can evolve independently.
	scopeFiles, scopeErr := scopestore.Build(root, edrDir, walk.RepoFiles)

	rep := idx.NewReporter(root, edrDir).Status()
	result := map[string]any{
		"status":        "built",
		"files_indexed": rep.Files,
		"size_bytes":    rep.Bytes,
	}
	if tri, ok := rep.Extra["trigrams"].(int); ok {
		result["trigrams"] = tri
	}
	if scopeErr == nil {
		result["scope_files"] = scopeFiles
	} else {
		result["scope_error"] = scopeErr.Error()
	}
	if syms, ok := rep.Extra["symbols"].(int); ok {
		result["symbols"] = syms
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
			result["ref_names"] = len(rg.ForwardNames)
			result["ref_inv_entries"] = len(rg.InvEntries)
		}
	}
	return result, nil
}

// needsSymbolNarrowing returns true for languages where one import path
// resolves to multiple files but the importer only uses symbols from some.
// Go is excluded: Go imports a package (= directory), so all files in the
// package should get edges — narrowing drops canonical definition files
// like types.go when the importer uses few symbols.
func needsSymbolNarrowing(ext string) bool {
	switch ext {
	case ".java", ".py":
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
	walk.RepoFiles(root, func(path string) error {
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
