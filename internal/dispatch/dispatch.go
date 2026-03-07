package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
		return runXrefs(ctx, db, root, args)
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
	case "edit-plan":
		return runEditPlan(ctx, db, root, args, flags)
	case "impact":
		return runImpact(ctx, db, root, args, flags)
	case "call-chain":
		return runCallChain(ctx, db, root, args, flags)
	case "verify":
		return runVerify(ctx, db, root, args, flags)
	case "multi", "get-diff":
		return nil, fmt.Errorf("%s is only available in MCP mode (edr mcp)", cmd)
	default:
		return nil, fmt.Errorf("unknown command: %s", cmd)
	}
}

// MultiCmd represents a single command in a multi-command batch.
type MultiCmd struct {
	Cmd   string         `json:"cmd"`
	Args  []string       `json:"args"`
	Flags map[string]any `json:"flags"`
}

// MultiResult holds the result of a single command in a multi-command batch.
type MultiResult struct {
	Cmd    string `json:"cmd"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// DispatchMulti runs multiple commands sequentially and returns all results.
func DispatchMulti(ctx context.Context, db *index.DB, commands []MultiCmd) []MultiResult {
	results := make([]MultiResult, len(commands))
	for i, c := range commands {
		if c.Args == nil {
			c.Args = []string{}
		}
		if c.Flags == nil {
			c.Flags = map[string]any{}
		}
		result, err := Dispatch(ctx, db, c.Cmd, c.Args, c.Flags)
		if err != nil {
			results[i] = MultiResult{Cmd: c.Cmd, OK: false, Error: err.Error()}
		} else {
			results[i] = MultiResult{Cmd: c.Cmd, OK: true, Result: result}
		}
	}
	return results
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
	var opts []index.RepoMapOption
	if dir := flagString(flags, "dir", ""); dir != "" {
		opts = append(opts, index.WithDir(dir))
	}
	if glob := flagString(flags, "glob", ""); glob != "" {
		opts = append(opts, index.WithGlob(glob))
	}
	if symType := flagString(flags, "type", ""); symType != "" {
		opts = append(opts, index.WithSymbolType(symType))
	}
	if grep := flagString(flags, "grep", ""); grep != "" {
		opts = append(opts, index.WithGrep(grep))
	}

	repoMap, err := index.RepoMap(ctx, db, opts...)
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
	showSigs := flagBool(flags, "signatures", false)
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

	if showSigs {
		result.Symbol.Signature = index.ExtractSignature(*sym)
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
								csym := output.Symbol{
									Type:  s.Type,
									Name:  s.Name,
									File:  output.Rel(s.File),
									Lines: [2]int{int(s.StartLine), int(s.EndLine)},
									Size:  int(s.EndByte-s.StartByte) / 4,
								}
								if showSigs {
									csym.Signature = index.ExtractSignature(s)
								}
								result.Callers = append(result.Callers, csym)
							}
						}
					}
				}
			}
		} else {
			for _, c := range callers {
				csym := output.Symbol{
					Type:  c.Type,
					Name:  c.Name,
					File:  output.Rel(c.File),
					Lines: [2]int{int(c.StartLine), int(c.EndLine)},
					Size:  int(c.EndByte-c.StartByte) / 4,
				}
				if showSigs {
					csym.Signature = index.ExtractSignature(c)
				}
				result.Callers = append(result.Callers, csym)
			}
		}
	}

	if showDeps {
		deps, err := index.FindDeps(ctx, db, sym)
		if err == nil {
			for _, d := range deps {
				dsym := output.Symbol{
					Type:  d.Type,
					Name:  d.Name,
					File:  output.Rel(d.File),
					Lines: [2]int{int(d.StartLine), int(d.EndLine)},
					Size:  int(d.EndByte-d.StartByte) / 4,
				}
				if showSigs {
					dsym.Signature = index.ExtractSignature(d)
				}
				result.Deps = append(result.Deps, dsym)
			}
		}
	}

	return result, nil
}

func runXrefs(ctx context.Context, db *index.DB, root string, args []string) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("xrefs requires 1-2 arguments: [file] <symbol>")
	}

	// Resolve symbol with optional file disambiguation
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	refs, err := index.FindReferences(ctx, db, sym.Name)
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
	if results == nil {
		results = []output.Symbol{}
	}
	return results, nil
}

func runGather(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("gather requires at least 1 argument")
	}
	budget := flagInt(flags, "budget", 1500)
	includeBody := flagBool(flags, "body", false)
	includeSigs := flagBool(flags, "signatures", false)

	// Try exact symbol resolution first
	sym, resolveErr := resolveSymbolArgs(ctx, db, root, args)
	if resolveErr == nil {
		return gather.Gather(ctx, db, sym.File, sym.Name, budget, includeBody, includeSigs)
	}
	// Fall back to search-based gather for single arg
	if len(args) == 1 {
		return gather.GatherBySearch(ctx, db, args[0], budget, includeBody, includeSigs)
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

	// Find all identifier occurrences (exact byte ranges for replacement).
	// We use FindIdentifierOccurrences instead of FindReferences because the
	// semantic path returns containing symbols, not individual identifier positions.
	refs, err := index.FindIdentifierOccurrences(ctx, db, oldName)
	if err != nil {
		return nil, err
	}

	// Apply --scope filter (glob pattern on relative paths)
	if scope := flagString(flags, "scope", ""); scope != "" {
		var filtered []index.SymbolInfo
		for _, r := range refs {
			rel := output.Rel(r.File)
			if matched, _ := filepath.Match(scope, rel); matched {
				filtered = append(filtered, r)
			} else if matched, _ := filepath.Match(scope, filepath.Base(rel)); matched {
				filtered = append(filtered, r)
			} else if strings.HasPrefix(rel, strings.TrimSuffix(scope, "**")) {
				// Support "dir/**" style patterns
				filtered = append(filtered, r)
			}
		}
		refs = filtered
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
	replacement := flagString(flags, "replacement", "")
	if replacement == "" {
		return nil, fmt.Errorf("smart-edit requires 'replacement' in flags")
	}

	// Determine targeting mode:
	// 1. --lines <start> <end>: line range (requires file as first arg)
	// 2. --match <text>: text match (requires file as first arg, like replace-text but with smart-edit UX)
	// 3. Default: symbol-based (existing behavior)

	startLine := flagInt(flags, "start_line", 0)
	endLine := flagInt(flags, "end_line", 0)
	matchText := flagString(flags, "match", "")

	if startLine > 0 && endLine > 0 {
		// Line-range mode
		if len(args) < 1 {
			return nil, fmt.Errorf("smart-edit with --start_line/--end_line requires a file argument")
		}
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		return smartEditSpan(ctx, db, file, startLine, endLine, replacement, "")
	}

	if matchText != "" {
		// Text-match mode
		if len(args) < 1 {
			return nil, fmt.Errorf("smart-edit with --match requires a file argument")
		}
		file, err := db.ResolvePath(args[0])
		if err != nil {
			return nil, err
		}
		return smartEditMatch(ctx, db, file, matchText, replacement, flags)
	}

	// Symbol mode (original behavior)
	if len(args) < 1 {
		return nil, fmt.Errorf("smart-edit requires: [file] <symbol>, or <file> with --lines/--match")
	}
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}
	return smartEditByteRange(ctx, db, sym.File, sym.StartByte, sym.EndByte, replacement, sym.Name)
}

// smartEditByteRange applies an edit to a byte range and returns a smart-edit result.
func smartEditByteRange(ctx context.Context, db *index.DB, file string, startByte, endByte uint32, replacement, label string) (any, error) {
	src, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	oldBody := string(src[startByte:endByte])

	diff, err := edit.DiffPreview(file, startByte, endByte, replacement)
	if err != nil {
		return nil, err
	}

	hash, _ := edit.FileHash(file)
	err = edit.ReplaceSpan(file, startByte, endByte, replacement, hash)
	if err != nil {
		return nil, fmt.Errorf("edit failed: %w", err)
	}

	_ = index.IndexFile(ctx, db, file)
	newHash, _ := edit.FileHash(file)

	result := map[string]any{
		"ok":       true,
		"file":     output.Rel(file),
		"diff":     diff,
		"hash":     newHash,
		"old_size": len(oldBody) / 4,
		"new_size": len(replacement) / 4,
	}
	if label != "" {
		result["symbol"] = label
	}
	return result, nil
}

// smartEditSpan applies an edit to a line range.
func smartEditSpan(ctx context.Context, db *index.DB, file string, startLine, endLine int, replacement, label string) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Convert lines to byte offsets
	line := 1
	var startByte, endByte uint32
	foundStart := false
	for i := 0; i <= len(data); i++ {
		if line == startLine && !foundStart {
			startByte = uint32(i)
			foundStart = true
		}
		if line == endLine+1 || (line == endLine && i == len(data)) {
			endByte = uint32(i)
			break
		}
		if i < len(data) && data[i] == '\n' {
			line++
		}
	}
	if !foundStart {
		return nil, fmt.Errorf("smart-edit: start line %d beyond file (%d lines)", startLine, line-1)
	}
	if endByte == 0 && endLine >= line {
		endByte = uint32(len(data))
	}

	if label == "" {
		label = fmt.Sprintf("lines %d-%d", startLine, endLine)
	}
	return smartEditByteRange(ctx, db, file, startByte, endByte, replacement, label)
}

// smartEditMatch applies an edit by finding and replacing text.
func smartEditMatch(ctx context.Context, db *index.DB, file, matchText, replacement string, flags map[string]any) (any, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	content := string(data)
	useRegex := flagBool(flags, "regex", false)
	replaceAll := flagBool(flags, "all", false)

	var startByte, endByte int
	if useRegex {
		re, err := regexp.Compile(matchText)
		if err != nil {
			return nil, fmt.Errorf("invalid regex: %w", err)
		}
		loc := re.FindStringIndex(content)
		if loc == nil {
			return nil, fmt.Errorf("smart-edit: pattern %q not found in %s", matchText, output.Rel(file))
		}
		if replaceAll {
			// For regex --all, do a full replacement and write directly
			result := re.ReplaceAllString(content, replacement)
			count := len(re.FindAllStringIndex(content, -1))

			hash, _ := edit.FileHash(file)
			info, _ := os.Stat(file)
			if err := os.WriteFile(file, []byte(result), info.Mode()); err != nil {
				return nil, err
			}
			_ = index.IndexFile(ctx, db, file)
			newHash, _ := edit.FileHash(file)

			return map[string]any{
				"ok":       true,
				"file":     output.Rel(file),
				"hash":     newHash,
				"old_hash": hash,
				"count":    count,
				"match":    matchText,
			}, nil
		}
		startByte = loc[0]
		endByte = loc[1]
	} else {
		idx := strings.Index(content, matchText)
		if idx < 0 {
			return nil, fmt.Errorf("smart-edit: text %q not found in %s", matchText, output.Rel(file))
		}
		if replaceAll {
			count := strings.Count(content, matchText)
			result := strings.ReplaceAll(content, matchText, replacement)

			hash, _ := edit.FileHash(file)
			info, _ := os.Stat(file)
			if err := os.WriteFile(file, []byte(result), info.Mode()); err != nil {
				return nil, err
			}
			_ = index.IndexFile(ctx, db, file)
			newHash, _ := edit.FileHash(file)

			return map[string]any{
				"ok":       true,
				"file":     output.Rel(file),
				"hash":     newHash,
				"old_hash": hash,
				"count":    count,
				"match":    matchText,
			}, nil
		}
		startByte = idx
		endByte = idx + len(matchText)
	}

	return smartEditByteRange(ctx, db, file, uint32(startByte), uint32(endByte), replacement, "")
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

// editPlanEntry describes a single edit within an edit-plan.
type editPlanEntry struct {
	File        string `json:"file"`
	Symbol      string `json:"symbol,omitempty"`      // symbol-based edit
	StartLine   int    `json:"start_line,omitempty"`  // line-based edit
	EndLine     int    `json:"end_line,omitempty"`    // line-based edit
	OldText     string `json:"old_text,omitempty"`    // text-based edit
	NewText     string `json:"new_text,omitempty"`    // text-based edit (used with old_text)
	Replacement string `json:"replacement,omitempty"` // replacement for symbol/line edits
	ExpectHash  string `json:"expect_hash,omitempty"`
	All         bool   `json:"all,omitempty"` // replace all occurrences (text-based)
}

func runEditPlan(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	// Edits come from flags.edits as a JSON array
	rawEdits, ok := flags["edits"]
	if !ok {
		return nil, fmt.Errorf("edit-plan requires 'edits' array in flags")
	}

	// Re-marshal to get proper typing from the any interface
	var edits []editPlanEntry
	switch v := rawEdits.(type) {
	case []any:
		// JSON came through as []any — need to re-marshal
		rawJSON, _ := json.Marshal(v)
		if err := json.Unmarshal(rawJSON, &edits); err != nil {
			return nil, fmt.Errorf("edit-plan: invalid edits array: %w", err)
		}
	default:
		rawJSON, _ := json.Marshal(v)
		if err := json.Unmarshal(rawJSON, &edits); err != nil {
			return nil, fmt.Errorf("edit-plan: invalid edits: %w", err)
		}
	}

	if len(edits) == 0 {
		return nil, fmt.Errorf("edit-plan: edits array is empty")
	}

	dryRun := flagBool(flags, "dry-run", false)

	// Resolve each edit to byte-level spans
	type resolvedEdit struct {
		File        string
		StartByte   uint32
		EndByte     uint32
		Replacement string
		ExpectHash  string
		Description string
	}

	var resolved []resolvedEdit

	for i, e := range edits {
		if e.File == "" {
			return nil, fmt.Errorf("edit-plan: edit %d missing 'file'", i)
		}
		file, err := db.ResolvePath(e.File)
		if err != nil {
			return nil, fmt.Errorf("edit-plan: edit %d: %w", i, err)
		}

		switch {
		case e.Symbol != "":
			// Symbol-based edit
			sym, err := db.GetSymbol(ctx, file, e.Symbol)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: symbol %q: %w", i, e.Symbol, err)
			}
			resolved = append(resolved, resolvedEdit{
				File: file, StartByte: sym.StartByte, EndByte: sym.EndByte,
				Replacement: e.Replacement, ExpectHash: e.ExpectHash,
				Description: fmt.Sprintf("replace symbol %s in %s", e.Symbol, output.Rel(file)),
			})

		case e.OldText != "":
			// Text-based edit — resolve to byte spans
			data, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: %w", i, err)
			}
			content := string(data)
			idx := strings.Index(content, e.OldText)
			if idx < 0 {
				return nil, fmt.Errorf("edit-plan: edit %d: old_text not found in %s", i, output.Rel(file))
			}
			if e.All {
				// For replace-all, collect all occurrences (reverse order for offset stability)
				var offsets []int
				start := 0
				for {
					idx := strings.Index(content[start:], e.OldText)
					if idx < 0 {
						break
					}
					offsets = append(offsets, start+idx)
					start += idx + len(e.OldText)
				}
				for j := len(offsets) - 1; j >= 0; j-- {
					resolved = append(resolved, resolvedEdit{
						File: file, StartByte: uint32(offsets[j]), EndByte: uint32(offsets[j] + len(e.OldText)),
						Replacement: e.NewText, ExpectHash: e.ExpectHash,
						Description: fmt.Sprintf("replace text in %s (occurrence %d)", output.Rel(file), j+1),
					})
				}
			} else {
				resolved = append(resolved, resolvedEdit{
					File: file, StartByte: uint32(idx), EndByte: uint32(idx + len(e.OldText)),
					Replacement: e.NewText, ExpectHash: e.ExpectHash,
					Description: fmt.Sprintf("replace text in %s", output.Rel(file)),
				})
			}

		case e.StartLine > 0 && e.EndLine > 0:
			// Line-based edit — convert to byte offsets
			data, err := os.ReadFile(file)
			if err != nil {
				return nil, fmt.Errorf("edit-plan: edit %d: %w", i, err)
			}
			var startByte, endByte uint32
			line := 1
			foundStart := false
			for j := 0; j <= len(data); j++ {
				if line == e.StartLine && !foundStart {
					startByte = uint32(j)
					foundStart = true
				}
				if line == e.EndLine+1 || (line == e.EndLine && j == len(data)) {
					endByte = uint32(j)
					break
				}
				if j < len(data) && data[j] == '\n' {
					line++
				}
			}
			if !foundStart {
				return nil, fmt.Errorf("edit-plan: edit %d: start line %d beyond file", i, e.StartLine)
			}
			resolved = append(resolved, resolvedEdit{
				File: file, StartByte: startByte, EndByte: endByte,
				Replacement: e.Replacement, ExpectHash: e.ExpectHash,
				Description: fmt.Sprintf("replace lines %d-%d in %s", e.StartLine, e.EndLine, output.Rel(file)),
			})

		default:
			return nil, fmt.Errorf("edit-plan: edit %d: must specify symbol, old_text, or start_line/end_line", i)
		}
	}

	// Dry-run: return what would happen
	if dryRun {
		var preview []map[string]any
		for _, r := range resolved {
			entry := map[string]any{
				"file":        output.Rel(r.File),
				"description": r.Description,
			}
			diff, err := edit.DiffPreview(r.File, r.StartByte, r.EndByte, r.Replacement)
			if err == nil && diff != "" {
				entry["diff"] = diff
			}
			preview = append(preview, entry)
		}
		return map[string]any{
			"dry_run": true,
			"edits":   preview,
			"count":   len(preview),
		}, nil
	}

	// Apply atomically via Transaction
	tx := edit.NewTransaction()
	for _, r := range resolved {
		tx.Add(r.File, r.StartByte, r.EndByte, r.Replacement, r.ExpectHash)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("edit-plan: %w", err)
	}

	// Re-index affected files and collect hashes
	affectedFiles := make(map[string]bool)
	for _, r := range resolved {
		affectedFiles[r.File] = true
	}
	hashes := make(map[string]string)
	for file := range affectedFiles {
		_ = index.IndexFile(ctx, db, file)
		if h, err := edit.FileHash(file); err == nil {
			hashes[output.Rel(file)] = h
		}
	}

	var descriptions []string
	for _, r := range resolved {
		descriptions = append(descriptions, r.Description)
	}

	return map[string]any{
		"ok":          true,
		"edits":       len(resolved),
		"files":       len(affectedFiles),
		"hashes":      hashes,
		"description": descriptions,
	}, nil
}

func runImpact(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("impact requires 1-2 arguments: [file] <symbol>")
	}
	depth := flagInt(flags, "depth", 3)

	// Resolve to get the file for accurate import filtering
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}
	symbolName := sym.Name

	type impactNode struct {
		Name  string `json:"name"`
		File  string `json:"file"`
		Type  string `json:"type"`
		Depth int    `json:"depth"`
	}

	type frontierItem struct {
		name string
		file string
	}

	seen := make(map[string]bool)
	var results []impactNode

	// BFS through callers
	frontier := []frontierItem{{name: symbolName, file: sym.File}}
	for d := 0; d < depth && len(frontier) > 0; d++ {
		var next []frontierItem
		for _, item := range frontier {
			callers, err := db.FindSemanticCallers(ctx, item.name, item.file)
			if err != nil || len(callers) == 0 {
				// Fall back to text-based references
				refs, err2 := index.FindReferencesInFile(ctx, db, item.name, item.file)
				if err2 == nil {
					allSyms, _ := db.AllSymbols(ctx)
					symMap := make(map[string][]index.SymbolInfo)
					for _, s := range allSyms {
						symMap[s.File] = append(symMap[s.File], s)
					}
					for _, ref := range refs {
						for _, s := range symMap[ref.File] {
							if ref.StartLine >= s.StartLine && ref.EndLine <= s.EndLine {
								key := s.File + ":" + s.Name
								if !seen[key] && s.Name != item.name {
									callers = append(callers, s)
								}
								break
							}
						}
					}
				}
			}
			for _, c := range callers {
				key := c.File + ":" + c.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				results = append(results, impactNode{
					Name:  c.Name,
					File:  output.Rel(c.File),
					Type:  c.Type,
					Depth: d + 1,
				})
				next = append(next, frontierItem{name: c.Name, file: c.File})
			}
		}
		frontier = next
	}

	return map[string]any{
		"symbol":    symbolName,
		"max_depth": depth,
		"impacted":  results,
		"total":     len(results),
	}, nil
}

func runCallChain(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 2 {
		return nil, fmt.Errorf("call-chain requires 2 arguments: <from-symbol> <to-symbol>")
	}
	from := args[0]
	to := args[1]
	maxDepth := flagInt(flags, "depth", 5)

	// Resolve 'to' to get its file for semantic lookup
	toSym, err := db.ResolveSymbol(ctx, to)
	if err != nil {
		return nil, fmt.Errorf("call-chain: cannot resolve %q: %w", to, err)
	}

	// BFS from 'to' backwards through callers to find 'from'
	type pathNode struct {
		name   string
		file   string
		parent int // index in visited
	}

	visited := []pathNode{{name: to, file: toSym.File, parent: -1}}
	seen := map[string]bool{to: true}
	found := -1

	for front := 0; front < len(visited) && found < 0; front++ {
		current := visited[front]
		depth := 0
		for p := front; p >= 0; p = visited[p].parent {
			depth++
		}
		if depth > maxDepth {
			break
		}

		callers, err := db.FindSemanticCallers(ctx, current.name, current.file)
		if err != nil || len(callers) == 0 {
			// Fall back to text-based
			refs, _ := index.FindReferencesInFile(ctx, db, current.name, current.file)
			allSyms, _ := db.AllSymbols(ctx)
			symMap := make(map[string][]index.SymbolInfo)
			for _, s := range allSyms {
				symMap[s.File] = append(symMap[s.File], s)
			}
			for _, ref := range refs {
				for _, s := range symMap[ref.File] {
					if ref.StartLine >= s.StartLine && ref.EndLine <= s.EndLine {
						callers = append(callers, s)
						break
					}
				}
			}
		}
		for _, c := range callers {
			if seen[c.Name] {
				continue
			}
			seen[c.Name] = true
			idx := len(visited)
			visited = append(visited, pathNode{name: c.Name, file: c.File, parent: front})
			if c.Name == from {
				found = idx
				break
			}
		}
	}

	if found < 0 {
		return map[string]any{
			"from":    from,
			"to":      to,
			"found":   false,
			"message": fmt.Sprintf("no call chain found from %s to %s within depth %d", from, to, maxDepth),
		}, nil
	}

	// Reconstruct path: from found (from) back to root (to)
	// This gives from → intermediate → to (call direction)
	var chain []string
	for idx := found; idx >= 0; idx = visited[idx].parent {
		chain = append(chain, visited[idx].name)
	}

	return map[string]any{
		"from":  from,
		"to":    to,
		"found": true,
		"chain": chain,
		"depth": len(chain) - 1,
	}, nil
}

func runVerify(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	command := flagString(flags, "command", "")
	if command == "" {
		// Auto-detect based on project files
		if _, err := os.Stat(root + "/go.mod"); err == nil {
			command = "go build ./..."
		} else if _, err := os.Stat(root + "/package.json"); err == nil {
			command = "npx tsc --noEmit"
		} else if _, err := os.Stat(root + "/Cargo.toml"); err == nil {
			command = "cargo check"
		} else {
			return nil, fmt.Errorf("verify: no command specified and could not auto-detect project type")
		}
	}

	timeout := flagInt(flags, "timeout", 30)
	cmdCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "sh", "-c", command)
	cmd.Dir = root
	// Inherit environment and set GOCACHE for sandboxed environments
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".edr", "gocache"))
	out, err := cmd.CombinedOutput()

	result := map[string]any{
		"command": command,
		"output":  string(out),
	}

	if err != nil {
		result["ok"] = false
		result["error"] = err.Error()
	} else {
		result["ok"] = true
	}

	return result, nil
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
