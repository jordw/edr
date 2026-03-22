package dispatch

import (
	"context"
	"fmt"
	"os"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/search"
)

const defaultSearchBudget = 2000

func runRepoMap(ctx context.Context, db *index.DB, flags map[string]any) (any, error) {
	var opts []index.RepoMapOption
	if dir := flagString(flags, "dir", ""); dir != "" {
		opts = append(opts, index.WithDir(dir))
	}
	if glob := flagString(flags, "glob", ""); glob != "" {
		opts = append(opts, index.WithGlob(glob))
	}
	if symType := flagString(flags, "type", ""); symType != "" {
		opts = append(opts, index.WithSymbolType(symType))
	}
	if grep := flagString(flags, "grep", ""); grep != "" {
		opts = append(opts, index.WithGrep(grep))
	}
	if lang := flagString(flags, "lang", ""); lang != "" {
		opts = append(opts, index.WithLang(lang))
	}
	// Hide locals by default; pass --locals to include them
	if !flagBool(flags, "locals", false) {
		opts = append(opts, index.WithHideLocals())
	}

	// Apply default budget unless --budget specified or --full
	budget := flagInt(flags, "budget", 0)
	if budget == 0 && !flagBool(flags, "full", false) {
		budget = defaultSearchBudget
	}
	if budget > 0 {
		opts = append(opts, index.WithBudget(budget))
	}

	_, stats, err := index.RepoMap(ctx, db, opts...)
	if err != nil {
		return nil, err
	}

	result := map[string]any{
		"files":     stats.TotalFiles,
		"symbols":   stats.TotalSymbols,
		"content":   stats.Files,
		"truncated": stats.Truncated,
	}
	if stats.Truncated {
		result["shown_files"] = stats.ShownFiles
		result["shown_symbols"] = stats.ShownSymbols
		result["hint"] = "use --dir, --type, --lang, or --grep to narrow scope"
	}
	return result, nil
}

func runSearch(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 || args[0] == "" {
		return nil, fmt.Errorf("search requires a non-empty pattern")
	}
	budget := flagInt(flags, "budget", 0)
	if budget == 0 && !flagBool(flags, "full", false) {
		budget = defaultSearchBudget
	}
	showBody := flagBool(flags, "body", true) // body on by default for agent use
	limit := flagInt(flags, "limit", 0)
	return search.SearchSymbol(ctx, db, args[0], budget, showBody, limit)
}

func runSearchText(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 || args[0] == "" {
		return nil, fmt.Errorf("search requires a non-empty pattern")
	}
	budget := flagInt(flags, "budget", 0)
	if budget == 0 && !flagBool(flags, "full", false) {
		budget = defaultSearchBudget
	}
	useRegex := flagBool(flags, "regex", false)
	var opts []search.SearchTextOption
	if inc := flagStringSlice(flags, "include"); len(inc) > 0 {
		opts = append(opts, search.WithInclude(inc...))
	}
	if exc := flagStringSlice(flags, "exclude"); len(exc) > 0 {
		opts = append(opts, search.WithExclude(exc...))
	}
	if ctxLines := flagInt(flags, "context", 0); ctxLines > 0 {
		opts = append(opts, search.WithContext(ctxLines))
	}
	if limit := flagInt(flags, "limit", 0); limit > 0 {
		opts = append(opts, search.WithLimit(limit))
	}
	result, err := search.SearchText(ctx, db, args[0], budget, useRegex, opts...)
	if err != nil {
		return nil, err
	}

	// Group text results by file for compact output (default on, --no-group to disable)
	noGroup := flagBool(flags, "no_group", false) || flagBool(flags, "no-group", false)
	if !noGroup && result.Kind == "text" && len(result.Matches) > 0 {
		return groupTextResults(result), nil
	}
	return result, nil
}

type groupedFileMatch struct {
	File    string         `json:"file"`
	Count   int            `json:"count"`
	Matches []groupedLine  `json:"matches"`
}

type groupedLine struct {
	Line    int     `json:"line"`
	Column  int     `json:"column,omitempty"`
	Text    string  `json:"text"`
	Snippet string  `json:"snippet,omitempty"`
	Score   float64 `json:"score,omitempty"`
}

func groupTextResults(result *search.SearchResult) map[string]any {
	fileOrder := []string{}
	groups := map[string]*groupedFileMatch{}

	// Check if all scores are uniform — if so, omit them (no information value)
	uniformScore := len(result.Matches) > 0
	if uniformScore {
		first := result.Matches[0].Score
		for _, m := range result.Matches[1:] {
			if m.Score != first {
				uniformScore = false
				break
			}
		}
	}

	for _, m := range result.Matches {
		f := m.Symbol.File
		g, ok := groups[f]
		if !ok {
			g = &groupedFileMatch{File: f}
			groups[f] = g
			fileOrder = append(fileOrder, f)
		}
		g.Count++
		gl := groupedLine{
			Line:    m.Symbol.Lines[0],
			Column:  m.Column,
			Text:    m.Symbol.Name,
			Snippet: m.Snippet,
		}
		if !uniformScore {
			gl.Score = m.Score
		}
		g.Matches = append(g.Matches, gl)
	}

	resultGroups := make([]groupedFileMatch, 0, len(fileOrder))
	for _, f := range fileOrder {
		resultGroups = append(resultGroups, *groups[f])
	}

	out := map[string]any{
		"kind":          "text_grouped",
		"files":         resultGroups,
		"total_files":   len(fileOrder),
		"total_matches": result.TotalMatches,
		"truncated":     result.Truncated,
	}
	if result.Hint != "" {
		out["hint"] = result.Hint
	}
	return out
}

func runFindFiles(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("find-files requires 1 argument: <pattern>")
	}
	pattern := args[0]
	dir := flagString(flags, "dir", "")
	budget := flagInt(flags, "budget", 0)

	return search.FindFiles(ctx, root, pattern, dir, budget)
}

func runSearchInSymbol(ctx context.Context, db *index.DB, args []string, flags map[string]any, inSpec string) (any, error) {
	if len(args) < 1 || args[0] == "" {
		return nil, fmt.Errorf("search requires a non-empty pattern")
	}
	budget := flagInt(flags, "budget", 0)
	if budget == 0 && !flagBool(flags, "full", false) {
		budget = defaultSearchBudget
	}
	useRegex := flagBool(flags, "regex", false)

	// Parse file:Symbol from --in
	parts := splitFileSymbol(inSpec)

	// If not file:Symbol, check if it is a bare file or directory path.
	// Route to text search with --include filter instead of symbol-scoped search.
	if parts == nil {
		resolved, resolveErr := db.ResolvePath(inSpec)
		if resolveErr == nil {
			info, statErr := os.Stat(resolved)
			if statErr == nil {
				if info.IsDir() {
					// Directory: add include glob for all files under it
					rel := output.Rel(resolved)
					flags["include"] = rel + "/**"
				} else {
					// File: add include filter for this specific file
					rel := output.Rel(resolved)
					flags["include"] = rel
				}
				return runSearchText(ctx, db, args, flags)
			}
		}
		return nil, fmt.Errorf("--in requires file:Symbol, file path, or directory path; got %q", inSpec)
	}

	var opts []search.SearchTextOption
	if ctxLines := flagInt(flags, "context", 0); ctxLines > 0 {
		opts = append(opts, search.WithContext(ctxLines))
	}
	if limit := flagInt(flags, "limit", 0); limit > 0 {
		opts = append(opts, search.WithLimit(limit))
	}

	result, err := search.SearchInSymbol(ctx, db, args[0], parts[0], parts[1], budget, useRegex, opts...)
	if err != nil {
		return nil, err
	}

	// Group text results by file for compact output
	noGroup := flagBool(flags, "no_group", false) || flagBool(flags, "no-group", false)
	if !noGroup && result.Kind == "text" && len(result.Matches) > 0 {
		return groupTextResults(result), nil
	}
	return result, nil
}
