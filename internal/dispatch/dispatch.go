package dispatch

import (
	"context"
	"fmt"
	"os"
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
	default:
		return nil, fmt.Errorf("unknown command: %s", cmd)
	}
}

// --- individual command handlers ---

func runInit(ctx context.Context, db *index.DB) (any, error) {
	files, symbols, err := index.IndexRepo(ctx, db)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":  "ok",
		"files":   files,
		"symbols": symbols,
	}, nil
}

func runRepoMap(ctx context.Context, db *index.DB, flags map[string]any) (any, error) {
	repoMap, err := index.RepoMap(ctx, db)
	if err != nil {
		return nil, err
	}

	budget := flagInt(flags, "budget", 0)
	if budget > 0 {
		size := len(repoMap) / 4
		if size > budget {
			chars := budget * 4
			if chars < len(repoMap) {
				repoMap = repoMap[:chars] + "\n... (trimmed to budget)"
			}
		}
	}

	files, symbols, _ := db.Stats(ctx)
	return map[string]any{
		"files":   files,
		"symbols": symbols,
		"map":     repoMap,
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
	return search.SearchText(ctx, db, args[0], budget, useRegex)
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

	var results []output.Symbol
	for _, s := range syms {
		results = append(results, output.Symbol{
			Type:  s.Type,
			Name:  s.Name,
			File:  output.Rel(s.File),
			Lines: [2]int{int(s.StartLine), int(s.EndLine)},
			Size:  int(s.EndByte-s.StartByte) / 4,
		})
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

	if budget > 0 && size > budget {
		chars := budget * 4
		if chars < len(body) {
			body = body[:chars] + "\n... (trimmed to budget)"
		}
	}

	hash, _ := edit.FileHash(sym.File)
	return output.ExpandResult{
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
				if chars < len(body) {
					body = body[:chars] + "\n... (trimmed to budget)"
				}
			}
		}
		result.Body = body
	}

	if showCallers {
		refs, err := index.FindReferences(ctx, db, sym.Name)
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

	// Try exact symbol resolution first
	sym, resolveErr := resolveSymbolArgs(ctx, db, root, args)
	if resolveErr == nil {
		return gather.Gather(ctx, db, sym.File, sym.Name, budget)
	}
	// Fall back to search-based gather for single arg
	if len(args) == 1 {
		return gather.GatherBySearch(ctx, db, args[0], budget)
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

	var body string
	if startLine <= endLine {
		body = strings.Join(lines[startLine-1:endLine], "")
	}

	size := len(body) / 4
	if budget > 0 && size > budget {
		chars := budget * 4
		if chars < len(body) {
			body = body[:chars] + "\n... (trimmed to budget)"
		}
		size = budget
	}

	return map[string]any{
		"file":        output.Rel(file),
		"lines":       [2]int{startLine, endLine},
		"total_lines": totalLines,
		"size":        size,
		"content":     body,
	}, nil
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
	if !strings.Contains(content, oldText) {
		return output.EditResult{OK: false, File: output.Rel(file), Message: "old-text not found in file"}, nil
	}

	var result string
	var count int
	if replaceAll {
		count = strings.Count(content, oldText)
		result = strings.ReplaceAll(content, oldText, newText)
	} else {
		count = 1
		result = strings.Replace(content, oldText, newText, 1)
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

	// Re-index all affected files
	for file := range grouped {
		_ = index.IndexFile(ctx, db, file)
	}

	return output.RenameResult{
		OldName:      oldName,
		NewName:      newName,
		FilesChanged: filesChanged,
		Occurrences:  len(refs),
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
