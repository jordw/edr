package search

import (
	"bufio"
	"context"
	"os"
	"regexp"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// SearchSymbol searches the index for symbols matching a pattern.
// When showBody is true, each match includes a snippet of the symbol's source.
func SearchSymbol(ctx context.Context, db *index.DB, pattern string, budget int, showBody bool) ([]output.Match, error) {
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

		m := output.Match{
			Symbol: output.Symbol{
				Type:  s.Type,
				Name:  s.Name,
				File:  output.Rel(s.File),
				Lines: [2]int{int(s.StartLine), int(s.EndLine)},
				Size:  size,
			},
			Score: 1.0,
		}

		if showBody {
			src, err := os.ReadFile(s.File)
			if err == nil && int(s.EndByte) <= len(src) {
				body := string(src[s.StartByte:s.EndByte])
				// Truncate long bodies to keep output manageable
				if len(body) > 800 {
					body = body[:800] + "\n... (truncated)"
				}
				m.Body = body
			}
		}

		matches = append(matches, m)
	}
	return matches, nil
}

// SearchText searches file contents for a text pattern (like grep).
// It walks all repo files (not just indexed ones) so it finds matches in
// YAML, Markdown, Dockerfiles, etc. When useRegex is true, pattern is
// compiled as a Go regexp.
func SearchText(ctx context.Context, db *index.DB, pattern string, budget int, useRegex bool) ([]output.Match, error) {
	root := db.Root()

	var re *regexp.Regexp
	var lowerPattern string
	if useRegex {
		var err error
		re, err = regexp.Compile(pattern)
		if err != nil {
			return nil, err
		}
	} else {
		lowerPattern = strings.ToLower(pattern)
	}

	var matches []output.Match
	totalTokens := 0

	err := index.WalkRepoFiles(root, func(file string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		f, err := os.Open(file)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()

			var matched bool
			if re != nil {
				matched = re.MatchString(line)
			} else {
				matched = strings.Contains(strings.ToLower(line), lowerPattern)
			}

			if matched {
				size := len(line) / 4
				if size < 1 {
					size = 1
				}
				if budget > 0 && totalTokens+size > budget {
					return nil
				}
				totalTokens += size

				matches = append(matches, output.Match{
					Symbol: output.Symbol{
						Type:    "text",
						Name:    strings.TrimSpace(line),
						File:    output.Rel(file),
						Lines:   [2]int{lineNum, lineNum},
						Size:    size,
						Summary: strings.TrimSpace(line),
					},
					Score: 0.5,
				})
			}
		}
		return nil
	})

	return matches, err
}
