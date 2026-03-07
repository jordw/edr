package search

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// SearchResult wraps matches with truncation metadata.
type SearchResult struct {
	Kind         string         `json:"kind"` // "symbol" or "text"
	Matches      []output.Match `json:"matches"`
	TotalMatches int            `json:"total_matches"`
	Truncated    bool           `json:"truncated"`
}

// scoreSymbolMatch scores how well a symbol name matches a search pattern.
// Higher scores indicate better matches.
func scoreSymbolMatch(symbolName, pattern string) float64 {
	lowerName := strings.ToLower(symbolName)
	lowerPattern := strings.ToLower(pattern)

	// Exact match
	if symbolName == pattern {
		return 1.0
	}
	// Case-insensitive exact match
	if lowerName == lowerPattern {
		return 0.95
	}
	// Prefix match (case-sensitive)
	if strings.HasPrefix(symbolName, pattern) {
		return 0.8
	}
	// Case-insensitive prefix
	if strings.HasPrefix(lowerName, lowerPattern) {
		return 0.75
	}
	// Suffix match (case-sensitive) — catches "parseConfig" when searching "Config"
	if strings.HasSuffix(symbolName, pattern) {
		return 0.7
	}
	// Case-insensitive suffix
	if strings.HasSuffix(lowerName, lowerPattern) {
		return 0.65
	}
	// Contains (already filtered by DB query, so this is the fallback)
	return 0.5
}

// SearchSymbol searches the index for symbols matching a pattern.
// When showBody is true, each match includes a snippet of the symbol's source.
func SearchSymbol(ctx context.Context, db *index.DB, pattern string, budget int, showBody bool) (*SearchResult, error) {
	symbols, err := db.SearchSymbols(ctx, pattern)
	if err != nil {
		return nil, err
	}

	// Build scored matches for sorting before budget trimming
	type scoredSymbol struct {
		sym   index.SymbolInfo
		score float64
	}
	scored := make([]scoredSymbol, len(symbols))
	for i, s := range symbols {
		scored[i] = scoredSymbol{sym: s, score: scoreSymbolMatch(s.Name, pattern)}
	}
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].score > scored[j].score
	})

	matches := make([]output.Match, 0)
	totalTokens := 0
	truncated := false
	for _, ss := range scored {
		s := ss.sym
		size := int(s.EndByte-s.StartByte) / 4 // rough token estimate

		// Budget limits total matches when not showing body
		// Always include at least the first match so low-budget queries aren't empty
		if !showBody && budget > 0 && totalTokens+size > budget && len(matches) > 0 {
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
			Score: ss.score,
		}

		if showBody {
			src, err := os.ReadFile(s.File)
			if err == nil && int(s.EndByte) <= len(src) {
				body := string(src[s.StartByte:s.EndByte])
				if budget > 0 {
					body, _ = output.TruncateBodyToTokenBudget(body, budget, totalTokens)
				}
				m.Body = body
			}
			totalTokens += size
		}

		matches = append(matches, m)

		// When showing body, stop adding matches once budget is fully used
		if showBody && budget > 0 && totalTokens >= budget {
			truncated = true
			// Continue adding metadata-only matches until we've added a reasonable count
			for _, ss2 := range scored[len(matches):] {
				if len(matches) >= len(scored) || len(matches) >= 20 {
					break
				}
				s2 := ss2.sym
				matches = append(matches, output.Match{
					Symbol: output.Symbol{
						Type:  s2.Type,
						Name:  s2.Name,
						File:  output.Rel(s2.File),
						Lines: [2]int{int(s2.StartLine), int(s2.EndLine)},
						Size:  int(s2.EndByte-s2.StartByte) / 4,
					},
					Score: ss2.score,
				})
			}
			break
		}
	}
	return &SearchResult{
		Kind:         "symbol",
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

	var allMatches []output.Match
	totalMatches := 0

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

				// Name is always the single matched line (trimmed)
				matchedLine := strings.TrimSpace(line)

				// Snippet contains context block when --context is used
				var snippet string
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
					snippet = strings.Join(ctxLines, "\n")
					displayStart = ctxStart + 1
					displayEnd = ctxEnd
				}

				sizeStr := matchedLine
				if snippet != "" {
					sizeStr = snippet
				}
				size := len(sizeStr) / 4
				if size < 1 {
					size = 1
				}

				// Compute score: source files rank higher, exact matches rank higher
				score := scoreTextMatch(rel, line, pattern, lowerPattern, re)

				// Find column offset of match
				col := 0
				if re != nil {
					if loc := re.FindStringIndex(line); loc != nil {
						col = loc[0] + 1
					}
				} else {
					col = strings.Index(strings.ToLower(line), lowerPattern) + 1
				}

				allMatches = append(allMatches, output.Match{
					Symbol: output.Symbol{
						Type:  "text",
						Name:  matchedLine,
						File:  output.Rel(file),
						Lines: [2]int{displayStart, displayEnd},
						Size:  size,
					},
					Score:   score,
					Snippet: snippet,
					Column:  col,
				})
			}
		}
		return nil
	})

	// Sort by score descending
	sort.Slice(allMatches, func(i, j int) bool {
		return allMatches[i].Score > allMatches[j].Score
	})

	// Apply budget trimming after sorting
	truncated := false
	var result []output.Match
	totalTokens := 0
	for _, m := range allMatches {
		if budget > 0 && totalTokens+m.Symbol.Size > budget && len(result) > 0 {
			truncated = true
			continue
		}
		totalTokens += m.Symbol.Size
		result = append(result, m)
	}

	if result == nil {
		result = []output.Match{}
	}
	return &SearchResult{
		Kind:         "text",
		Matches:      result,
		TotalMatches: totalMatches,
		Truncated:    truncated,
	}, err
}

// sourceExts contains file extensions for source code files.
var sourceExts = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true,
	".ts": true, ".tsx": true, ".rs": true, ".java": true,
	".rb": true, ".c": true, ".h": true, ".cpp": true,
	".cc": true, ".cs": true, ".swift": true, ".kt": true,
}

func isSourceFile(path string) bool {
	ext := filepath.Ext(path)
	return sourceExts[ext]
}

// scoreTextMatch computes a relevance score for a text search match.
// Source files score higher than config/docs; exact case matches score higher.
func scoreTextMatch(relPath, line, pattern, lowerPattern string, re *regexp.Regexp) float64 {
	score := 0.5

	// Source file bonus
	if isSourceFile(relPath) {
		score += 0.3
	}

	// Exact case match bonus (only for non-regex)
	if re == nil && strings.Contains(line, pattern) {
		score += 0.2
	}

	return score
}
