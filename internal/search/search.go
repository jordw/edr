package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// SearchResult wraps matches with truncation metadata.
type SearchResult struct {
	Matches      []output.Match `json:"matches"`
	TotalMatches int            `json:"total_matches"`
	Truncated    bool           `json:"truncated"`
}

// SearchSymbol searches the index for symbols matching a pattern.
// When showBody is true, each match includes a snippet of the symbol's source.
func SearchSymbol(ctx context.Context, db *index.DB, pattern string, budget int, showBody bool) (*SearchResult, error) {
	symbols, err := db.SearchSymbols(ctx, pattern)
	if err != nil {
		return nil, err
	}

	matches := make([]output.Match, 0)
	totalTokens := 0
	truncated := false
	for _, s := range symbols {
		size := int(s.EndByte-s.StartByte) / 4 // rough token estimate

		// Budget limits total matches when not showing body
		if !showBody && budget > 0 && totalTokens+size > budget {
			truncated = true
			break
		}

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

		if showBody && (budget == 0 || totalTokens+size <= budget) {
			src, err := os.ReadFile(s.File)
			if err == nil && int(s.EndByte) <= len(src) {
				body := string(src[s.StartByte:s.EndByte])
				if budget > 0 {
					remaining := (budget - totalTokens) * 4
					if remaining > 0 && remaining < len(body) {
						body = body[:remaining] + "\n... (trimmed to budget)"
					}
				}
				m.Body = body
				totalTokens += size
			}
		} else if showBody && budget > 0 && totalTokens+size > budget {
			// Over budget for body, still include metadata
			matches = append(matches, m)
			truncated = true
			break
		}

		matches = append(matches, m)
	}
	return &SearchResult{
		Matches:      matches,
		TotalMatches: len(symbols),
		Truncated:    truncated,
	}, nil
}

// searchTextConfig holds optional filters for SearchText.
type searchTextConfig struct {
	include []string // glob patterns to include (e.g. "*.go")
	exclude []string // glob patterns to exclude (e.g. "vendor/*")
	context int      // lines of context around each match
}

// SearchTextOption configures SearchText behavior.
type SearchTextOption func(*searchTextConfig)

// WithInclude filters results to files matching any of the given glob patterns.
func WithInclude(patterns ...string) SearchTextOption {
	return func(c *searchTextConfig) { c.include = append(c.include, patterns...) }
}

// WithExclude filters out files matching any of the given glob patterns.
func WithExclude(patterns ...string) SearchTextOption {
	return func(c *searchTextConfig) { c.exclude = append(c.exclude, patterns...) }
}

// WithContext adds N lines of context around each match.
func WithContext(n int) SearchTextOption {
	return func(c *searchTextConfig) { c.context = n }
}

func matchesAnyPath(base, rel string, patterns []string) bool {
	for _, p := range patterns {
		if strings.Contains(p, "**") {
			if matchDoublestar(rel, p) {
				return true
			}
			continue
		}
		if strings.Contains(p, "/") {
			if ok, _ := filepath.Match(p, rel); ok {
				return true
			}
		} else {
			if ok, _ := filepath.Match(p, base); ok {
				return true
			}
		}
	}
	return false
}

// matchDoublestar matches a path against a pattern with ** support.
func matchDoublestar(path, pattern string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	if len(parts) == 1 {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	prefix := parts[0]
	suffix := parts[1]
	if prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if !strings.HasPrefix(path, prefix+"/") && path != prefix {
			return false
		}
	}
	suffix = strings.TrimPrefix(suffix, "/")
	if suffix == "" {
		return true
	}
	if ok, _ := filepath.Match(suffix, filepath.Base(path)); ok {
		return true
	}
	for i := 0; i < len(path); i++ {
		if path[i] == '/' {
			if ok, _ := filepath.Match(suffix, path[i+1:]); ok {
				return true
			}
		}
	}
	return false
}

// SearchText searches file contents for a text pattern (like grep).
// It walks all repo files (not just indexed ones) so it finds matches in
// YAML, Markdown, Dockerfiles, etc. When useRegex is true, pattern is
// compiled as a Go regexp.
func SearchText(ctx context.Context, db *index.DB, pattern string, budget int, useRegex bool, opts ...SearchTextOption) (*SearchResult, error) {
	cfg := searchTextConfig{}
	for _, o := range opts {
		o(&cfg)
	}
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

	matches := make([]output.Match, 0)
	totalTokens := 0
	totalMatches := 0
	truncated := false

	err := index.WalkRepoFiles(root, func(file string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		rel, _ := filepath.Rel(root, file)
		base := filepath.Base(file)
		if len(cfg.include) > 0 && !matchesAnyPath(base, rel, cfg.include) {
			return nil
		}
		if len(cfg.exclude) > 0 && matchesAnyPath(base, rel, cfg.exclude) {
			return nil
		}

		data, err := os.ReadFile(file)
		if err != nil {
			return nil
		}
		allLines := strings.Split(string(data), "\n")

		for lineIdx, line := range allLines {
			lineNum := lineIdx + 1

			var matched bool
			if re != nil {
				matched = re.MatchString(line)
			} else {
				matched = strings.Contains(strings.ToLower(line), lowerPattern)
			}

			if matched {
				totalMatches++

				// Build display text with optional context
				displayName := strings.TrimSpace(line)
				displayStart := lineNum
				displayEnd := lineNum
				if cfg.context > 0 {
					ctxStart := lineIdx - cfg.context
					if ctxStart < 0 {
						ctxStart = 0
					}
					ctxEnd := lineIdx + cfg.context + 1
					if ctxEnd > len(allLines) {
						ctxEnd = len(allLines)
					}
					var ctxLines []string
					for i := ctxStart; i < ctxEnd; i++ {
						ctxLines = append(ctxLines, fmt.Sprintf("%d\t%s", i+1, allLines[i]))
					}
					displayName = strings.Join(ctxLines, "\n")
					displayStart = ctxStart + 1
					displayEnd = ctxEnd
				}

				size := len(displayName) / 4
				if size < 1 {
					size = 1
				}
				if budget > 0 && totalTokens+size > budget {
					truncated = true
					continue // keep counting total
				}
				totalTokens += size

				// Find column offset of match
				col := 0
				if re != nil {
					if loc := re.FindStringIndex(line); loc != nil {
						col = loc[0] + 1
					}
				} else {
					col = strings.Index(strings.ToLower(line), lowerPattern) + 1
				}

				matches = append(matches, output.Match{
					Symbol: output.Symbol{
						Type:  "text",
						Name:  displayName,
						File:  output.Rel(file),
						Lines: [2]int{displayStart, displayEnd},
						Size:  size,
					},
					Score:  0.5,
					Column: col,
				})
			}
		}
		return nil
	})

	return &SearchResult{
		Matches:      matches,
		TotalMatches: totalMatches,
		Truncated:    truncated,
	}, err
}
