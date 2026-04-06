package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// relatedSym is a compact symbol reference with signature, used in expand/prepare output.
type relatedSym struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Signature string `json:"signature"`
}

// isBinaryFile sniffs the first 512 bytes for NUL characters.
func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	for _, b := range buf[:n] {
		if b == 0 {
			return true
		}
	}
	return false
}

// grepMatch returns true if name matches the grep pattern.
// Tries regex first (to support alternation like "error|warn");
// falls back to case-insensitive substring if the pattern is not valid regex.
func grepMatch(name, pattern string) bool {
	re, err := regexp.Compile("(?i)(?:" + pattern + ")")
	if err != nil {
		return strings.Contains(strings.ToLower(name), strings.ToLower(pattern))
	}
	return re.MatchString(name)
}

func runReadFile(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("read-file requires 1-3 arguments: <file> [start-line] [end-line]")
	}
	budget := flagInt(flags, "budget", 0)
	full := flagBool(flags, "full", false)

	// Default budget for full-file reads: 4000 tokens.
	// Overridden by explicit --budget or --full.
	// Line-range reads (start_line/end_line) are excluded — explicit range = explicit intent.
	hasLineRange := flagInt(flags, "start_line", 0) > 0 || flagInt(flags, "end_line", 0) > 0 || len(args) >= 2
	if budget == 0 && !full && !hasLineRange {
		budget = 4000
	}

	file := args[0]
	file, err := db.ResolvePathReadOnly(file)
	if err != nil {
		return nil, err
	}

	// Reject binary files early — dumping raw bytes wastes context.
	if isBinaryFile(file) {
		return map[string]any{
			"file":   output.Rel(file),
			"binary": true,
			"error":  "binary file, not readable as text",
		}, nil
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

	// Auto-skeleton: large files (>200 lines) without explicit --full or line range
	// get skeleton view automatically. This prevents the most common context waste
	// pattern — reading a 500-line file to find one function.
	if !full && !hasLineRange && depth == 0 {
		if lc := fileLineCount(file); lc > 200 {
			body, outlineErr := index.OutlineFile(file, 2)
			if outlineErr == nil {
				size := len(body) / 4
				truncated := false
				if budget > 0 && size > budget {
					chars := budget * 4
					body, truncated = output.TruncateAtLine(body, chars)
					size = budget
				}
				hash, _ := edit.FileHash(file)
				r := map[string]any{
					"file":        output.Rel(file),
					"depth":       2,
					"lines":       [2]int{1, lc},
					"total_lines": lc,
					"size":        size,
					"content":     body,
					"hash":        hash,
					"truncated":   truncated,
					"mtime":       fileMtime(file),
					"auto":        "skeleton",
				}
				setBudgetUsed(r, size)
				return r, nil
			}
		}
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

// addSignatureToResult computes and attaches the symbol's signature to a read result.
// The "_signature" field is internal — stripped before output, consumed by session tracking.
func addSignatureToResult(sym *index.SymbolInfo, src []byte, result map[string]any) {
	if sym == nil || len(src) == 0 {
		return
	}
	sig := index.ExtractSignatureFromSource(*sym, src)
	if sig != "" {
		result["_signature"] = sig
	}
}

func runReadSymbol(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("read-symbol requires 1-2 arguments: [file] <symbol>")
	}
	budget := flagInt(flags, "budget", 0)

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		// Smart resolution: rank ambiguous candidates instead of failing
		var ambErr *index.AmbiguousSymbolError
		if errors.As(err, &ambErr) {
			ranked := rankCandidates(ambErr.Candidates, ambErr.Name, root)
			if shouldAutoResolve(ranked, ambErr.Name) {
				sym = &ranked[0].Symbol
				err = nil
			} else if len(ranked) > 0 {
				return buildShortlist(ranked, ambErr.Name, root), nil
			}
		}
		if err != nil {
			return nil, err
		}
	}

	// --signatures on a container: return compact stub instead of full body
	if flagBool(flags, "signatures", false) && containerTypes[sym.Type] {
		allSyms, _ := db.GetSymbolsByFile(ctx, sym.File)
		stub := index.ExtractContainerStub(*sym, allSyms)
		hash, _ := edit.FileHash(sym.File)
		r := symbolReadResult(sym, stub, hash, map[string]any{"signatures": true})
		r["size"] = len(stub) / 4
		// Record signature for assumption tracking (agent sees the signature)
		src, _ := os.ReadFile(sym.File)
		addSignatureToResult(sym, src, r)
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
	// Record signature for assumption tracking (only for non-truncated reads)
	if !truncated {
		addSignatureToResult(sym, src, r)
	}

	// Auto-expand: symbol reads include dep signatures by default.
	// --expand overrides the mode (deps, callers, both).
	// --no-expand or --expand="" suppresses.
	expandMode := flagString(flags, "expand", "")
	if expandMode == "" && !flagBool(flags, "no_expand", false) {
		// Default: auto-include a compact set of dep signatures
		attachAutoExpand(ctx, db, sym, r)
	} else if expandMode != "" {
		attachExpand(ctx, db, sym, expandMode, r)
	}
	return r, nil
}

func runBatchRead(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
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


func runSymbols(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
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
		if grepFilter != "" && !grepMatch(s.Name, grepFilter) {
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


// findCallersWithFallback tries semantic callers first, falls back to text-based refs.
func findCallersWithFallback(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) []index.SymbolInfo {
	// For large repos, skip the full-repo parse and use same-file callers only.
	// Use index header for fast file count when available.
	files := 0
	if h, err := idx.ReadHeader(db.EdrDir()); err == nil {
		files = int(h.NumFiles)
	} else {
		files, _, _ = db.Stats(ctx)
	}
	if files > 1000 {
		callers, _ := db.FindSameFileCallers(ctx, sym.Name, sym.File)
		return callers
	}

	callers, err := db.FindSemanticCallers(ctx, sym.Name, sym.File)
	if err == nil && len(callers) > 0 {
		return callers
	}
	refs, _ := index.FindReferencesInFile(ctx, db, sym.Name, sym.File)
	allSyms, _ := db.AllSymbols(ctx)
	symMap := make(map[string][]index.SymbolInfo)
	for _, s := range allSyms {
		symMap[s.File] = append(symMap[s.File], s)
	}
	seen := make(map[string]bool)
	for _, ref := range refs {
		if ref.File == sym.File && ref.StartLine >= sym.StartLine && ref.EndLine <= sym.EndLine {
			continue
		}
		for _, s := range symMap[ref.File] {
			if ref.StartLine >= s.StartLine && ref.EndLine <= s.EndLine {
				key := s.File + ":" + s.Name
				if !seen[key] {
					seen[key] = true
					callers = append(callers, s)
				}
			}
		}
	}
	return callers
}

// symbolsToSignatures converts symbols to signature structs for output.
func symbolsToSignatures(ctx context.Context, syms []index.SymbolInfo) []relatedSym {
	var items []relatedSym
	for _, s := range syms {
		sig := index.ExtractSignatureCtx(ctx, s)
		if sig != "" {
			items = append(items, relatedSym{
				File:      output.Rel(s.File),
				Name:      s.Name,
				Type:      s.Type,
				Signature: sig,
			})
		}
	}
	return items
}

// attachExpand adds related symbol signatures to a read result.
// expandMode: "deps" (default if empty/truthy), "callers", or "both".
func attachExpand(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo, expandMode string, result map[string]any) {
	showDeps := true
	showCallers := false
	switch expandMode {
	case "callers":
		showDeps = false
		showCallers = true
	case "both":
		showCallers = true
	case "deps", "true", "1":
		// default: deps only
	default:
		// treat unrecognized as deps
	}

	if showDeps {
		deps, err := index.FindDeps(ctx, db, sym)
		if err == nil && len(deps) > 0 {
			// Cap at 10 to keep response bounded on large repos
			if len(deps) > 10 {
				deps = deps[:10]
			}
			if items := symbolsToSignatures(ctx, deps); len(items) > 0 {
				result["deps"] = items
			}
		}
	}

	if showCallers {
		callers := findCallersWithFallback(ctx, db, sym)
		// Cap at 10
		if len(callers) > 10 {
			callers = callers[:10]
		}
		if items := symbolsToSignatures(ctx, callers); len(items) > 0 {
			result["callers"] = items
		}
	}
}


// attachAutoExpand adds a compact set of dep signatures for auto-expand.
// Unlike explicit --expand which includes all deps, auto-expand:
// - Only includes cross-file deps (same-file deps are visible in sig view)
// - Caps at 5 deps to keep the response compact
func attachAutoExpand(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo, result map[string]any) {
	deps, err := index.FindDeps(ctx, db, sym)
	if err != nil || len(deps) == 0 {
		return
	}

	// Filter to cross-file deps only
	var crossFile []index.SymbolInfo
	for _, d := range deps {
		if d.File != sym.File {
			crossFile = append(crossFile, d)
		}
	}
	if len(crossFile) == 0 {
		return
	}

	// Cap at 5
	if len(crossFile) > 5 {
		crossFile = crossFile[:5]
	}

	if items := symbolsToSignatures(ctx, crossFile); len(items) > 0 {
		result["deps"] = items
	}
}


