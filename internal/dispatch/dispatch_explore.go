package dispatch

import (
	"context"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/gather"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

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
		Symbol: toOutputSymbol(sym, hash),
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
								csym := toOutputSymbol(&s, "")
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
				csym := toOutputSymbol(&c, "")
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
				dsym := toOutputSymbol(&d, "")
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
