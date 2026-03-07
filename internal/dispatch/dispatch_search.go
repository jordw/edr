package dispatch

import (
	"context"
	"fmt"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/search"
)

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
	// Hide locals by default; pass --locals to include them
	if !flagBool(flags, "locals", false) {
		opts = append(opts, index.WithHideLocals())
	}

	repoMap, err := index.RepoMap(ctx, db, opts...)
	if err != nil {
		return nil, err
	}

	budget := flagInt(flags, "budget", 0)
	truncated := false
	if budget > 0 {
		size := len(repoMap) / 4
		if size > budget {
			chars := budget * 4
			repoMap, truncated = output.TruncateAtLine(repoMap, chars)
		}
	}

	files, symbols, _ := db.Stats(ctx)
	return map[string]any{
		"files":     files,
		"symbols":   symbols,
		"map":       repoMap,
		"truncated": truncated,
	}, nil
}

func runSearch(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("search requires 1 argument: <pattern>")
	}
	budget := flagInt(flags, "budget", 0)
	showBody := flagBool(flags, "body", false)
	return search.SearchSymbol(ctx, db, args[0], budget, showBody)
}

func runSearchText(ctx context.Context, db *index.DB, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("search-text requires 1 argument: <pattern>")
	}
	budget := flagInt(flags, "budget", 0)
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
	return search.SearchText(ctx, db, args[0], budget, useRegex, opts...)
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
