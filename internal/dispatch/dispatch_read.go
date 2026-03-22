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
	full := flagBool(flags, "full", false)

	// Default budget for full-file reads: 2000 tokens.
	// Overridden by explicit --budget or --full.
	// Line-range reads (start_line/end_line) are excluded — explicit range = explicit intent.
	hasLineRange := flagInt(flags, "start_line", 0) > 0 || flagInt(flags, "end_line", 0) > 0 || len(args) >= 2
	if budget == 0 && !full && !hasLineRange {
		budget = 2000
	}

	file := args[0]
	file, err := db.ResolvePathReadOnly(file)
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
			totalLines := fileLineCount(file)
			r := map[string]any{
				"file":        output.Rel(file),
				"signatures":  true,
				"lines":       [2]int{1, totalLines},
				"total_lines": totalLines,
				"size":        size,
				"content":     body,
				"hash":        hash,
				"truncated":   truncated,
				"mtime":       fileMtime(file),
			}
			setBudgetUsed(r, size)
			return r, nil
		}
		// Fall through to normal read for non-code files
	}

	// --skeleton or --depth: AST-aware progressive disclosure
	depth := flagInt(flags, "depth", 0)
	if flagBool(flags, "skeleton", false) {
		depth = 2
	}
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
			totalLines := fileLineCount(file)
			r := map[string]any{
				"file":        output.Rel(file),
				"depth":       depth,
				"lines":       [2]int{1, totalLines},
				"total_lines": totalLines,
				"size":        size,
				"content":     body,
				"hash":        hash,
				"truncated":   truncated,
				"mtime":       fileMtime(file),
			}
			setBudgetUsed(r, size)
			return r, nil
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
	// --lines flag (parsed into start_line/end_line by runReadUnified)
	if sl := flagInt(flags, "start_line", 0); sl > 0 {
		startLine = sl
	}
	if el := flagInt(flags, "end_line", 0); el > 0 {
		endLine = el
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

	body := strings.Join(lines[startLine-1:endLine], "")

	// Collapse license headers and import blocks for full-file reads
	if !full && !hasLineRange {
		body = collapseBoilerplate(body, file)
	}

	size := len(body) / 4
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
	setBudgetUsed(result, size)

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

// symbolReadResult builds a flat result map for symbol reads.
// Shared fields (file, hash, lines) are at top level only.
// The "symbol" sub-object contains only name and type metadata.
func symbolReadResult(sym *index.SymbolInfo, content string, hash string, extra map[string]any) map[string]any {
	size := len(content) / 4
	result := map[string]any{
		"file":      output.Rel(sym.File),
		"hash":      hash,
		"lines":     [2]int{int(sym.StartLine), int(sym.EndLine)},
		"content":   content,
		"size":      size,
		"truncated": false,
		"symbol":    sym.Name,
	}
	for k, v := range extra {
		result[k] = v
	}
	return result
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
		r := symbolReadResult(sym, stub, hash, map[string]any{"signatures": true})
		r["size"] = len(stub) / 4
		return r, nil
	}

	// --signatures on a non-container: return error
	if flagBool(flags, "signatures", false) {
		return nil, fmt.Errorf("%s is a %s, not a container; use --skeleton or read without --signatures", sym.Name, sym.Type)
	}

	// --skeleton or --depth: progressive disclosure via AST-aware collapsing
	depth := flagInt(flags, "depth", 0)
	if flagBool(flags, "skeleton", false) {
		depth = 2
	}
	if depth > 2 {
		depth = 2
	}
	if depth > 0 {
		body, err := index.OutlineSymbol(sym.File, *sym, depth)
		if err != nil {
			return nil, err
		}
		hash, _ := edit.FileHash(sym.File)
		return symbolReadResult(sym, body, hash, nil), nil
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
	r := symbolReadResult(sym, body, hash, nil)
	r["size"] = size
	r["truncated"] = truncated
	setBudgetUsed(r, size)
	return r, nil
}

func runBatchRead(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("batch-read requires at least 1 argument: <file-or-file:symbol> ...")
	}

	// Build individual read commands for each arg
	cmds := make([]MultiCmd, len(args))
	for i, arg := range args {
		readFlags := make(map[string]any)
		// Copy relevant flags
		for _, k := range []string{"signatures", "skeleton", "symbols", "full", "lines", "depth"} {
			if v, ok := flags[k]; ok {
				readFlags[k] = v
			}
		}
		cmds[i] = MultiCmd{Cmd: "read", Args: []string{arg}, Flags: readFlags}
	}

	budget := flagInt(flags, "budget", 0)
	var budgetOpt []int
	if budget > 0 {
		budgetOpt = []int{budget}
	}

	results := DispatchMulti(ctx, db, cmds, budgetOpt...)
	return MultiResults(results), nil
}


func runSymbols(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("symbols requires 1 argument: <file>")
	}
	file, err := db.ResolvePathReadOnly(args[0])
	if err != nil {
		return nil, err
	}

	syms, err := db.GetSymbolsByFile(ctx, file)
	if err != nil {
		return nil, err
	}

	// If no symbols found, check if the file is unindexed or missing
	if len(syms) == 0 {
		if hash, _ := db.GetFileHash(ctx, file); hash == "" {
			if _, statErr := os.Stat(file); statErr != nil {
				return nil, fmt.Errorf("open %s: no such file or directory", output.Rel(file))
			}
			return map[string]any{
				"file":    output.Rel(file),
				"symbols": nil,
				"hint":    "file exists but is not indexed (it may be gitignored)",
			}, nil
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
		"content": []any{
			map[string]any{
				"file":    output.Rel(file),
				"symbols": results,
			},
		},
		"files":         1,
		"shown_files":   1,
		"shown_symbols": len(results),
		"symbols":       len(results),
		"truncated":     false,
	}, nil
}

// setBudgetUsed adds budget_used to a result map when truncation occurred.
func setBudgetUsed(result map[string]any, size int) {
	if trunc, _ := result["truncated"].(bool); trunc {
		result["budget_used"] = size
	}
}

// fileLineCount returns the number of lines in a file (cheap: reads file, counts newlines).
func fileLineCount(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return strings.Count(string(data), "\n") + 1
}

// fileMtime returns the file modification time as an ISO 8601 string, or empty on error.
func fileMtime(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return fi.ModTime().Format(time.RFC3339)
}

