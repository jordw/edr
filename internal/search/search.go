package search

import (
	"bufio"
	"context"
	"os"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// SearchSymbol searches the index for symbols matching a pattern.
func SearchSymbol(ctx context.Context, db *index.DB, pattern string, budget int) ([]output.Match, error) {
	symbols, err := db.SearchSymbols(ctx, pattern)
	if err != nil {
		return nil, err
	}

	var matches []output.Match
	totalTokens := 0
	for _, s := range symbols {
		size := int(s.EndByte-s.StartByte) / 4 // rough token estimate
		if budget > 0 && totalTokens+size > budget {
			break
		}
		totalTokens += size

		matches = append(matches, output.Match{
			Symbol: output.Symbol{
				Type:  s.Type,
				Name:  s.Name,
				File:  s.File,
				Lines: [2]int{int(s.StartLine), int(s.EndLine)},
				Size:  size,
			},
			Score: 1.0, // exact match
		})
	}
	return matches, nil
}

// SearchText searches file contents for a text pattern (like grep).
func SearchText(ctx context.Context, db *index.DB, pattern string, budget int) ([]output.Match, error) {
	// Get all indexed files
	symbols, err := db.AllSymbols(ctx)
	if err != nil {
		return nil, err
	}

	// Collect unique files
	fileSet := make(map[string]bool)
	for _, s := range symbols {
		fileSet[s.File] = true
	}

	var matches []output.Match
	totalTokens := 0
	lowerPattern := strings.ToLower(pattern)

	for file := range fileSet {
		if ctx.Err() != nil {
			break
		}

		f, err := os.Open(file)
		if err != nil {
			continue
		}

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if strings.Contains(strings.ToLower(line), lowerPattern) {
				size := len(line) / 4
				if budget > 0 && totalTokens+size > budget {
					f.Close()
					return matches, nil
				}
				totalTokens += size

				matches = append(matches, output.Match{
					Symbol: output.Symbol{
						Type:    "text",
						Name:    strings.TrimSpace(line),
						File:    file,
						Lines:   [2]int{lineNum, lineNum},
						Size:    size,
						Summary: strings.TrimSpace(line),
					},
					Score: 0.5,
				})
			}
		}
		f.Close()
	}

	return matches, nil
}
