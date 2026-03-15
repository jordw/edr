package dispatch

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runReadFile(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("read-file requires 1-3 arguments: <file> [start-line] [end-line]")
	}
	budget := flagInt(flags, "budget", 0)

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	// --signatures: file-level signatures view
	if flagBool(flags, "signatures", false) {
		// For Go files, use grouped signatures (types + receiver methods)
		var body string
		var sigErr error
		allSyms, symErr := db.GetSymbolsByFile(ctx, file)
		if symErr == nil && len(allSyms) > 0 {
			if grouped := index.GoFileSignatures(file, allSyms); grouped != "" {
				body = grouped
			}
		}
		if body == "" {
			body, sigErr = index.OutlineFile(file, 1)
		}
		if sigErr == nil {
			size := len(body) / 4
			truncated := false
			if budget > 0 && size > budget {
				chars := budget * 4
				body, truncated = output.TruncateAtLine(body, chars)
				size = budget
			}
			hash, _ := edit.FileHash(file)
			return map[string]any{
				"file":       output.Rel(file),
				"signatures": true,
				"size":       size,
				"content":    body,
				"hash":       hash,
				"truncated":  truncated,
				"mtime":      fileMtime(file),
			}, nil
		}
		// Fall through to normal read for non-code files
	}

	// --depth N: AST-aware progressive disclosure for the whole file
	depth := flagInt(flags, "depth", 0)
	if depth > 2 {
		depth = 2
	}
	if depth > 0 {
		body, outlineErr := index.OutlineFile(file, depth)
		if outlineErr == nil {
			size := len(body) / 4
			truncated := false
			if budget > 0 && size > budget {
				chars := budget * 4
				body, truncated = output.TruncateAtLine(body, chars)
				size = budget
			}
			hash, _ := edit.FileHash(file)
			return map[string]any{
				"file":      output.Rel(file),
				"depth":     depth,
				"size":      size,
				"content":   body,
				"hash":      hash,
				"truncated": truncated,
				"mtime":     fileMtime(file),
			}, nil
		}
		// Fall through to normal read if outline fails (unsupported language etc.)
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	content := string(data)
	lines := strings.SplitAfter(content, "\n")
	totalLines := len(lines)

	startLine := 1
	endLine := totalLines
	if len(args) >= 2 {
		fmt.Sscanf(args[1], "%d", &startLine)
	}
	if len(args) >= 3 {
		fmt.Sscanf(args[2], "%d", &endLine)
	}
	if startLine < 1 {
		startLine = 1
	}
	if endLine > totalLines {
		endLine = totalLines
	}
	if startLine > endLine {
		return nil, fmt.Errorf("start line %d is beyond end line %d (file has %d lines)", startLine, endLine, totalLines)
	}

	var numbered strings.Builder
	for i, line := range lines[startLine-1 : endLine] {
		fmt.Fprintf(&numbered, "%d\t%s", startLine+i, line)
	}
	body := numbered.String()

	// Budget is based on raw content size (line numbers are overhead)
	rawSize := 0
	for _, line := range lines[startLine-1 : endLine] {
		rawSize += len(line)
	}
	size := rawSize / 4
	truncated := false
	if budget > 0 && size > budget {
		chars := budget * 4
		body, truncated = output.TruncateAtLine(body, chars)
		size = budget
	}

	hash, _ := edit.FileHash(file)
	result := map[string]any{
		"file":        output.Rel(file),
		"lines":       [2]int{startLine, endLine},
		"total_lines": totalLines,
		"size":        size,
		"content":     body,
		"hash":        hash,
		"truncated":   truncated,
		"mtime":       fileMtime(file),
	}

	if flagBool(flags, "symbols", false) {
		syms, err := db.GetSymbolsByFile(ctx, file)
		if err == nil && len(syms) > 0 {
			var symList []output.Symbol
			for _, s := range syms {
				symList = append(symList, output.Symbol{
					Type:  s.Type,
					Name:  s.Name,
					Lines: [2]int{int(s.StartLine), int(s.EndLine)},
					Size:  int(s.EndByte-s.StartByte) / 4,
				})
			}
			result["symbols"] = symList
		}
	}

	return result, nil
}

func runReadSymbol(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("read-symbol requires 1-2 arguments: [file] <symbol>")
	}
	budget := flagInt(flags, "budget", 0)

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	// --signatures on a container: return compact stub instead of full body
	if flagBool(flags, "signatures", false) && containerTypes[sym.Type] {
		allSyms, _ := db.GetSymbolsByFile(ctx, sym.File)
		stub := index.ExtractContainerStub(*sym, allSyms)
		hash, _ := edit.FileHash(sym.File)
		osym := toOutputSymbol(sym, hash)
		osym.Size = len(stub) / 4
		return output.ExpandResult{
			Symbol: osym,
			Body:   stub,
		}, nil
	}

	// --signatures on a non-container: fall back to signature-only (depth 1)
	if flagBool(flags, "signatures", false) {
		body, err := index.OutlineSymbol(sym.File, *sym, 1)
		if err != nil {
			return nil, err
		}
		hash, _ := edit.FileHash(sym.File)
		osym := toOutputSymbol(sym, hash)
		osym.Size = len(body) / 4
		return output.ExpandResult{
			Symbol: osym,
			Body:   body,
		}, nil
	}

	// --depth N: progressive disclosure via AST-aware collapsing
	depth := flagInt(flags, "depth", 0)
	if depth > 2 {
		depth = 2
	}
	if depth > 0 {
		body, err := index.OutlineSymbol(sym.File, *sym, depth)
		if err != nil {
			return nil, err
		}
		hash, _ := edit.FileHash(sym.File)
		osym := toOutputSymbol(sym, hash)
		osym.Size = len(body) / 4
		return output.ExpandResult{
			Symbol: osym,
			Body:   body,
		}, nil
	}

	src, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, err
	}

	body := string(src[sym.StartByte:sym.EndByte])
	size := len(body) / 4

	truncated := false
	if budget > 0 && size > budget {
		chars := budget * 4
		body, truncated = output.TruncateAtLine(body, chars)
	}

	hash, _ := edit.FileHash(sym.File)
	osym := toOutputSymbol(sym, hash)
	osym.Size = size
	return output.ExpandResult{
		Truncated: truncated,
		Symbol:    osym,
		Body:      body,
	}, nil
}

func runBatchRead(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("batch-read requires at least 1 argument: <file-or-file:symbol> ...")
	}
	budget := flagInt(flags, "budget", 0)
	perFile := 0
	if budget > 0 {
		perFile = budget / len(args)
		if perFile < 1 {
			perFile = 1
		}
	}

	type batchEntry struct {
		File    string          `json:"file"`
		Symbol  string          `json:"symbol,omitempty"`
		OK      bool            `json:"ok"`
		Error   string          `json:"error,omitempty"`
		Content string          `json:"content,omitempty"`
		Hash    string          `json:"hash,omitempty"`
		Lines   [2]int          `json:"lines,omitempty"`
		Size    int             `json:"size,omitempty"`
		Symbols []output.Symbol `json:"symbols,omitempty"`
	}

	showSymbols := flagBool(flags, "symbols", false)
	var results []batchEntry

	for _, arg := range args {
		var filePath, symName string
		if idx := strings.LastIndex(arg, ":"); idx > 0 {
			filePath = arg[:idx]
			symName = arg[idx+1:]
		} else {
			filePath = arg
		}

		if symName != "" {
			// Read a specific symbol
			sym, err := resolveSymbolArgs(ctx, db, root, []string{filePath, symName})
			if err != nil {
				results = append(results, batchEntry{File: filePath, Symbol: symName, OK: false, Error: err.Error()})
				continue
			}
			src, err := os.ReadFile(sym.File)
			if err != nil {
				results = append(results, batchEntry{File: filePath, Symbol: symName, OK: false, Error: err.Error()})
				continue
			}
			body := string(src[sym.StartByte:sym.EndByte])
			size := len(body) / 4
			if perFile > 0 && size > perFile {
				chars := perFile * 4
				body, _ = output.TruncateAtLine(body, chars)
			}
			hash, _ := edit.FileHash(sym.File)
			results = append(results, batchEntry{
				File:    output.Rel(sym.File),
				Symbol:  symName,
				OK:      true,
				Content: body,
				Hash:    hash,
				Lines:   [2]int{int(sym.StartLine), int(sym.EndLine)},
				Size:    size,
			})
		} else {
			// Read entire file
			file, err := db.ResolvePath(filePath)
			if err != nil {
				results = append(results, batchEntry{File: filePath, OK: false, Error: err.Error()})
				continue
			}
			data, err := os.ReadFile(file)
			if err != nil {
				results = append(results, batchEntry{File: filePath, OK: false, Error: err.Error()})
				continue
			}
			body := string(data)
			lines := strings.SplitAfter(body, "\n")
			size := len(body) / 4
			if perFile > 0 && size > perFile {
				chars := perFile * 4
				body, _ = output.TruncateAtLine(body, chars)
			}
			hash, _ := edit.FileHash(file)
			entry := batchEntry{
				File:    output.Rel(file),
				OK:      true,
				Content: body,
				Hash:    hash,
				Lines:   [2]int{1, len(lines)},
				Size:    size,
			}
			if showSymbols {
				syms, err := db.GetSymbolsByFile(ctx, file)
				if err == nil {
					symTokens := 0
					for _, s := range syms {
						symSize := len(s.Name)/4 + 5 // rough token estimate per symbol entry
						symTokens += symSize
						entry.Symbols = append(entry.Symbols, output.Symbol{
							Type:  s.Type,
							Name:  s.Name,
							Lines: [2]int{int(s.StartLine), int(s.EndLine)},
							Size:  int(s.EndByte-s.StartByte) / 4,
						})
					}
					// When budget is tight and symbols are requested, count symbols toward budget
					if perFile > 0 && symTokens+size > perFile*2 {
						// Content was large; strip it and keep only symbols as the summary
						if len(entry.Content) > perFile*4 {
							entry.Content, _ = output.TruncateAtLine(entry.Content, perFile*2)
						}
					}
				}
			}
			results = append(results, entry)
		}
	}

	return results, nil
}

func runSymbols(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("symbols requires 1 argument: <file>")
	}
	file, err := db.ResolvePath(args[0])
	if err != nil {
		return nil, err
	}

	syms, err := db.GetSymbolsByFile(ctx, file)
	if err != nil {
		return nil, err
	}

	// If no symbols found, check if the file is unindexed (e.g. gitignored)
	if len(syms) == 0 {
		if hash, _ := db.GetFileHash(ctx, file); hash == "" {
			if _, statErr := os.Stat(file); statErr == nil {
				return map[string]any{
					"file":    output.Rel(file),
					"symbols": nil,
					"hint":    "file exists but is not indexed (it may be gitignored)",
				}, nil
			}
		}
	}

	// Apply filters consistent with repo-map
	typeFilter := flagString(flags, "type", "")
	grepFilter := flagString(flags, "grep", "")
	hideLocals := !flagBool(flags, "locals", false)

	// Build container spans for locals filtering (same logic as RepoMap).
	// Only function/method/variable spans hide their contents.
	// Class/struct/interface members are public API, not locals.
	type span struct{ start, end uint32 }
	var containerSpans []span
	if hideLocals {
		for _, s := range syms {
			if s.StartLine >= s.EndLine {
				continue
			}
			switch s.Type {
			case "function", "method", "variable":
				containerSpans = append(containerSpans, span{s.StartLine, s.EndLine})
			}
		}
	}

	// Detect duplicate names for disambiguation
	nameCounts := make(map[string]int)
	for _, s := range syms {
		nameCounts[s.Name]++
	}

	var results []output.Symbol
	for _, s := range syms {
		if typeFilter != "" && !strings.EqualFold(s.Type, typeFilter) {
			continue
		}
		if grepFilter != "" && !strings.Contains(strings.ToLower(s.Name), strings.ToLower(grepFilter)) {
			continue
		}
		if hideLocals {
			isLocal := false
			for _, cs := range containerSpans {
				if s.StartLine > cs.start && s.EndLine <= cs.end {
					isLocal = true
					break
				}
			}
			if isLocal {
				continue
			}
		}
		sym := output.Symbol{
			Type:  s.Type,
			Name:  s.Name,
			File:  output.Rel(s.File),
			Lines: [2]int{int(s.StartLine), int(s.EndLine)},
			Size:  int(s.EndByte-s.StartByte) / 4,
		}
		if nameCounts[s.Name] > 1 {
			sym.Qualifier = fmt.Sprintf("line %d", s.StartLine)
		}
		results = append(results, sym)
	}
	return map[string]any{
		"file":    output.Rel(file),
		"symbols": results,
	}, nil
}


// fileMtime returns the file modification time as an ISO 8601 string, or empty on error.
func fileMtime(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fi.ModTime().Format(time.RFC3339)
}

