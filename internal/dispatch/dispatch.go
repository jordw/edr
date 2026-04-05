package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

var setRootOnce sync.Once

// resolveSymbolArgs resolves 1 or 2 args to a symbol.
// With 1 arg: global name resolution (errors if ambiguous).
// With 2 args: file + name lookup.
func resolveSymbolArgs(ctx context.Context, db index.SymbolStore, root string, args []string) (*index.SymbolInfo, error) {
	// Support "file.go:Symbol" colon syntax — expand to two args
	if len(args) == 1 {
		if parts := splitFileSymbol(args[0]); parts != nil {
			args = parts
		}
	}

	switch len(args) {
	case 1:
		sym, err := db.ResolveSymbol(ctx, args[0])
		if err != nil {
			return nil, err
		}
		return sym, nil
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
func Dispatch(ctx context.Context, db index.SymbolStore, cmd string, args []string, flags map[string]any) (any, error) {
	root := db.Root()
	setRootOnce.Do(func() { output.SetRoot(root) })

	var result any
	var err error

	switch cmd {
	// --- Primary commands ---
	case "orient", "map":
		result, err = runMapUnified(ctx, db, root, args, flags)
	case "focus", "read":
		result, err = runReadUnified(ctx, db, root, args, flags)
	case "edit":
		// If --content is set without --old, this is a write/create operation.
		if flagString(flags, "content", "") != "" && flagString(flags, "old_text", "") == "" {
			result, err = runWriteUnified(ctx, db, root, args, flags)
		} else {
			result, err = runSmartEdit(ctx, db, root, args, flags)
		}
	// --- Internal: used by edit routing and auto-verify ---
	case "write":
		result, err = runWriteUnified(ctx, db, root, args, flags)
	case "verify":
		result, err = runVerify(ctx, db, root, args, flags)
	case "index":
		result, err = runIndex(ctx, db, root, args, flags)
	default:
		if suggestion := suggestCommand(cmd); suggestion != "" {
			return nil, fmt.Errorf("unknown command: %s (did you mean: %s?)", cmd, suggestion)
		}
		return nil, fmt.Errorf("unknown command: %s", cmd)
	}

	if err != nil {
		return nil, relError(root, err)
	}
	return result, nil
}

// relError rewrites absolute repo paths in error messages to relative paths.
// It preserves the error chain so that errors.As still works on the result.
func relError(root string, err error) error {
	if err == nil || root == "" {
		return err
	}
	msg := err.Error()
	cleaned := strings.ReplaceAll(msg, root+"/", "")
	cleaned = strings.ReplaceAll(cleaned, root, "")
	if cleaned != msg {
		return &relPathError{msg: cleaned, wrapped: err}
	}
	return err
}

// relPathError wraps an error with a cleaned message while preserving the chain.
type relPathError struct {
	msg     string
	wrapped error
}

func (e *relPathError) Error() string { return e.msg }
func (e *relPathError) Unwrap() error { return e.wrapped }

// --- Unified command routers ---

// runReadUnified routes to read-file, read-symbol, or batch-read based on args.
//
//	read file.go                       → read-file
//	read file.go 10 50                 → read-file with line range
//	read file.go symbolName            → read-symbol (file + symbol)
//	read file.go:symbolName            → read-symbol (colon syntax)
//	read file.go file2.go              → batch-read (multiple files)
//	read file.go:sym file2.go:sym2     → batch-read (multiple file:symbol)
func runReadUnified(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("read requires at least 1 argument: <file>, <file>:<symbol>, or multiple files")
	}

	// Enforce --signatures and --skeleton are mutually exclusive
	if flagBool(flags, "signatures", false) && flagBool(flags, "skeleton", false) {
		return nil, fmt.Errorf("--signatures and --skeleton are mutually exclusive")
	}

	// --lines flag: parse "start:end" into start_line/end_line flags
	if linesStr := flagString(flags, "lines", ""); linesStr != "" {
		start, end, err := parseColonRange(linesStr)
		if err != nil {
			return nil, fmt.Errorf("--lines: %w", err)
		}
		flags["start_line"] = start
		flags["end_line"] = end
	}

	// Multiple args: check if it is a line range (compat) or batch
	if len(args) > 1 {
		// 2-3 args where second is numeric → file read with line range (compat)
		if _, err := strconv.Atoi(args[1]); err == nil {
			return runReadFile(ctx, db, root, args, flags)
		}
		// 2 args, second non-numeric, no colons → file+symbol (compat) or batch
		if len(args) == 2 && !strings.Contains(args[0], ":") && !strings.Contains(args[1], ":") {
			if looksLikeFilePath(args[1]) {
				return runBatchRead(ctx, db, root, args, flags)
			}
			return runReadSymbol(ctx, db, root, args, flags)
		}
		// Multiple args → batch read
		return runBatchRead(ctx, db, root, args, flags)
	}

	// Single arg with colon → file:symbol, file:N-M, or file:N:M (line range shortcuts)
	arg := args[0]
	if idx := strings.LastIndex(arg, ":"); idx > 0 && idx < len(arg)-1 {
		suffix := arg[idx+1:]
		if _, err := strconv.Atoi(suffix); err != nil {
			// Not a single number — try as line range (N-M)
			if start, end, rangeErr := parseColonRange(suffix); rangeErr == nil {
				flags["start_line"] = start
				flags["end_line"] = end
				return runReadFile(ctx, db, root, []string{arg[:idx]}, flags)
			}
			return runReadSymbol(ctx, db, root, []string{arg[:idx], suffix}, flags)
		}
		// Single number after last colon — check for file:N:M pattern (two colons)
		prefix := arg[:idx]
		if idx2 := strings.LastIndex(prefix, ":"); idx2 > 0 {
			startStr := prefix[idx2+1:]
			if start, err2 := strconv.Atoi(startStr); err2 == nil {
				end, _ := strconv.Atoi(suffix)
				flags["start_line"] = start
				flags["end_line"] = end
				return runReadFile(ctx, db, root, []string{prefix[:idx2]}, flags)
			}
		}
	}

	// Single arg: try as file first, fall back to symbol if it does not look like a path
	if !looksLikeFilePath(arg) && !strings.Contains(arg, "/") {
		resolved, resolveErr := db.ResolvePath(arg)
		if resolveErr != nil {
			return runReadSymbol(ctx, db, root, args, flags)
		}
		if _, statErr := os.Stat(resolved); statErr != nil {
			return runReadSymbol(ctx, db, root, args, flags)
		}
	}
	return runReadFile(ctx, db, root, args, flags)
}

// parseColonRange parses "start:end" into two ints.
func parseColonRange(s string) (int, int, error) {
	sep := ":"
	if !strings.Contains(s, ":") {
		sep = "-" // also accept N-M for compat
	}
	parts := strings.SplitN(s, sep, 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("expected start:end format, got %q", s)
	}
	start, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid start %q", parts[0])
	}
	end, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid end %q", parts[1])
	}
	return start, end, nil
}

// runWriteUnified routes to write-file, append-file, or insert-after based on flags.
//
//	write file.go                      → write-file (content in flags)
//	write file.go --append             → append-file
//	write file.go --after symbolName   → insert-after
func runWriteUnified(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("write requires at least 1 argument: <file>")
	}

	insideSymbol := flagString(flags, "inside", "")
	afterSymbol := flagString(flags, "after", "")
	appendMode := flagBool(flags, "append", false)

	// Enforce mutually exclusive placement flags.
	// --inside may combine with --after only when they refer to different symbols
	// (insert inside container after a child).
	if afterSymbol != "" && appendMode {
		return nil, fmt.Errorf("write: --after and --append are mutually exclusive")
	}
	if appendMode && insideSymbol != "" {
		return nil, fmt.Errorf("write: --append and --inside are mutually exclusive")
	}
	if insideSymbol != "" && afterSymbol != "" && insideSymbol == afterSymbol {
		return nil, fmt.Errorf("write: --inside and --after cannot refer to the same symbol %q", insideSymbol)
	}

	switch {
	case insideSymbol != "":
		return runInsertInside(ctx, db, root, args[0], insideSymbol, flags)
	case afterSymbol != "":
		return runInsertAfter(ctx, db, root, []string{args[0], afterSymbol}, flags)
	case appendMode:
		return runAppendFile(ctx, db, root, args, flags)
	default:
		return runWriteFile(ctx, db, root, args, flags)
	}
}

// runMapUnified routes to repo-map or symbols based on args.
//
//	map                                → repo-map
//	map dir/                           → repo-map scoped to dir
//	map file.go                        → symbols
const defaultMapBudget = 4000

func runRepoMap(ctx context.Context, db index.SymbolStore, flags map[string]any) (any, error) {
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
	if lang := flagString(flags, "lang", ""); lang != "" {
		opts = append(opts, index.WithLang(lang))
	}
	if search := flagString(flags, "search", ""); search != "" {
		opts = append(opts, index.WithSearch(search))
	}
	if !flagBool(flags, "locals", false) {
		opts = append(opts, index.WithHideLocals())
	}
	budget := flagInt(flags, "budget", 0)
	if budget == 0 && !flagBool(flags, "full", false) {
		budget = defaultMapBudget
	}
	if budget > 0 {
		opts = append(opts, index.WithBudget(budget))
	}
	_, stats, err := index.RepoMap(ctx, db, opts...)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"files":     stats.TotalFiles,
		"symbols":   stats.TotalSymbols,
		"content":   stats.Files,
		"truncated": stats.Truncated,
	}
	if stats.TotalFiles == 0 && stats.TotalSymbols == 0 {
		grep := flagString(flags, "grep", "")
		dir := flagString(flags, "dir", "")
		lang := flagString(flags, "lang", "")
		symType := flagString(flags, "type", "")
		if grep != "" || dir != "" || lang != "" || symType != "" {
			parts := []string{}
			if grep != "" { parts = append(parts, "--grep "+grep) }
			if dir != "" { parts = append(parts, "--dir "+dir) }
			if lang != "" { parts = append(parts, "--lang "+lang) }
			if symType != "" { parts = append(parts, "--type "+symType) }
			result["hint"] = "no symbols matched filters: " + strings.Join(parts, ", ")
		}
	}
	if stats.Truncated {
		result["shown_files"] = stats.ShownFiles
		result["shown_symbols"] = stats.ShownSymbols
		result["hint"] = "use --dir, --type, --lang, or --grep to narrow scope"
		if stats.BudgetUsed > 0 {
			result["budget_used"] = stats.BudgetUsed
		}
		if len(stats.DirSummary) > 0 {
			result["dirs"] = stats.DirSummary
			result["hint"] = "repo too large for full map; use --dir <name> to drill into a directory"
			// Keep content (individual symbols) alongside dir summary
			// so the agent sees both structure and some symbols
		}
	}
	return result, nil
}

func runMapUnified(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	if len(args) > 0 {
		// If the arg is a directory, treat it as --dir for repo-map.
		// Explicit --dir takes priority over positional arg.
		resolved := args[0]
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, resolved)
		}
		if info, err := os.Stat(resolved); err == nil && info.IsDir() {
			if flags == nil {
				flags = map[string]any{}
			}
			if flagString(flags, "dir", "") == "" {
				flags["dir"] = args[0]
			}
			return runRepoMap(ctx, db, flags)
		}
		return runSymbols(ctx, db, root, args, flags)
	}
	return runRepoMap(ctx, db, flags)
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

// MultiResults is returned by Dispatch when a single command expands into
// multiple ops (e.g. standalone multi-file read). The caller should add
// each result as a separate op on the envelope.
type MultiResults []MultiResult

// DispatchMulti runs multiple commands concurrently where safe.
// Commands targeting different files run in parallel. Commands targeting
// the same file run sequentially in their original order. Global-mutating
// commands (init, rename, edit-plan) force fully sequential execution.
func DispatchMulti(ctx context.Context, db index.SymbolStore, commands []MultiCmd, topBudget ...int) []MultiResult {
	// Inject per-request source cache if not already present.
	ctx = index.WithSourceCache(ctx)

	results := make([]MultiResult, len(commands))

	// Normalize nil args/flags
	for i := range commands {
		if commands[i].Args == nil {
			commands[i].Args = []string{}
		}
		if commands[i].Flags == nil {
			commands[i].Flags = map[string]any{}
		}
	}

	// Distribute top-level budget to sub-commands that don't have one.
	// Use 2*budget/(n+1) instead of budget/n so each command gets a larger
	// share — failed or small reads won't waste their allocation, and the
	// slight overshoot is acceptable since budgets are soft truncation limits.
	if len(topBudget) > 0 && topBudget[0] > 0 && len(commands) > 0 {
		n := len(commands)
		perCmd := topBudget[0] * 2 / (n + 1)
		if perCmd > topBudget[0] {
			perCmd = topBudget[0]
		}
		if perCmd < 50 {
			perCmd = 50
		}
		for i := range commands {
			if _, has := commands[i].Flags["budget"]; !has {
				commands[i].Flags["budget"] = perCmd
			}
		}
	}

	// If any command is global-mutating, fall back to fully sequential
	for _, c := range commands {
		if isGlobalMutating(c.Cmd) {
			dispatchSequential(ctx, db, commands, results)
			return results
		}
	}

	// Group commands by target file. Commands with no file target (global reads
	// like map, search) get an empty key and can all run in parallel.
	type indexedCmd struct {
		index int
		cmd   MultiCmd
	}
	groups := make(map[string][]indexedCmd)
	for i, c := range commands {
		key := commandFileKey(c.Cmd, c.Args)
		groups[key] = append(groups[key], indexedCmd{index: i, cmd: c})
	}

	// If everything lands in one group, no benefit from parallelism
	if len(groups) == 1 {
		dispatchSequential(ctx, db, commands, results)
		return results
	}

	// Run each file-group as a goroutine; within a group, commands run sequentially.
	// Global-read commands (empty key) each get their own goroutine.
	var wg sync.WaitGroup
	for key, group := range groups {
		if key == "" {
			// Global reads are independent — fan out individually
			for _, ic := range group {
				wg.Add(1)
				go func(ic indexedCmd) {
					defer wg.Done()
					result, err := Dispatch(ctx, db, ic.cmd.Cmd, ic.cmd.Args, ic.cmd.Flags)
					if err != nil {
						results[ic.index] = MultiResult{Cmd: ic.cmd.Cmd, OK: false, Error: err.Error()}
					} else {
						results[ic.index] = MultiResult{Cmd: ic.cmd.Cmd, OK: true, Result: result}
					}
				}(ic)
			}
		} else {
			// Same-file commands run sequentially within their goroutine
			wg.Add(1)
			go func(group []indexedCmd) {
				defer wg.Done()
				for _, ic := range group {
					result, err := Dispatch(ctx, db, ic.cmd.Cmd, ic.cmd.Args, ic.cmd.Flags)
					if err != nil {
						results[ic.index] = MultiResult{Cmd: ic.cmd.Cmd, OK: false, Error: err.Error()}
					} else {
						results[ic.index] = MultiResult{Cmd: ic.cmd.Cmd, OK: true, Result: result}
					}
				}
			}(group)
		}
	}
	wg.Wait()
	return results
}

func dispatchSequential(ctx context.Context, db index.SymbolStore, commands []MultiCmd, results []MultiResult) {
	for i, c := range commands {
		result, err := Dispatch(ctx, db, c.Cmd, c.Args, c.Flags)
		if err != nil {
			results[i] = MultiResult{Cmd: c.Cmd, OK: false, Error: err.Error()}
		} else {
			results[i] = MultiResult{Cmd: c.Cmd, OK: true, Result: result}
		}
	}
}

// isGlobalMutating returns true for commands that mutate global state
// (index, multiple files) and cannot safely run alongside anything else.
func isGlobalMutating(cmd string) bool {
	return cmdspec.IsGlobalMutating(cmd)
}

// commandFileKey extracts the target file from a command's args.
// Returns "" for global/fileless commands so they can run fully in parallel.
func commandFileKey(cmd string, args []string) string {
	if !cmdspec.IsFileScoped(cmd) {
		return ""
	}
	if len(args) > 0 {
		file := args[0]
		if idx := strings.IndexByte(file, ':'); idx > 0 {
			file = file[:idx]
		}
		return file
	}
	return ""
}

// --- helpers ---

// looksLikeFilePath returns true if the argument looks like a file path
// rather than a symbol name (has path separators or a file extension).
func looksLikeFilePath(arg string) bool {
	if strings.Contains(arg, "/") || strings.Contains(arg, string(filepath.Separator)) {
		return true
	}
	ext := filepath.Ext(arg)
	if ext == "" {
		return false
	}
	// Common source/config file extensions
	switch strings.ToLower(ext) {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".c", ".h", ".rs", ".java",
		".rb", ".yaml", ".yml", ".json", ".toml", ".md", ".txt", ".css", ".html",
		".xml", ".sh", ".bash", ".zsh", ".sql", ".proto", ".graphql", ".vue",
		".svelte", ".swift", ".kt", ".scala", ".php", ".lua", ".zig", ".cs",
		".cpp", ".cc", ".hpp", ".hh", ".m", ".mm", ".r", ".jl", ".ex", ".exs",
		".erl", ".hs", ".ml", ".mli", ".clj", ".cljs", ".dart", ".groovy",
		".tf", ".cfg", ".ini", ".env", ".lock", ".sum", ".mod":
		return true
	}
	return false
}

// splitFileSymbol splits "file:Symbol" into [file, symbol], or returns nil.
func splitFileSymbol(s string) []string {
	idx := strings.LastIndex(s, ":")
	if idx <= 0 || idx == len(s)-1 {
		return nil
	}
	file, sym := s[:idx], s[idx+1:]
	if !looksLikeFilePath(file) {
		return nil
	}
	return []string{file, sym}
}

// toOutputSymbol converts an index.SymbolInfo to output.Symbol.
func toOutputSymbol(sym *index.SymbolInfo, hash string) output.Symbol {
	return output.Symbol{
		Type:  sym.Type,
		Name:  sym.Name,
		File:  output.Rel(sym.File),
		Lines: [2]int{int(sym.StartLine), int(sym.EndLine)},
		Size:  int(sym.EndByte-sym.StartByte) / 4,
		Hash:  hash,
	}
}

type resolvedEdit struct {
	File        string
	StartByte   uint32
	EndByte     uint32
	Replacement string
	ExpectHash  string
	Description string
}

type commitResult struct {
	Hashes      map[string]string
	IndexErrors map[string]string
	FileCount   int
	EditCount   int
	Status      string // "applied", "applied_index_stale"
}

// commitEdits applies edits via Transaction and invalidates caches for affected files.
// The Transaction is atomic: all files are validated and transformed in memory
// first, then written via temp-file-then-rename with rollback on failure.
func commitEdits(ctx context.Context, db index.SymbolStore, edits []resolvedEdit) (*commitResult, error) {
	tx := edit.NewTransaction()
	for _, r := range edits {
		tx.Add(r.File, r.StartByte, r.EndByte, r.Replacement, r.ExpectHash)
	}

	fileSet := make(map[string]bool)
	for _, r := range edits {
		fileSet[r.File] = true
	}
	var fileList []string
	for f := range fileSet {
		fileList = append(fileList, f)
	}

	// Hold writer lock across commit to prevent concurrent
	// same-file edit races.
	var indexErrors map[string]string
	if lockErr := db.WithWriteLock(func() error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return nil
	}); lockErr != nil {
		return nil, lockErr
	}
	// Invalidate caches for edited files.
	if err := db.InvalidateFiles(ctx, fileList); err != nil {
		// Non-fatal: edits already applied.
		for _, f := range fileList {
			if indexErrors == nil {
				indexErrors = make(map[string]string)
			}
			indexErrors[output.Rel(f)] = err.Error()
		}
	}

	hashes := make(map[string]string)
	for f := range fileSet {
		if h, err := edit.FileHash(f); err == nil {
			hashes[output.Rel(f)] = h
		}
	}

	status := "applied"
	if len(indexErrors) > 0 {
		status = "applied_index_stale"
	}

	return &commitResult{
		Hashes:      hashes,
		IndexErrors: indexErrors,
		FileCount:   len(fileSet),
		EditCount:   len(edits),
		Status:      status,
	}, nil
}

// --- flag helpers ---

// flagLookup finds a value in the flags map, trying the given key first,
// then the alternate form (hyphens ↔ underscores). This lets callers use
// either "dry-run" or "dry_run", "old_text" or "old-text", etc.
func flagLookup(flags map[string]any, key string) (any, bool) {
	if flags == nil {
		return nil, false
	}
	if v, ok := flags[key]; ok {
		return v, true
	}
	// Try alternate form: swap hyphens and underscores
	var alt string
	if strings.Contains(key, "-") {
		alt = strings.ReplaceAll(key, "-", "_")
	} else if strings.Contains(key, "_") {
		alt = strings.ReplaceAll(key, "_", "-")
	} else {
		return nil, false
	}
	if v, ok := flags[alt]; ok {
		return v, true
	}
	return nil, false
}

func flagString(flags map[string]any, key string, defaultVal string) string {
	v, ok := flagLookup(flags, key)
	if !ok {
		return defaultVal
	}
	if s, ok := v.(string); ok {
		return s
	}
	return defaultVal
}

func flagInt(flags map[string]any, key string, defaultVal int) int {
	v, ok := flagLookup(flags, key)
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
	v, ok := flagLookup(flags, key)
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
	v, ok := flagLookup(flags, key)
	if !ok {
		return defaultVal
	}
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return b == "true" || b == "1"
	default:
		return defaultVal
	}
}

// suggestCommand returns the closest known command if within edit distance 2, or "".
func suggestCommand(input string) string {
	best := ""
	bestDist := 3
	for _, cmd := range cmdspec.Names() {
		d := cmdspec.Levenshtein(input, cmd)
		if d < bestDist {
			bestDist = d
			best = cmd
		}
	}
	return best
}
