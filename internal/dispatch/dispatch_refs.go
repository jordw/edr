package dispatch

import (
	"context"
	"fmt"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

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
