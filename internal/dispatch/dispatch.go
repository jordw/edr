package dispatch

import (
	"context"
	"fmt"
	"os"

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
		file := args[0]
		if len(file) > 0 && file[0] != '/' {
			file = root + "/" + file
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

	switch cmd {
	case "init":
		return runInit(ctx, db)
	case "repo-map":
		return runRepoMap(ctx, db)
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

func runRepoMap(ctx context.Context, db *index.DB) (any, error) {
	repoMap, err := index.RepoMap(ctx, db)
	if err != nil {
		return nil, err
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
	return search.SearchSymbol(ctx, db, args[0], budget)
}

func runSearchText(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("search-text requires 1 argument: <pattern>")
	}
	budget := flagInt(flags, "budget", 0)
	return search.SearchText(ctx, db, args[0], budget)
}

func runSymbols(ctx context.Context, db *index.DB, root string, args []string) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("symbols requires 1 argument: <file>")
	}
	file := args[0]
	if len(file) > 0 && file[0] != '/' {
		file = root + "/" + file
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
			File:  s.File,
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
			File:  sym.File,
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

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	hash, _ := edit.FileHash(sym.File)
	result := output.ExpandResult{
		Symbol: output.Symbol{
			Type:  sym.Type,
			Name:  sym.Name,
			File:  sym.File,
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
		result.Body = string(src[sym.StartByte:sym.EndByte])
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
								File:  s.File,
								Lines: [2]int{int(s.StartLine), int(s.EndLine)},
								Size:  int(s.EndByte-s.StartByte) / 4,
							})
						}
					}
				}
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
			File:  r.File,
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
