package search

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
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

	matches := make([]output.Match, 0)
	totalTokens := 0
	for _, s := range symbols {
		size := int(s.EndByte-s.StartByte) / 4 // rough token estimate

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
		}

		matches = append(matches, m)

		// Budget limits total matches when not showing body
		if !showBody && budget > 0 && totalTokens+size > budget {
			// Still include this match (metadata is cheap), but stop after
			totalTokens += size
			if totalTokens > budget {
				break
			}
		}
	}
	return matches, nil
}

// searchTextConfig holds optional filters for SearchText.
type searchTextConfig struct {
	include []string // glob patterns to include (e.g. "*.go")
	exclude []string // glob patterns to exclude (e.g. "vendor/*")
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
func SearchText(ctx context.Context, db *index.DB, pattern string, budget int, useRegex bool, opts ...SearchTextOption) ([]output.Match, error) {
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
						Name:  strings.TrimSpace(line),
						File:  output.Rel(file),
						Lines: [2]int{lineNum, lineNum},
						Size:  size,
					},
					Score:  0.5,
					Column: col,
				})
			}
		}
		return nil
	})

	return matches, err
}
