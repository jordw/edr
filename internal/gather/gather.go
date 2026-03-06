package gather

import (
	"context"
	"os"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// Gather builds a minimal context bundle for a symbol.
func Gather(ctx context.Context, db *index.DB, file, symbolName string, budget int) (*output.GatherResult, error) {
	// Find target symbol
	sym, err := db.GetSymbol(ctx, file, symbolName)
	if err != nil {
		return nil, err
	}

	target := symbolToOutput(*sym)
	result := &output.GatherResult{
		Target:      target,
		TotalTokens: target.Size,
	}

	remaining := budget - target.Size
	if remaining <= 0 {
		return result, nil
	}

	// Find references (callers/deps) via the index
	refs, err := index.FindReferences(ctx, db, symbolName)
	if err != nil {
		return result, nil // non-fatal
	}

	// Classify refs: callers are symbols that contain a reference to our symbol
	// (but are not the symbol itself)
	allSymbols, _ := db.AllSymbols(ctx)
	symMap := make(map[string][]index.SymbolInfo) // file -> symbols
	for _, s := range allSymbols {
		symMap[s.File] = append(symMap[s.File], s)
	}

	seen := make(map[string]bool)
	for _, ref := range refs {
		if ref.File == file && ref.StartLine >= sym.StartLine && ref.EndLine <= sym.EndLine {
			continue // skip self-references
		}

		// Find which symbol contains this reference
		for _, s := range symMap[ref.File] {
			if ref.StartLine >= s.StartLine && ref.EndLine <= s.EndLine {
				key := s.File + ":" + s.Name
				if seen[key] {
					continue
				}
				seen[key] = true

				caller := symbolToOutput(s)
				if remaining-caller.Size < 0 {
					continue
				}
				remaining -= caller.Size
				result.Callers = append(result.Callers, caller)
				result.TotalTokens += caller.Size
			}
		}
	}

	// Find related tests
	if remaining > 0 {
		tests := FindRelatedTests(ctx, db, symbolName, file)
		for _, t := range tests {
			ts := symbolToOutput(t)
			if remaining-ts.Size < 0 {
				continue
			}
			remaining -= ts.Size
			result.Tests = append(result.Tests, ts)
			result.TotalTokens += ts.Size
		}
	}

	return result, nil
}

// GatherBySearch gathers context for a search query.
func GatherBySearch(ctx context.Context, db *index.DB, query string, budget int) (*output.GatherResult, error) {
	// Search for matching symbols
	symbols, err := db.SearchSymbols(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(symbols) == 0 {
		return &output.GatherResult{}, nil
	}

	// Use first match as target
	return Gather(ctx, db, symbols[0].File, symbols[0].Name, budget)
}

func symbolToOutput(s index.SymbolInfo) output.Symbol {
	size := int(s.EndByte-s.StartByte) / 4
	body := s.Body
	if body == "" {
		// Try to read from file
		if data, err := os.ReadFile(s.File); err == nil {
			if int(s.EndByte) <= len(data) {
				body = string(data[s.StartByte:s.EndByte])
			}
		}
	}

	return output.Symbol{
		Type:  s.Type,
		Name:  s.Name,
		File:  output.Rel(s.File),
		Lines: [2]int{int(s.StartLine), int(s.EndLine)},
		Size:  size,
	}
}
