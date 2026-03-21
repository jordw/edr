// Package hints emits contextual suggestions to stderr about unused edr
// features, giving agent models progressive exposure to the CLI surface.
package hints

import (
	"fmt"
	"os"
	"strings"
)

// maxHints caps how many hints are printed per invocation to avoid noise.
const maxHints = 2

// Prefix is prepended to every hint line on stderr.
const Prefix = "hint: "

// Op describes one operation that was part of the request.
type Op struct {
	Kind  string            // "read", "search", "map", "edit", "write", "refs", "rename", "verify"
	Flags map[string]bool   // flags that were set (e.g. "sig" → true)
	Meta  map[string]string // optional metadata (e.g. "results" → "0")
}

// Context carries everything the hint engine needs to decide what to suggest.
type Context struct {
	Ops       []Op
	IsBatch   bool
	HasError  bool
	SeenHints map[string]bool // hints already shown this session — skip these
}

// hint is a candidate suggestion.
type hint struct {
	key     string // dedup key (stored in session to avoid repeating)
	message string
}

// Generate returns up to maxHints contextual hints for the given request.
func Generate(ctx Context) []hint {
	var candidates []hint

	readCount := 0
	hasSearch := false
	hasMap := false
	hasEdit := false
	hasRefs := false

	for _, op := range ctx.Ops {
		switch op.Kind {
		case "read":
			readCount++
			if !op.Flags["sig"] && !op.Flags["signatures"] && !op.Flags["skeleton"] {
				candidates = append(candidates, hint{
					key:     "read-sig",
					message: "use --sig to read only signatures (75-85% smaller output)",
				})
			}
			if !op.Flags["budget"] && !op.Flags["full"] {
				candidates = append(candidates, hint{
					key:     "read-budget",
					message: "use --budget N to cap read output to N tokens",
				})
			}

		case "search":
			hasSearch = true
			if !op.Flags["text"] && op.Meta["results"] == "0" {
				candidates = append(candidates, hint{
					key:     "search-text",
					message: "no symbol matches — try --text for full-text search",
				})
			}
			if !op.Flags["context"] && op.Flags["text"] {
				candidates = append(candidates, hint{
					key:     "search-context",
					message: "use --context N to see surrounding lines with text search results",
				})
			}

		case "map":
			hasMap = true
			if !op.Flags["budget"] && !op.Flags["full"] {
				candidates = append(candidates, hint{
					key:     "map-budget",
					message: "use --budget N to control map output size (default: 2000 tokens)",
				})
			}
			if !op.Flags["type"] && !op.Flags["grep"] {
				candidates = append(candidates, hint{
					key:     "map-filter",
					message: "use --type function|struct|class or --grep pattern to filter the symbol map",
				})
			}

		case "edit":
			hasEdit = true
			if !op.Flags["dry_run"] && !op.Flags["dry-run"] {
				candidates = append(candidates, hint{
					key:     "edit-dryrun",
					message: "use --dry-run to preview edits without applying them",
				})
			}

		case "refs":
			hasRefs = true
			if !op.Flags["impact"] {
				candidates = append(candidates, hint{
					key:     "refs-impact",
					message: "use --impact to see transitive callers (Go/Python/JS/TS)",
				})
			}
			if !op.Flags["chain"] {
				candidates = append(candidates, hint{
					key:     "refs-chain",
					message: "use --chain TARGET to find the call path between two symbols",
				})
			}

		case "write":
			if !op.Flags["inside"] && !op.Flags["after"] {
				candidates = append(candidates, hint{
					key:     "write-inside",
					message: "use --inside Symbol to add fields/methods to a class or struct without reading the file first",
				})
			}
		}
	}

	// Cross-op hints
	if readCount >= 2 && !ctx.IsBatch {
		candidates = append(candidates, hint{
			key:     "batch-reads",
			message: "batch multiple reads in one call: edr -r file1.go -r file2.go",
		})
	}

	if hasEdit && !hasRefs {
		candidates = append(candidates, hint{
			key:     "refs-before-edit",
			message: "use edr refs Symbol --impact before refactoring to check all callers",
		})
	}

	if (hasSearch || hasMap) && readCount == 0 && !hasEdit {
		candidates = append(candidates, hint{
			key:     "read-symbol",
			message: "use edr -r file.go:SymbolName to read a specific function or type",
		})
	}

	_ = hasRefs // used above

	// Filter out already-seen hints
	var filtered []hint
	for _, h := range candidates {
		if ctx.SeenHints != nil && ctx.SeenHints[h.key] {
			continue
		}
		filtered = append(filtered, h)
	}

	// Cap at maxHints
	if len(filtered) > maxHints {
		filtered = filtered[:maxHints]
	}

	return filtered
}

// Emit prints hints to stderr and returns the keys that were emitted
// (so the caller can record them in the session).
func Emit(ctx Context) []string {
	if os.Getenv("EDR_NO_HINTS") != "" {
		return nil
	}
	hints := Generate(ctx)
	if len(hints) == 0 {
		return nil
	}

	var b strings.Builder
	var keys []string
	for _, h := range hints {
		fmt.Fprintf(&b, "%s%s\n", Prefix, h.message)
		keys = append(keys, h.key)
	}
	fmt.Fprint(os.Stderr, b.String())
	return keys
}
