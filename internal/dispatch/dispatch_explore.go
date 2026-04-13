package dispatch

import (
	"context"
	"fmt"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
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
		callers := findCallersWithFallback(ctx, db, sym)
		for _, c := range callers {
			csym := toOutputSymbol(&c, "")
			if showSigs {
				csym.Signature = index.ExtractSignatureCtx(ctx, c)
			}
			result.Callers = append(result.Callers, csym)
		}
	}

	if showDeps {
		// Try ref graph first for deps.
		var deps []index.SymbolInfo
		edrDir := db.EdrDir()
		if rg := idx.ReadRefGraph(edrDir); rg != nil {
			allSyms, symFiles := idx.LoadAllSymbols(edrDir)
			if allSyms != nil {
				targetIDs := refGraphSymbolIDs(sym, allSyms, symFiles, root)
				for _, tid := range targetIDs {
					deps = append(deps, refGraphCallees(rg, tid, allSyms, symFiles, root)...)
				}
			}
		}
		// Fall back to text-based if ref graph didn't produce results.
		if len(deps) == 0 {
			deps, _ = index.FindDeps(ctx, db, sym)
		}
		for _, d := range deps {
			dsym := toOutputSymbol(&d, "")
			if showSigs {
				dsym.Signature = index.ExtractSignatureCtx(ctx, d)
			}
			result.Deps = append(result.Deps, dsym)
		}
	}

	return result, nil
}


