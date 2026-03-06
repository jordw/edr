package dispatch

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/gather"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/search"
)

// resolveSymbolArgs resolves 1 or 2 args to a symbol.
// With 1 arg: global name resolution (errors if ambiguous).
// With 2 args: file + name lookup.
func resolveSymbolArgs(ctx context.Context, db *index.DB, root string, args []string) (*index.SymbolInfo, error) {
	switch len(args) {
	case 1:
		return db.ResolveSymbol(ctx, args[0])
	case 2:
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		return db.GetSymbol(ctx, file, args[1])
	default:
		return nil, fmt.Errorf("expected 1 or 2 arguments: [file] <symbol>")
	}
}

// Dispatch routes a command name to the appropriate internal handler and
// returns the result. It reuses the same logic as the cobra commands but
// bypasses the CLI layer so callers can invoke commands programmatically.
func Dispatch(ctx context.Context, db *index.DB, cmd string, args []string, flags map[string]any) (any, error) {
	root := db.Root()
	output.SetRoot(root)

	switch cmd {
	case "init":
		return runInit(ctx, db)
	case "repo-map":
		return runRepoMap(ctx, db, flags)
	case "search":
		return runSearch(ctx, db, args, flags)
	case "search-text":
		return runSearchText(ctx, db, args, flags)
	case "symbols":
		return runSymbols(ctx, db, root, args)
	case "read-symbol":
		return runReadSymbol(ctx, db, root, args, flags)
	case "expand":
		return runExpand(ctx, db, root, args, flags)
	case "xrefs":
		return runXrefs(ctx, db, args)
	case "gather":
		return runGather(ctx, db, root, args, flags)
	case "replace-symbol":
		return runReplaceSymbol(ctx, db, root, args, flags)
	case "replace-span":
		return runReplaceSpan(ctx, db, root, args, flags)
	case "diff-preview":
		return runDiffPreview(ctx, db, root, args, flags)
	case "diff-preview-span":
		return runDiffPreviewSpan(ctx, db, root, args, flags)
	case "read-file":
		return runReadFile(ctx, db, root, args, flags)
	case "replace-text":
		return runReplaceText(ctx, db, root, args, flags)
	case "write-file":
		return runWriteFile(ctx, db, root, args, flags)
	case "replace-lines":
		return runReplaceLines(ctx, db, root, args, flags)
	case "rename-symbol":
		return runRenameSymbol(ctx, db, root, args, flags)
	case "insert-after":
		return runInsertAfter(ctx, db, root, args, flags)
	case "append-file":
		return runAppendFile(ctx, db, root, args, flags)
	case "smart-edit":
		return runSmartEdit(ctx, db, root, args, flags)
	case "find-files":
		return runFindFiles(ctx, db, root, args, flags)
	case "batch-read":
		return runBatchRead(ctx, db, root, args, flags)
	default:
		return nil, fmt.Errorf("unknown command: %s", cmd)
	}
}

// --- individual command handlers ---

func runInit(ctx context.Context, db *index.DB) (any, error) {
	filesChanged, symbolsChanged, err := index.IndexRepo(ctx, db)
	if err != nil {
		return nil, err
	}
	totalFiles, totalSymbols, _ := db.Stats(ctx)
	return map[string]any{
		"status":          "ok",
		"files_changed":   filesChanged,
		"symbols_changed": symbolsChanged,
		"total_files":     totalFiles,
		"total_symbols":   totalSymbols,
	}, nil
}

func runRepoMap(ctx context.Context, db *index.DB, flags map[string]any) (any, error) {
	repoMap, err := index.RepoMap(ctx, db)
	if err != nil {
		return nil, err
	}

	budget := flagInt(flags, "budget", 0)
	truncated := false
	if budget > 0 {
		size := len(repoMap) / 4
		if size > budget {
			chars := budget * 4
			repoMap, truncated = output.TruncateAtLine(repoMap, chars)
		}
	}

	files, symbols, _ := db.Stats(ctx)
	return map[string]any{
		"files":     files,
		"symbols":   symbols,
		"map":       repoMap,
		"truncated": truncated,
	}, nil
}

func runSearch(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("search requires 1 argument: <pattern>")
	}
	budget := flagInt(flags, "budget", 0)
	showBody := flagBool(flags, "body", false)
	return search.SearchSymbol(ctx, db, args[0], budget, showBody)
}

func runSearchText(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("search-text requires 1 argument: <pattern>")
	}
	budget := flagInt(flags, "budget", 0)
	useRegex := flagBool(flags, "regex", false)
	var opts []search.SearchTextOption
	if inc := flagStringSlice(flags, "include"); len(inc) > 0 {
		opts = append(opts, search.WithInclude(inc...))
	}
	if exc := flagStringSlice(flags, "exclude"); len(exc) > 0 {
		opts = append(opts, search.WithExclude(exc...))
	}
	if ctxLines := flagInt(flags, "context", 0); ctxLines > 0 {
		opts = append(opts, search.WithContext(ctxLines))
	}
	return search.SearchText(ctx, db, args[0], budget, useRegex, opts...)
}

func runSymbols(ctx context.Context, db *index.DB, root string, args []string) (any, error) {
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

	// Detect duplicate names for disambiguation
	nameCounts := make(map[string]int)
	for _, s := range syms {
		nameCounts[s.Name]++
	}

	var results []output.Symbol
	for _, s := range syms {
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
	return results, nil
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
	return output.ExpandResult{
		Truncated: truncated,
		Symbol: output.Symbol{
			Type:  sym.Type,
			Name:  sym.Name,
			File:  output.Rel(sym.File),
			Lines: [2]int{int(sym.StartLine), int(sym.EndLine)},
			Size:  size,
			Hash:  hash,
		},
		Body: body,
	}, nil
}

func runExpand(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("expand requires 1-2 arguments: [file] <symbol>")
	}

	showBody := flagBool(flags, "body", false)
	showCallers := flagBool(flags, "callers", false)
	showDeps := flagBool(flags, "deps", false)
	budget := flagInt(flags, "budget", 0)

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	hash, _ := edit.FileHash(sym.File)
	result := output.ExpandResult{
		Symbol: output.Symbol{
			Type:  sym.Type,
			Name:  sym.Name,
			File:  output.Rel(sym.File),
			Lines: [2]int{int(sym.StartLine), int(sym.EndLine)},
			Size:  int(sym.EndByte-sym.StartByte) / 4,
			Hash:  hash,
		},
	}

	if showBody {
		src, err := os.ReadFile(sym.File)
		if err != nil {
			return nil, err
		}
		body := string(src[sym.StartByte:sym.EndByte])
		if budget > 0 {
			size := len(body) / 4
			if size > budget {
				chars := budget * 4
				body, _ = output.TruncateAtLine(body, chars)
			}
		}
		result.Body = body
	}

	if showCallers {
		callers, err := db.FindSemanticCallers(ctx, sym.Name, sym.File)
		if err != nil || len(callers) == 0 {
			// Fallback to text-based
			refs, err := index.FindReferencesInFile(ctx, db, sym.Name, sym.File)
			if err == nil {
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
								result.Callers = append(result.Callers, output.Symbol{
									Type:  s.Type,
									Name:  s.Name,
									File:  output.Rel(s.File),
									Lines: [2]int{int(s.StartLine), int(s.EndLine)},
									Size:  int(s.EndByte-s.StartByte) / 4,
								})
							}
						}
					}
				}
			}
		} else {
			for _, c := range callers {
				result.Callers = append(result.Callers, output.Symbol{
					Type:  c.Type,
					Name:  c.Name,
					File:  output.Rel(c.File),
					Lines: [2]int{int(c.StartLine), int(c.EndLine)},
					Size:  int(c.EndByte-c.StartByte) / 4,
				})
			}
		}
	}

	if showDeps {
		deps, err := index.FindDeps(ctx, db, sym)
		if err == nil {
			for _, d := range deps {
				result.Deps = append(result.Deps, output.Symbol{
					Type:  d.Type,
					Name:  d.Name,
					File:  output.Rel(d.File),
					Lines: [2]int{int(d.StartLine), int(d.EndLine)},
					Size:  int(d.EndByte-d.StartByte) / 4,
				})
			}
		}
	}

	return result, nil
}

func runXrefs(ctx context.Context, db *index.DB, args []string) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("xrefs requires 1 argument: <symbol>")
	}

	refs, err := index.FindReferences(ctx, db, args[0])
	if err != nil {
		return nil, err
	}

	var results []output.Symbol
	for _, r := range refs {
		results = append(results, output.Symbol{
			Type:  "reference",
			Name:  r.Name,
			File:  output.Rel(r.File),
			Lines: [2]int{int(r.StartLine), int(r.EndLine)},
		})
	}
	return results, nil
}

func runGather(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("gather requires at least 1 argument")
	}
	budget := flagInt(flags, "budget", 1500)
	includeBody := flagBool(flags, "body", false)

	// Try exact symbol resolution first
	sym, resolveErr := resolveSymbolArgs(ctx, db, root, args)
	if resolveErr == nil {
		return gather.Gather(ctx, db, sym.File, sym.Name, budget, includeBody)
	}
	// Fall back to search-based gather for single arg
	if len(args) == 1 {
		return gather.GatherBySearch(ctx, db, args[0], budget, includeBody)
	}
	return nil, resolveErr
}

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

	var numbered strings.Builder
	if startLine <= endLine {
		for i, line := range lines[startLine-1 : endLine] {
			fmt.Fprintf(&numbered, "%d\t%s", startLine+i, line)
		}
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

func runReplaceText(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("replace-text requires 3 arguments: <file> <old-text> <new-text>")
	}
	expectHash := flagString(flags, "expect-hash", "")
	replaceAll := flagBool(flags, "all", false)

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}
	oldText := args[1]
	newText := args[2]

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	if expectHash != "" {
		hash, _ := edit.FileHash(file)
		if hash != expectHash {
			return output.EditResult{OK: false, File: output.Rel(file), Message: fmt.Sprintf("hash mismatch: expected %s, got %s", expectHash, hash)}, nil
		}
	}

	content := string(data)
	useRegex := flagBool(flags, "regex", false)

	var result string
	var count int
	if useRegex {
		re, err := regexp.Compile(oldText)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		matches := re.FindAllStringIndex(content, -1)
		if len(matches) == 0 {
			return output.EditResult{OK: false, File: output.Rel(file), Message: "pattern not found in file"}, nil
		}
		if replaceAll {
			count = len(matches)
			result = re.ReplaceAllString(content, newText)
		} else {
			count = 1
			loc := matches[0]
			result = content[:loc[0]] + re.ReplaceAllString(content[loc[0]:loc[1]], newText) + content[loc[1]:]
		}
	} else {
		if !strings.Contains(content, oldText) {
			return output.EditResult{OK: false, File: output.Rel(file), Message: "old-text not found in file"}, nil
		}
		if replaceAll {
			count = strings.Count(content, oldText)
			result = strings.ReplaceAll(content, oldText, newText)
		} else {
			count = 1
			result = strings.Replace(content, oldText, newText, 1)
		}
	}

	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, []byte(result), info.Mode()); err != nil {
		return nil, err
	}

	// Re-index if it's a supported language
	_ = index.IndexFile(ctx, db, file)

	return editOK(file, fmt.Sprintf("replaced %d occurrence(s)", count)), nil
}

func runReplaceLines(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("replace-lines requires 3 arguments: <file> <start-line> <end-line>")
	}
	expectHash := flagString(flags, "expect-hash", "")
	replacement := flagString(flags, "replacement", "")
	if replacement == "" {
		return nil, fmt.Errorf("replace-lines requires 'replacement' in flags")
	}

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	var startLine, endLine int
	fmt.Sscanf(args[1], "%d", &startLine)
	fmt.Sscanf(args[2], "%d", &endLine)

	err = edit.ReplaceLines(file, startLine, endLine, replacement, expectHash)
	if err != nil {
		return output.EditResult{OK: false, File: output.Rel(file), Message: err.Error()}, nil
	}

	_ = index.IndexFile(ctx, db, file)

	return editOK(file, fmt.Sprintf("replaced lines %d-%d", startLine, endLine)), nil
}

func runWriteFile(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("write-file requires 1 argument: <file>")
	}
	content := flagString(flags, "content", "")
	mkdir := flagBool(flags, "mkdir", false)

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	if mkdir {
		dir := file[:strings.LastIndex(file, "/")]
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir: %w", err)
		}
	}

	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		return nil, err
	}

	_ = index.IndexFile(ctx, db, file)

	return editOK(file, fmt.Sprintf("wrote %d bytes", len(content))), nil
}

func flagString(flags map[string]any, key string, defaultVal string) string {
	if flags == nil {
		return defaultVal
	}
	v, ok := flags[key]
	if !ok {
		return defaultVal
	}
	if s, ok := v.(string); ok {
		return s
	}
	return defaultVal
}

func runReplaceSymbol(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("replace-symbol requires 1-2 arguments: [file] <symbol>")
	}
	expectHash := flagString(flags, "expect-hash", "")
	replacement := flagString(flags, "replacement", "")
	if replacement == "" {
		return nil, fmt.Errorf("replace-symbol requires 'replacement' in flags")
	}

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	err = edit.ReplaceSpan(sym.File, sym.StartByte, sym.EndByte, replacement, expectHash)
	if err != nil {
		return output.EditResult{OK: false, File: output.Rel(sym.File), Message: err.Error()}, nil
	}

	// Re-index the modified file
	_ = index.IndexFile(ctx, db, sym.File)

	return editOK(sym.File, fmt.Sprintf("replaced symbol %s", sym.Name)), nil
}

func runReplaceSpan(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("replace-span requires 3 arguments: <file> <start-byte> <end-byte>")
	}
	expectHash := flagString(flags, "expect-hash", "")
	replacement := flagString(flags, "replacement", "")
	if replacement == "" {
		return nil, fmt.Errorf("replace-span requires 'replacement' in flags")
	}

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	var startByte, endByte uint32
	fmt.Sscanf(args[1], "%d", &startByte)
	fmt.Sscanf(args[2], "%d", &endByte)

	err = edit.ReplaceSpan(file, startByte, endByte, replacement, expectHash)
	if err != nil {
		return output.EditResult{OK: false, File: output.Rel(file), Message: err.Error()}, nil
	}

	// Re-index the modified file
	_ = index.IndexFile(ctx, db, file)

	return editOK(file, "span replaced"), nil
}

func runDiffPreview(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("diff-preview requires 1-2 arguments: [file] <symbol>")
	}
	replacement := flagString(flags, "replacement", "")
	if replacement == "" {
		return nil, fmt.Errorf("diff-preview requires 'replacement' in flags")
	}

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	diff, err := edit.DiffPreview(sym.File, sym.StartByte, sym.EndByte, replacement)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"file":     output.Rel(sym.File),
		"symbol":   sym.Name,
		"diff":     diff,
		"old_size": int(sym.EndByte - sym.StartByte),
		"new_size": len(replacement),
	}, nil
}

func runDiffPreviewSpan(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 3 {
		return nil, fmt.Errorf("diff-preview-span requires 3 arguments: <file> <start-byte> <end-byte>")
	}
	replacement := flagString(flags, "replacement", "")
	if replacement == "" {
		return nil, fmt.Errorf("diff-preview-span requires 'replacement' in flags")
	}

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	var startByte, endByte uint32
	fmt.Sscanf(args[1], "%d", &startByte)
	fmt.Sscanf(args[2], "%d", &endByte)

	diff, err := edit.DiffPreview(file, startByte, endByte, replacement)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"file":     output.Rel(file),
		"diff":     diff,
		"old_size": int(endByte - startByte),
		"new_size": len(replacement),
	}, nil
}

func runRenameSymbol(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("rename-symbol requires 2 arguments: <old-name> <new-name>")
	}
	oldName := args[0]
	newName := args[1]
	dryRun := flagBool(flags, "dry-run", false)

	// Find all references to the old name
	refs, err := index.FindReferences(ctx, db, oldName)
	if err != nil {
		return nil, err
	}

	if len(refs) == 0 {
		return output.RenameResult{OldName: oldName, NewName: newName, DryRun: dryRun}, nil
	}

	// Group refs by file
	grouped := make(map[string][]index.SymbolInfo)
	for _, r := range refs {
		grouped[r.File] = append(grouped[r.File], r)
	}

	// Collect file list
	var filesChanged []string
	for file := range grouped {
		filesChanged = append(filesChanged, output.Rel(file))
	}

	// Dry-run: show what would change without applying
	if dryRun {
		var preview []output.RenameOccurrence
		for _, r := range refs {
			// Read the line containing this reference
			src, err := os.ReadFile(r.File)
			if err != nil {
				continue
			}
			lines := strings.SplitAfter(string(src), "\n")
			lineIdx := int(r.StartLine) - 1
			lineText := ""
			if lineIdx >= 0 && lineIdx < len(lines) {
				lineText = strings.TrimRight(lines[lineIdx], "\n")
			}
			preview = append(preview, output.RenameOccurrence{
				File: output.Rel(r.File),
				Line: int(r.StartLine),
				Text: lineText,
			})
		}
		return output.RenameResult{
			OldName:      oldName,
			NewName:      newName,
			FilesChanged: filesChanged,
			Occurrences:  len(refs),
			DryRun:       true,
			Preview:      preview,
		}, nil
	}

	tx := edit.NewTransaction()
	for file, fileRefs := range grouped {
		hash, _ := edit.FileHash(file)
		for i, r := range fileRefs {
			h := ""
			if i == 0 {
				h = hash
			}
			tx.Add(file, r.StartByte, r.EndByte, newName, h)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("rename failed: %w", err)
	}

	// Re-index all affected files and collect new hashes
	hashes := make(map[string]string)
	for file := range grouped {
		_ = index.IndexFile(ctx, db, file)
		rel := output.Rel(file)
		if h, err := edit.FileHash(file); err == nil {
			hashes[rel] = h
		}
	}

	return output.RenameResult{
		OldName:      oldName,
		NewName:      newName,
		FilesChanged: filesChanged,
		Occurrences:  len(refs),
		Hashes:       hashes,
	}, nil
}

func runInsertAfter(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("insert-after requires 1-2 arguments: [file] <symbol>")
	}
	content := flagString(flags, "content", "")
	if content == "" {
		return nil, fmt.Errorf("insert-after requires 'content' in flags")
	}

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	// Insert content after the symbol, with a blank line separator
	insertion := "\n\n" + content
	err = edit.InsertAfterSpan(sym.File, sym.EndByte, insertion)
	if err != nil {
		return output.EditResult{OK: false, File: output.Rel(sym.File), Message: err.Error()}, nil
	}

	_ = index.IndexFile(ctx, db, sym.File)

	return editOK(sym.File, fmt.Sprintf("inserted after %s", sym.Name)), nil
}

func runAppendFile(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("append-file requires 1 argument: <file>")
	}
	content := flagString(flags, "content", "")
	if content == "" {
		return nil, fmt.Errorf("append-file requires 'content' in flags")
	}

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Ensure there's a newline before appending
	sep := "\n"
	if len(data) > 0 && data[len(data)-1] == '\n' {
		sep = ""
	}

	newData := append(data, []byte(sep+content+"\n")...)

	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(file, newData, info.Mode()); err != nil {
		return nil, err
	}

	_ = index.IndexFile(ctx, db, file)

	return editOK(file, fmt.Sprintf("appended %d bytes", len(content))), nil
}

func runSmartEdit(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("smart-edit requires 1-2 arguments: [file] <symbol>")
	}
	replacement := flagString(flags, "replacement", "")
	if replacement == "" {
		return nil, fmt.Errorf("smart-edit requires 'replacement' in flags")
	}

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	// Read the current body
	src, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, err
	}
	oldBody := string(src[sym.StartByte:sym.EndByte])

	// Generate diff preview
	diff, err := edit.DiffPreview(sym.File, sym.StartByte, sym.EndByte, replacement)
	if err != nil {
		return nil, err
	}

	// Get current hash for safe write
	hash, _ := edit.FileHash(sym.File)

	// Apply the edit
	err = edit.ReplaceSpan(sym.File, sym.StartByte, sym.EndByte, replacement, hash)
	if err != nil {
		return nil, fmt.Errorf("edit failed: %w", err)
	}

	// Re-index
	_ = index.IndexFile(ctx, db, sym.File)

	// Return new hash so caller can chain edits
	newHash, _ := edit.FileHash(sym.File)

	return map[string]any{
		"ok":       true,
		"file":     output.Rel(sym.File),
		"symbol":   sym.Name,
		"diff":     diff,
		"hash":     newHash,
		"old_size": len(oldBody) / 4,
		"new_size": len(replacement) / 4,
	}, nil
}

func runFindFiles(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("find-files requires 1 argument: <pattern>")
	}
	pattern := args[0]
	dir := flagString(flags, "dir", "")
	budget := flagInt(flags, "budget", 0)

	return search.FindFiles(ctx, root, pattern, dir, budget)
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
		Content string          `json:"content"`
		Hash    string          `json:"hash"`
		Lines   [2]int          `json:"lines"`
		Size    int             `json:"size"`
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
				continue
			}
			src, err := os.ReadFile(sym.File)
			if err != nil {
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
				Content: body,
				Hash:    hash,
				Lines:   [2]int{int(sym.StartLine), int(sym.EndLine)},
				Size:    size,
			})
		} else {
			// Read entire file
			file, err := db.ResolvePath(filePath)
			if err != nil {
				continue
			}
			data, err := os.ReadFile(file)
			if err != nil {
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
				Content: body,
				Hash:    hash,
				Lines:   [2]int{1, len(lines)},
				Size:    size,
			}
			if showSymbols {
				syms, err := db.GetSymbolsByFile(ctx, file)
				if err == nil {
					for _, s := range syms {
						entry.Symbols = append(entry.Symbols, output.Symbol{
							Type:  s.Type,
							Name:  s.Name,
							Lines: [2]int{int(s.StartLine), int(s.EndLine)},
							Size:  int(s.EndByte-s.StartByte) / 4,
						})
					}
				}
			}
			results = append(results, entry)
		}
	}

	return results, nil
}

// editOK builds a successful EditResult with the file's new hash.
func editOK(file string, message string) output.EditResult {
	hash, _ := edit.FileHash(file)
	return output.EditResult{OK: true, File: output.Rel(file), Message: message, Hash: hash}
}

// --- flag helpers ---

func flagInt(flags map[string]any, key string, defaultVal int) int {
	if flags == nil {
		return defaultVal
	}
	v, ok := flags[key]
	if !ok {
		return defaultVal
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return defaultVal
	}
}

func flagStringSlice(flags map[string]any, key string) []string {
	if flags == nil {
		return nil
	}
	v, ok := flags[key]
	if !ok {
		return nil
	}
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		var out []string
		for _, item := range s {
			if str, ok := item.(string); ok {
				out = append(out, str)
			}
		}
		return out
	case string:
		return []string{s}
	default:
		return nil
	}
}

func flagBool(flags map[string]any, key string, defaultVal bool) bool {
	if flags == nil {
		return defaultVal
	}
	v, ok := flags[key]
	if !ok {
		return defaultVal
	}
	switch b := v.(type) {
	case bool:
		return b
	default:
		return defaultVal
	}
}
