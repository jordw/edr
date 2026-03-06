package gather

import (
	"context"
	"os"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// Gather builds a minimal context bundle for a symbol.
// When includeBody is true, inlines source bodies for target, callers, and tests.
func Gather(ctx context.Context, db *index.DB, file, symbolName string, budget int, includeBody bool) (*output.GatherResult, error) {
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

	// Include target body
	if includeBody {
		body := readSymbolBody(*sym)
		if body != "" {
			result.TargetBody = body
		}
	}

	if remaining <= 0 {
		result.Truncated = true
		return result, nil
	}

	// Collect all candidate callers
	var allCallers []index.SymbolInfo
	callers, err := db.FindSemanticCallers(ctx, symbolName, file)
	if err != nil || len(callers) == 0 {
		// Fallback to text-based
		refs, _ := index.FindReferencesInFile(ctx, db, symbolName, file)
		allSymbols, _ := db.AllSymbols(ctx)
		symMap := make(map[string][]index.SymbolInfo)
		for _, s := range allSymbols {
			symMap[s.File] = append(symMap[s.File], s)
		}
		seen := make(map[string]bool)
		for _, ref := range refs {
			if ref.File == file && ref.StartLine >= sym.StartLine && ref.EndLine <= sym.EndLine {
				continue
			}
			for _, s := range symMap[ref.File] {
				if ref.StartLine >= s.StartLine && ref.EndLine <= s.EndLine {
					key := s.File + ":" + s.Name
					if !seen[key] {
						seen[key] = true
						allCallers = append(allCallers, s)
					}
				}
			}
		}
	} else {
		allCallers = callers
	}

	// Add callers that fit in budget; track omitted ones
	const sigTokens = 10 // approximate tokens for a signature-only entry
	for _, c := range allCallers {
		caller := symbolToOutput(c)
		if remaining-caller.Size < 0 {
			// Still include signature if space for that
			if remaining >= sigTokens {
				caller.Size = sigTokens
				remaining -= sigTokens
				result.Callers = append(result.Callers, caller)
				result.TotalTokens += sigTokens
			} else {
				result.OmittedCallers++
			}
			continue
		}
		remaining -= caller.Size
		result.Callers = append(result.Callers, caller)
		result.TotalTokens += caller.Size
		if includeBody {
			if result.CallerSnips == nil {
				result.CallerSnips = make(map[string]string)
			}
			result.CallerSnips[c.Name] = readSymbolBody(c)
		}
	}

	// Find related tests, prioritizing actual test functions
	allTests := FindRelatedTests(ctx, db, symbolName, file)
	testFuncs, testHelpers := partitionTests(allTests)
	orderedTests := append(testFuncs, testHelpers...)

	for _, t := range orderedTests {
		ts := symbolToOutput(t)
		if remaining-ts.Size < 0 {
			if remaining >= sigTokens {
				ts.Size = sigTokens
				remaining -= sigTokens
				result.Tests = append(result.Tests, ts)
				result.TotalTokens += sigTokens
			} else {
				result.OmittedTests++
			}
			continue
		}
		remaining -= ts.Size
		result.Tests = append(result.Tests, ts)
		result.TotalTokens += ts.Size
		if includeBody {
			if result.TestSnips == nil {
				result.TestSnips = make(map[string]string)
			}
			result.TestSnips[t.Name] = readSymbolBody(t)
		}
	}

	if result.OmittedCallers > 0 || result.OmittedTests > 0 {
		result.Truncated = true
	}

	return result, nil
}

// GatherBySearch gathers context for a search query.
func GatherBySearch(ctx context.Context, db *index.DB, query string, budget int, includeBody bool) (*output.GatherResult, error) {
	// Search for matching symbols
	symbols, err := db.SearchSymbols(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(symbols) == 0 {
		return &output.GatherResult{}, nil
	}

	// Use first match as target
	return Gather(ctx, db, symbols[0].File, symbols[0].Name, budget, includeBody)
}

func readSymbolBody(s index.SymbolInfo) string {
	data, err := os.ReadFile(s.File)
	if err != nil {
		return ""
	}
	if int(s.EndByte) <= len(data) {
		return string(data[s.StartByte:s.EndByte])
	}
	return ""
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
