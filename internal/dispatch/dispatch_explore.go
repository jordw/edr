package dispatch

import (
	"context"
	"fmt"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runExpand(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
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
		result.Symbol.Signature = index.ExtractSignatureCtx(ctx, *sym)
	}

	if showBody {
		src, err := index.CachedReadFile(ctx, sym.File)
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
									csym.Signature = index.ExtractSignatureCtx(ctx, s)
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
					csym.Signature = index.ExtractSignatureCtx(ctx, c)
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
					dsym.Signature = index.ExtractSignatureCtx(ctx, d)
				}
				result.Deps = append(result.Deps, dsym)
			}
		}
	}

	return result, nil
}


