package search

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"sync"
	"sync/atomic"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// matchColumn returns the 1-based column of the first match in line.
func matchColumn(line, pattern, lowerPattern string, re *regexp.Regexp, caseSensitive bool) int {
	if re != nil {
		if loc := re.FindStringIndex(line); loc != nil {
			return loc[0] + 1
		}
		return 0
	}
	if caseSensitive {
		return strings.Index(line, pattern) + 1
	}
	if lowerPattern != "" {
		return strings.Index(strings.ToLower(line), lowerPattern) + 1
	}
	return 0
}

// buildSnippet extracts context lines around lineIdx from allLines.
func buildSnippet(allLines []string, lineIdx, contextSize, baseLineNum int) (snippet string, displayStart, displayEnd int) {
	displayStart = baseLineNum
	displayEnd = baseLineNum
	if contextSize <= 0 {
		return "", displayStart, displayEnd
	}
	ctxStart := lineIdx - contextSize
	if ctxStart < 0 {
		ctxStart = 0
	}
	ctxEnd := lineIdx + contextSize + 1
	if ctxEnd > len(allLines) {
		ctxEnd = len(allLines)
	}
	var ctxLines []string
	for i := ctxStart; i < ctxEnd; i++ {
		ctxLines = append(ctxLines, allLines[i])
	}
	snippet = strings.Join(ctxLines, "\n")
	displayStart = baseLineNum - (lineIdx - ctxStart)
	displayEnd = baseLineNum + (ctxEnd - lineIdx - 1)
	return snippet, displayStart, displayEnd
}

// SearchResult wraps matches with truncation metadata.
type SearchResult struct {
	Kind         string         `json:"kind"`                   // "symbol" or "text"
	Matches      []output.Match `json:"matches"`
	TotalMatches int            `json:"total_matches"`
	Truncated    bool           `json:"truncated"`
	BudgetUsed   int            `json:"budget_used,omitempty"`
	Hint         string         `json:"hint,omitempty"`
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
func SearchSymbol(ctx context.Context, db index.SymbolStore, pattern string, budget int, showBody bool, limit int) (*SearchResult, error) {
	if pattern == "" {
		return &SearchResult{Kind: "symbol"}, nil
	}
	sqlLimit := 0
	if budget > 0 {
		sqlLimit = budget * 3 // overestimate: fetch more rows than budget to allow scoring/trimming
	}
	symbols, err := db.SearchSymbols(ctx, pattern, sqlLimit)
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
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].sym.File != scored[j].sym.File {
			return scored[i].sym.File < scored[j].sym.File
		}
		return scored[i].sym.StartLine < scored[j].sym.StartLine
	})

	// Apply limit after scoring
	if limit > 0 && len(scored) > limit {
		scored = scored[:limit]
	}

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
			src, err := index.CachedReadFile(ctx, s.File)
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
	budgetUsed := 0
	if truncated {
		budgetUsed = totalTokens
	}
	return &SearchResult{
		Kind:         "symbol",
		Matches:      matches,
		TotalMatches: len(symbols),
		Truncated:    truncated,
		BudgetUsed:   budgetUsed,
	}, nil
}

// searchTextConfig holds optional filters for SearchText.
type searchTextConfig struct {
	include []string // glob patterns to include (e.g. "*.go")
	exclude []string // glob patterns to exclude (e.g. "vendor/*")
	context int      // lines of context around each match
	limit       int  // max number of results (0 = unlimited)
	forceLiteral bool // suppress regex auto-retry when true
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

// WithLimit caps the number of results returned.
func WithLimit(n int) SearchTextOption {
	return func(c *searchTextConfig) { c.limit = n }
}

// WithForceLiteral suppresses the regex auto-retry when --text is explicit.
func WithForceLiteral() SearchTextOption {
	return func(c *searchTextConfig) { c.forceLiteral = true }
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
// stripRegexEscapes removes common regex escape sequences from a pattern to
// produce a literal string. Agents habitually escape metacharacters (e.g.,
// "map\[string\]", "db\.Root") even when searching literally.
func stripRegexEscapes(pattern string) string {
	var b strings.Builder
	b.Grow(len(pattern))
	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '\\' && i+1 < len(pattern) {
			next := pattern[i+1]
			// Strip backslash before common metacharacters
			if next == '.' || next == '[' || next == ']' || next == '(' || next == ')' ||
				next == '{' || next == '}' || next == '+' || next == '*' || next == '?' ||
				next == '|' || next == '^' || next == '$' || next == '\\' {
				b.WriteByte(next)
				i++ // skip the backslash
				continue
			}
		}
		b.WriteByte(pattern[i])
	}
	return b.String()
}

func SearchText(ctx context.Context, db index.SymbolStore, pattern string, budget int, useRegex bool, opts ...SearchTextOption) (*SearchResult, error) {
	if pattern == "" {
		return &SearchResult{Kind: "text"}, nil
	}
	cfg := searchTextConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	root := db.Root()

	// Comma-separated OR: "foo,bar,baz" → match any term
	var orTerms []string
	var orTermsLower []string
	if !useRegex && strings.Contains(pattern, ",") {
		for _, t := range strings.Split(pattern, ",") {
			t = strings.TrimSpace(t)
			if t != "" {
				orTerms = append(orTerms, t)
				orTermsLower = append(orTermsLower, strings.ToLower(t))
			}
		}
	}

	var re *regexp.Regexp
	var lowerPattern string
	caseSensitive := hasUpperCase(pattern) // smart-case: uppercase in pattern → exact match
	if len(orTerms) > 0 {
		// OR mode — matching handled below
		if !caseSensitive {
			orTerms = nil // use orTermsLower instead
		} else {
			orTermsLower = nil // use orTerms with exact case
		}
	} else if useRegex {
		p := pattern
		if !caseSensitive {
			p = "(?i)" + p
		}
		var err error
		re, err = regexp.Compile(p)
		if err != nil {
			return nil, err
		}
	} else {
		// Literal search: strip regex escapes agents may have added.
		pattern = stripRegexEscapes(pattern)
		caseSensitive = hasUpperCase(pattern) // re-check after stripping
		if caseSensitive {
			// exact case match — no lowering
		} else {
			lowerPattern = strings.ToLower(pattern)
		}
	}

	// Phase 1: Collect file paths (fast, sequential walk).
	var files []string
	_ = index.WalkRepoFiles(root, func(file string) error {
		rel, _ := filepath.Rel(root, file)
		base := filepath.Base(file)
		if len(cfg.include) > 0 && !matchesAnyPath(base, rel, cfg.include) {
			return nil
		}
		if len(cfg.exclude) > 0 && matchesAnyPath(base, rel, cfg.exclude) {
			return nil
		}
		files = append(files, file)
		return nil
	})

	// Phase 2: Search files in parallel with bounded workers.
	type fileMatches struct {
		matches []output.Match
		count   int  // total matches in file (may exceed len(matches) due to cap)
		capped  bool // true if matches were dropped by per-file cap
	}

	const maxMatchesPerFile = 10
	resultCh := make(chan fileMatches, len(files))
	var totalMatches atomic.Int64

	nWorkers := runtime.NumCPU()
	if nWorkers > len(files) {
		nWorkers = len(files)
	}
	if nWorkers < 1 {
		nWorkers = 1
	}

	fileCh := make(chan string, len(files))
	for _, f := range files {
		fileCh <- f
	}
	close(fileCh)

	var wg sync.WaitGroup
	for w := 0; w < nWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for file := range fileCh {
				if ctx.Err() != nil {
					return
				}
				data, err := index.CachedReadFile(ctx, file)
				if err != nil {
					continue
				}
				// Skip binary files (contain NUL bytes in first 8KB)
				checkLen := len(data)
				if checkLen > 8192 {
					checkLen = 8192
				}
				if bytes.Contains(data[:checkLen], []byte{0}) {
					continue
				}
				rel, _ := filepath.Rel(root, file)
				allLines := strings.Split(string(data), "\n")

				var fm fileMatches
				for lineIdx, line := range allLines {
				var matched bool
				if re != nil {
					matched = re.MatchString(line)
				} else if len(orTerms) > 0 {
					// Case-sensitive OR
					for _, term := range orTerms {
						if strings.Contains(line, term) {
							matched = true
							break
						}
					}
				} else if len(orTermsLower) > 0 {
					// Case-insensitive OR
					lower := strings.ToLower(line)
					for _, term := range orTermsLower {
						if strings.Contains(lower, term) {
							matched = true
							break
						}
					}
				} else if caseSensitive {
					matched = strings.Contains(line, pattern)
				} else {
					matched = strings.Contains(strings.ToLower(line), lowerPattern)
				}
				if !matched {
					continue
				}
				fm.count++
				if len(fm.matches) >= maxMatchesPerFile {
					fm.capped = true
					continue
				}

				lineNum := lineIdx + 1
				matchedLine := strings.TrimSpace(line)

				snippet, displayStart, displayEnd := buildSnippet(allLines, lineIdx, cfg.context, lineNum)

				sizeStr := matchedLine
				if snippet != "" {
					sizeStr = snippet
				}
				size := len(sizeStr) / 4
				if size < 1 {
					size = 1
				}

				score := scoreTextMatch(rel, line, pattern, lowerPattern, re)

				col := matchColumn(line, pattern, lowerPattern, re, caseSensitive)

				fm.matches = append(fm.matches, output.Match{
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
			if fm.count > 0 {
				totalMatches.Add(int64(fm.count))
				resultCh <- fm
			}
		}
	}()
	}
	wg.Wait()
	close(resultCh)

	// Phase 3: Merge, sort, and apply smart budget trimming.
	var allMatches []output.Match
	anyCapped := false
	for fm := range resultCh {
		allMatches = append(allMatches, fm.matches...)
		if fm.capped {
			anyCapped = true
		}
	}

	sort.SliceStable(allMatches, func(i, j int) bool {
		if allMatches[i].Score != allMatches[j].Score {
			return allMatches[i].Score > allMatches[j].Score
		}
		if allMatches[i].Symbol.File != allMatches[j].Symbol.File {
			return allMatches[i].Symbol.File < allMatches[j].Symbol.File
		}
		return allMatches[i].Symbol.Lines[0] < allMatches[j].Symbol.Lines[0]
	})

	// Apply limit after scoring
	if cfg.limit > 0 && len(allMatches) > cfg.limit {
		allMatches = allMatches[:cfg.limit]
	}

	truncated := anyCapped
	result := budgetTrimText(allMatches, budget, &truncated)

	if result == nil {
		result = []output.Match{}
	}
	total := int(totalMatches.Load())
	displayed := len(result)
	isTrunc := truncated || displayed < total
	budgetUsed := 0
	if isTrunc && budget > 0 {
		for _, m := range result {
			budgetUsed += m.Symbol.Size
		}
	}
	sr := &SearchResult{
		Kind:         "text",
		Matches:      result,
		TotalMatches: displayed,
		Truncated:    isTrunc,
		BudgetUsed:   budgetUsed,
	}
	if !useRegex && !cfg.forceLiteral && sr.TotalMatches == 0 && looksLikeRegex(pattern) {
		// Normalize BRE syntax (grep default) to ERE (Go regexp):
		// \| → |, \( → (, \) → )
		normalized := pattern
		for _, pair := range [][2]string{{`\|`, "|"}, {`\(`, "("}, {`\)`, ")"}} {
			normalized = strings.ReplaceAll(normalized, pair[0], pair[1])
		}
		// Auto-retry with regex when literal search finds nothing.
		if _, compileErr := regexp.Compile(normalized); compileErr == nil {
			retrySr, retryErr := SearchText(ctx, db, normalized, budget, true, opts...)
			if retryErr == nil && retrySr.TotalMatches > 0 {
				retrySr.Hint = fmt.Sprintf("no literal matches; auto-retried as regex (%d matches)", retrySr.TotalMatches)
				return retrySr, nil
			}
		}
		// Regex compile failed (e.g. "interface{}") — try with escaped regex
		escaped := regexp.QuoteMeta(stripRegexEscapes(pattern))
		if escaped != normalized {
			retrySr, retryErr := SearchText(ctx, db, escaped, budget, true, opts...)
			if retryErr == nil && retrySr.TotalMatches > 0 {
				retrySr.Hint = fmt.Sprintf("auto-retried as escaped literal (%d matches)", retrySr.TotalMatches)
				return retrySr, nil
			}
		}
		sr.Hint = "no matches found (tried literal, regex, and escaped literal)"
	}
	return sr, nil
}

// SearchInSymbol searches for a pattern within a specific symbol's body.
// The symbol is identified by file:name. Matches are returned with line numbers
// relative to the file (not the symbol).
func SearchInSymbol(ctx context.Context, db index.SymbolStore, pattern string, symbolFile string, symbolName string, budget int, useRegex bool, opts ...SearchTextOption) (*SearchResult, error) {
	if pattern == "" {
		return &SearchResult{Kind: "text"}, nil
	}
	cfg := searchTextConfig{}
	for _, o := range opts {
		o(&cfg)
	}

	// Resolve the symbol
	file, err := db.ResolvePath(symbolFile)
	if err != nil {
		return nil, fmt.Errorf("resolving file %q: %w", symbolFile, err)
	}
	// Stores are always fresh — on-demand parsing.
	sym, err := db.GetSymbol(ctx, file, symbolName)
	if err != nil {
		return nil, err
	}

	// Read the file
	data, err := index.CachedReadFile(ctx, file)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", symbolFile, err)
	}

	// Extract the symbol body
	if int(sym.EndByte) > len(data) {
		return nil, fmt.Errorf("symbol %s byte range [%d:%d] exceeds file size %d", symbolName, sym.StartByte, sym.EndByte, len(data))
	}
	symbolBody := string(data[sym.StartByte:sym.EndByte])
	bodyLines := strings.Split(symbolBody, "\n")

	// Set up matching (reuse SearchText logic)
	caseSensitive := hasUpperCase(pattern)
	var re *regexp.Regexp
	var lowerPattern string
	if useRegex {
		p := pattern
		if !caseSensitive {
			p = "(?i)" + p
		}
		re, err = regexp.Compile(p)
		if err != nil {
			return nil, err
		}
	} else if caseSensitive {
		// exact case match
	} else {
		lowerPattern = strings.ToLower(pattern)
	}

	rel := output.Rel(file)
	var matches []output.Match
	totalCount := 0
	const maxMatches = 50

	for lineIdx, line := range bodyLines {
		var matched bool
		if re != nil {
			matched = re.MatchString(line)
		} else if caseSensitive {
			matched = strings.Contains(line, pattern)
		} else {
			matched = strings.Contains(strings.ToLower(line), lowerPattern)
		}
		if !matched {
			continue
		}
		totalCount++
		if len(matches) >= maxMatches {
			continue
		}

		lineNum := int(sym.StartLine) + lineIdx
		matchedLine := strings.TrimSpace(line)

		snippet, displayStart, displayEnd := buildSnippet(bodyLines, lineIdx, cfg.context, lineNum)

		sizeStr := matchedLine
		if snippet != "" {
			sizeStr = snippet
		}
		size := len(sizeStr) / 4
		if size < 1 {
			size = 1
		}

		col := matchColumn(line, pattern, lowerPattern, re, caseSensitive)

		matches = append(matches, output.Match{
			Symbol: output.Symbol{
				Type:  "text",
				Name:  matchedLine,
				File:  rel,
				Lines: [2]int{displayStart, displayEnd},
				Size:  size,
			},
			Score:   1.0,
			Snippet: snippet,
			Column:  col,
		})
	}

	truncated := false
	result := budgetTrimText(matches, budget, &truncated)
	if result == nil {
		result = []output.Match{}
	}
	isTrunc := truncated || totalCount > maxMatches
	budgetUsed := 0
	if isTrunc && budget > 0 {
		for _, m := range result {
			budgetUsed += m.Symbol.Size
		}
	}
	return &SearchResult{
		Kind:         "text",
		Matches:      result,
		TotalMatches: totalCount,
		Truncated:    isTrunc,
		BudgetUsed:   budgetUsed,
		Hint:         fmt.Sprintf("scoped to %s:%s (lines %d-%d)", rel, symbolName, sym.StartLine, sym.EndLine),
	}, nil
}

// hasUpperCase returns true if s contains any uppercase letter.
// Used for smart-case: patterns with uppercase → case-sensitive search.
func hasUpperCase(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// looksLikeRegex returns true if pattern contains common regex metacharacters
// that suggest the user intended a regex search (not just a dot in a filename).
func looksLikeRegex(pattern string) bool {
	// Match patterns like .*, .+, \w, \d, [abc], (a|b), ^...$
	// Single dots are common in filenames, so we only flag "strong" indicators.
	indicators := []string{".*", ".+", "\\w", "\\d", "\\s", "\\b", "\\.", "[", "(", "^", "$", "|", "{"}
	for _, ind := range indicators {
		if strings.Contains(pattern, ind) {
			return true
		}
	}
	return false
}

// budgetTrimText progressively reduces match detail to fit within budget:
// 1. Try with full context and lines
// 2. Drop context snippets
// 3. Truncate long match lines to 160 chars around the match
// 4. Drop excess matches
func budgetTrimText(matches []output.Match, budget int, truncated *bool) []output.Match {
	if budget <= 0 {
		return matches
	}

	// Account for JSON wire overhead: each match adds ~5 tokens of framing
	// (field names, braces, line numbers), and each file group adds ~8 tokens.
	// Apply a discount so the final serialized output is closer to the
	// requested budget.  Estimate: 5 tokens/match overhead.  With an
	// average match text of ~10 tokens, overhead is ~33%.  A 0.6 multiplier
	// is a reasonable approximation across typical result sets.
	budget = int(float64(budget) * 0.6)
	if budget < 1 {
		budget = 1
	}

	if totalSize(matches) <= budget {
		return matches
	}

	// Pass 2: drop context snippets
	for i := range matches {
		if matches[i].Snippet != "" {
			matches[i].Snippet = ""
			matches[i].Symbol.Lines = [2]int{matches[i].Symbol.Lines[0], matches[i].Symbol.Lines[0]}
			matches[i].Symbol.Size = len(matches[i].Symbol.Name) / 4
			if matches[i].Symbol.Size < 1 {
				matches[i].Symbol.Size = 1
			}
		}
	}
	if totalSize(matches) <= budget {
		*truncated = true
		return matches
	}

	// Pass 3: truncate long match lines to 160 chars
	const maxLineLen = 160
	for i := range matches {
		name := matches[i].Symbol.Name
		if len(name) > maxLineLen {
			col := matches[i].Column
			if col > 0 {
				start := col - maxLineLen/2
				if start < 0 {
					start = 0
				}
				end := start + maxLineLen
				if end > len(name) {
					end = len(name)
					start = end - maxLineLen
					if start < 0 {
						start = 0
					}
				}
				matches[i].Symbol.Name = name[start:end]
			} else {
				matches[i].Symbol.Name = name[:maxLineLen]
			}
			matches[i].Symbol.Size = len(matches[i].Symbol.Name) / 4
			if matches[i].Symbol.Size < 1 {
				matches[i].Symbol.Size = 1
			}
		}
	}
	if totalSize(matches) <= budget {
		*truncated = true
		return matches
	}

	// Pass 4: drop excess matches
	*truncated = true
	var result []output.Match
	tokens := 0
	for _, m := range matches {
		if tokens+m.Symbol.Size > budget && len(result) > 0 {
			break
		}
		tokens += m.Symbol.Size
		result = append(result, m)
	}
	return result
}

func totalSize(matches []output.Match) int {
	n := 0
	for _, m := range matches {
		n += m.Symbol.Size
	}
	return n
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
