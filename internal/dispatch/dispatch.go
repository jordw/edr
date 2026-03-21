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
func resolveSymbolArgs(ctx context.Context, db *index.DB, root string, args []string) (*index.SymbolInfo, error) {
	// Support "file.go:Symbol" colon syntax — expand to two args
	if len(args) == 1 {
		if parts := splitFileSymbol(args[0]); parts != nil {
			args = parts
		}
	}

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
	setRootOnce.Do(func() { output.SetRoot(root) })

	var result any
	var err error

	switch cmd {
	// --- Unified commands ---
	case "read":
		result, err = runReadUnified(ctx, db, root, args, flags)
	case "write":
		result, err = runWriteUnified(ctx, db, root, args, flags)
	case "edit":
		result, err = runSmartEdit(ctx, db, root, args, flags)
	case "map":
		result, err = runMapUnified(ctx, db, root, args, flags)
	case "search":
		result, err = runSearchUnified(ctx, db, args, flags)
	case "refs":
		result, err = runRefsUnified(ctx, db, root, args, flags)

	case "reindex":
		result, err = runInit(ctx, db)
	case "rename":
		result, err = runRenameSymbol(ctx, db, root, args, flags)
	case "verify":
		result, err = runVerify(ctx, db, root, args, flags)
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
func runReadUnified(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
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

	// Single arg with colon → file:symbol (canonical syntax)
	arg := args[0]
	if idx := strings.LastIndex(arg, ":"); idx > 0 && idx < len(arg)-1 {
		suffix := arg[idx+1:]
		if _, err := strconv.Atoi(suffix); err != nil {
			return runReadSymbol(ctx, db, root, []string{arg[:idx], suffix}, flags)
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
func runWriteUnified(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("write requires at least 1 argument: <file>")
	}

	insideSymbol := flagString(flags, "inside", "")
	afterSymbol := flagString(flags, "after", "")
	appendMode := flagBool(flags, "append", false)

	// Enforce: --after and --append are mutually exclusive top-level modes.
	// --inside may combine with --after (insert inside container after a child).
	if afterSymbol != "" && appendMode {
		return nil, fmt.Errorf("write: --after and --append are mutually exclusive")
	}
	if insideSymbol == "" && afterSymbol != "" && appendMode {
		return nil, fmt.Errorf("write: --after and --append are mutually exclusive")
	}
	if appendMode && insideSymbol != "" {
		return nil, fmt.Errorf("write: --append and --inside are mutually exclusive")
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
func runMapUnified(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) > 0 {
		// If the arg is a directory, treat it as --dir for repo-map
		resolved := args[0]
		if !filepath.IsAbs(resolved) {
			resolved = filepath.Join(root, resolved)
		}
		if info, err := os.Stat(resolved); err == nil && info.IsDir() {
			if flags == nil {
				flags = map[string]any{}
			}
			flags["dir"] = args[0]
			return runRepoMap(ctx, db, flags)
		}
		return runSymbols(ctx, db, root, args, flags)
	}
	return runRepoMap(ctx, db, flags)
}

// runSearchUnified routes to symbol search or text search based on flags.
//
//	search pattern                     → symbol search
//	search pattern --text              → text search
//	search pattern --regex             → text search (auto-detected)
//	search pattern --include "*.go"    → text search (auto-detected)
func runSearchUnified(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	isText := flagBool(flags, "text", false) ||
		flagBool(flags, "regex", false) ||
		flagString(flags, "include", "") != "" || len(flagStringSlice(flags, "include")) > 0 ||
		flagString(flags, "exclude", "") != "" || len(flagStringSlice(flags, "exclude")) > 0 ||
		flagInt(flags, "context", 0) > 0

	if isText {
		return runSearchText(ctx, db, args, flags)
	}
	return runSearch(ctx, db, args, flags)
}

// runExploreUnified routes to expand or gather based on flags.
//
// runRefsUnified routes to xrefs, impact, call-chain, or expand based on flags.
//
//	refs symbol                        → xrefs
//	refs symbol --impact               → impact (transitive callers)
//	refs symbol --chain targetSymbol   → call-chain
//	refs symbol --callers              → expand (callers context)
//	refs symbol --deps                 → expand (deps context)
func runRefsUnified(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	// --callers/--deps: delegate to expand (formerly explore)
	if flagBool(flags, "callers", false) || flagBool(flags, "deps", false) {
		return runExpand(ctx, db, root, args, flags)
	}

	if flagBool(flags, "impact", false) {
		return runImpact(ctx, db, root, args, flags)
	}

	chainTarget := flagString(flags, "chain", "")
	if chainTarget != "" {
		newArgs := append(args, chainTarget)
		return runCallChain(ctx, db, root, newArgs, flags)
	}

	return runXrefs(ctx, db, root, args)
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
func DispatchMulti(ctx context.Context, db *index.DB, commands []MultiCmd, topBudget ...int) []MultiResult {
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

func dispatchSequential(ctx context.Context, db *index.DB, commands []MultiCmd, results []MultiResult) {
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

// --- individual command handlers ---

func runInit(ctx context.Context, db *index.DB) (any, error) {
	index.ClearTreeCache()
	var filesChanged, symbolsChanged int
	err := db.WithWriteLock(func() error {
		var e error
		filesChanged, symbolsChanged, e = index.IndexRepo(ctx, db)
		return e
	})
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

// commitEdits applies edits via Transaction and reindexes all affected files.
// The Transaction is atomic: all files are validated and transformed in memory
// first, then written via temp-file-then-rename with rollback on failure.
func commitEdits(ctx context.Context, db *index.DB, edits []resolvedEdit) (*commitResult, error) {
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

	// Hold writer lock across both commit and reindex to prevent concurrent
	// same-file edit races.
	var indexErrors map[string]string
	if lockErr := db.WithWriteLock(func() error {
		if err := tx.Commit(); err != nil {
			return err
		}
		// Batch all reindex SQLite writes into a single transaction.
		if err := db.BeginBatch(ctx); err == nil {
			defer db.RollbackBatch() // no-op after CommitBatch
		}
		for _, file := range fileList {
			if err := index.IndexFile(ctx, db, file); err != nil {
				if indexErrors == nil {
					indexErrors = make(map[string]string)
				}
				indexErrors[output.Rel(file)] = err.Error()
			}
		}
		if err := db.CommitBatch(); err != nil {
			return fmt.Errorf("commit reindex batch: %w", err)
		}
		return nil
	}); lockErr != nil {
		return nil, lockErr
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
	bestDist := 3 // only suggest if distance <= 2
	for _, cmd := range cmdspec.Names() {
		d := levenshtein(input, cmd)
		if d < bestDist {
			bestDist = d
			best = cmd
		}
	}
	return best
}

// levenshtein computes the edit distance between two strings.
func levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
