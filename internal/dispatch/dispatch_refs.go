package dispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// splitFileSymbol parses "file.go:Symbol" into [file, symbol].
// Returns nil if the string doesn't look like file:symbol.
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

func runImpact(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
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

	// Impact analysis requires import-aware refs (Go, Python, JS, TS)
	if !hasStrongRefs(sym.File) {
		return nil, fmt.Errorf("--impact requires Go, Python, JavaScript, or TypeScript (file: %s)", output.Rel(sym.File))
	}

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

func runCallChain(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	// Args come from runRefsUnified as [file, symbol, chain_target] or as
	// [from_spec, to_spec] when called directly. The last arg is always the
	// chain target; everything before it identifies the source symbol.
	if len(args) < 2 {
		return nil, fmt.Errorf("call-chain requires at least 2 arguments: <from-symbol> <to-symbol>")
	}
	to := args[len(args)-1]
	sourceArgs := args[:len(args)-1]
	maxDepth := flagInt(flags, "depth", 5)

	// Resolve source (from) symbol
	fromSym, err := resolveSymbolArgs(ctx, db, root, sourceArgs)
	if err != nil {
		return nil, fmt.Errorf("call-chain: cannot resolve source: %w", err)
	}
	from := fromSym.Name

	// Resolve target (to) symbol — supports file:symbol syntax
	var toSym *index.SymbolInfo
	if parts := splitFileSymbol(to); parts != nil {
		file, err := db.ResolvePath(parts[0])
		if err != nil {
			return nil, fmt.Errorf("call-chain: cannot resolve file %q: %w", parts[0], err)
		}
		toSym, err = db.GetSymbol(ctx, file, parts[1])
		if err != nil {
			return nil, fmt.Errorf("call-chain: cannot resolve %q in %s: %w", parts[1], parts[0], err)
		}
		to = parts[1]
	} else {
		var err error
		toSym, err = db.ResolveSymbol(ctx, to)
		if err != nil {
			return nil, fmt.Errorf("call-chain: cannot resolve %q: %w", to, err)
		}
	}

	// Call chain requires import-aware refs (Go, Python, JS, TS)
	if !hasStrongRefs(toSym.File) {
		return nil, fmt.Errorf("--chain requires Go, Python, JavaScript, or TypeScript (file: %s)", output.Rel(toSym.File))
	}

	type pathNode struct {
		name   string
		file   string
		depth  int
		parent int // index in visited
	}

	// Try BFS backward from both ends — the caller/callee relationship
	// between from and to is not known in advance.
	bfs := func(startName, startFile, targetName string) ([]pathNode, int) {
		visited := []pathNode{{name: startName, file: startFile, depth: 0, parent: -1}}
		seen := map[string]bool{startFile + ":" + startName: true}
		for front := 0; front < len(visited); front++ {
			cur := visited[front]
			if cur.depth >= maxDepth {
				continue
			}
			callers, _ := db.FindSemanticCallers(ctx, cur.name, cur.file)
			for _, c := range callers {
				key := c.File + ":" + c.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				idx := len(visited)
				visited = append(visited, pathNode{name: c.Name, file: c.File, depth: cur.depth + 1, parent: front})
				if c.Name == targetName {
					return visited, idx
				}
			}
		}
		return nil, -1
	}

	visited, found := bfs(to, toSym.File, from)
	if found < 0 {
		visited, found = bfs(from, fromSym.File, to)
	}

	if found < 0 {
		return map[string]any{
			"from":    from,
			"to":      to,
			"found":   false,
			"message": fmt.Sprintf("no call chain found between %s and %s within depth %d", from, to, maxDepth),
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

// hasStrongRefs returns true if the file is in a language with import-aware
// semantic references (Go, Python, JavaScript, TypeScript).
func hasStrongRefs(file string) bool {
	cfg := index.GetLangConfig(file)
	if cfg == nil {
		return false
	}
	switch cfg.LangID {
	case "go", "python", "javascript", "typescript":
		return true
	default:
		return false
	}
}
