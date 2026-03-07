package dispatch

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
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
	// --- Unified commands ---
	case "read":
		return runReadUnified(ctx, db, root, args, flags)
	case "write":
		return runWriteUnified(ctx, db, root, args, flags)
	case "edit":
		return runSmartEdit(ctx, db, root, args, flags)
	case "map":
		return runMapUnified(ctx, db, root, args, flags)
	case "search":
		return runSearchUnified(ctx, db, args, flags)
	case "explore":
		return runExploreUnified(ctx, db, root, args, flags)
	case "refs":
		return runRefsUnified(ctx, db, root, args, flags)

	// --- Legacy aliases (still supported) ---
	case "init":
		return runInit(ctx, db)
	case "repo-map":
		return runRepoMap(ctx, db, flags)
	case "search-text":
		return runSearchText(ctx, db, args, flags)
	case "symbols":
		return runSymbols(ctx, db, root, args)
	case "read-symbol":
		return runReadSymbol(ctx, db, root, args, flags)
	case "read-file":
		return runReadFile(ctx, db, root, args, flags)
	case "expand":
		return runExpand(ctx, db, root, args, flags)
	case "xrefs":
		return runXrefs(ctx, db, root, args)
	case "gather":
		return runGather(ctx, db, root, args, flags)
	case "write-file":
		return runWriteFile(ctx, db, root, args, flags)
	case "rename-symbol", "rename":
		return runRenameSymbol(ctx, db, root, args, flags)
	case "insert-after":
		return runInsertAfter(ctx, db, root, args, flags)
	case "append-file":
		return runAppendFile(ctx, db, root, args, flags)
	case "smart-edit":
		return runSmartEdit(ctx, db, root, args, flags)
	case "find-files", "find":
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
		return nil, fmt.Errorf("read requires at least 1 argument: <file> [start] [end], [file] <symbol>, or <file>:<symbol> ...")
	}

	// Multiple args: check if it's a line range or batch
	if len(args) > 1 {
		// 2-3 args where second is numeric → file read with line range
		if _, err := strconv.Atoi(args[1]); err == nil {
			return runReadFile(ctx, db, root, args, flags)
		}
		// 2 args, second non-numeric, no colons → file + symbol
		if len(args) == 2 && !strings.Contains(args[0], ":") && !strings.Contains(args[1], ":") {
			return runReadSymbol(ctx, db, root, args, flags)
		}
		// Multiple args → batch read
		return runBatchRead(ctx, db, root, args, flags)
	}

	// Single arg with colon → file:symbol
	arg := args[0]
	if idx := strings.LastIndex(arg, ":"); idx > 0 && idx < len(arg)-1 {
		// Ensure it's not a Windows drive letter (C:\...)
		suffix := arg[idx+1:]
		if _, err := strconv.Atoi(suffix); err != nil {
			// Non-numeric after colon → symbol
			return runReadSymbol(ctx, db, root, []string{arg[:idx], suffix}, flags)
		}
	}

	// Single arg → file read
	return runReadFile(ctx, db, root, args, flags)
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
	if insideSymbol != "" {
		return runInsertInside(ctx, db, root, args[0], insideSymbol, flags)
	}

	afterSymbol := flagString(flags, "after", "")
	if afterSymbol != "" {
		return runInsertAfter(ctx, db, root, []string{args[0], afterSymbol}, flags)
	}

	if flagBool(flags, "append", false) {
		return runAppendFile(ctx, db, root, args, flags)
	}

	return runWriteFile(ctx, db, root, args, flags)
}

// runMapUnified routes to repo-map or symbols based on args.
//
//	map                                → repo-map
//	map file.go                        → symbols
func runMapUnified(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) > 0 {
		return runSymbols(ctx, db, root, args)
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
		flagString(flags, "include", "") != "" ||
		flagString(flags, "exclude", "") != "" ||
		flagInt(flags, "context", 0) > 0

	if isText {
		return runSearchText(ctx, db, args, flags)
	}
	return runSearch(ctx, db, args, flags)
}

// runExploreUnified routes to expand or gather based on flags.
//
//	explore symbol --body --callers    → expand (fine-grained)
//	explore symbol --gather            → gather (context bundle with tests)
func runExploreUnified(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if flagBool(flags, "gather", false) {
		return runGather(ctx, db, root, args, flags)
	}
	return runExpand(ctx, db, root, args, flags)
}

// runRefsUnified routes to xrefs, impact, or call-chain based on flags.
//
//	refs symbol                        → xrefs
//	refs symbol --impact               → impact (transitive callers)
//	refs symbol --chain targetSymbol   → call-chain
func runRefsUnified(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
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

// --- helpers ---

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

// editOK builds a successful EditResult with the file's new hash.
func editOK(file string, message string) output.EditResult {
	hash, _ := edit.FileHash(file)
	return output.EditResult{OK: true, File: output.Rel(file), Message: message, Hash: hash}
}

// --- flag helpers ---

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
	case string:
		return b == "true" || b == "1"
	default:
		return defaultVal
	}
}
