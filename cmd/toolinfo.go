package cmd

// toolinfo.go — single source of truth for tool descriptions.
// Used by both MCP tool schema (mcpTools) and CLI help (cobra commands).

// ToolDesc holds the description for each tool.
var ToolDesc = map[string]string{
	"plan":    "Batch reads/queries/edits/writes + verify. Preferred for multi-step tasks.",
	"do":      "Batch reads, queries, edits, writes, renames, verify, init. The primary tool for all operations.",
	"read":    "Read file, symbol (file:sym), or batch. Use edr_do for multiple reads.",
	"edit":    "Edit by old_text/new_text, symbol, or line range. Returns hash.",
	"write":   "Create/overwrite file. Supports append, after (symbol), inside (container), mkdir.",
	"search":  "Symbol or text search. body=true includes source inline.",
	"map":     "Symbol map of repo or file. Filters: dir, glob, type, grep.",
	"explore": "Symbol body, callers, deps. gather=true bundles callers+tests.",
	"refs":    "Find references. impact=true for transitive, chain for call path.",
	"find":    "Find files by glob (supports **). Returns sizes and mod times.",
	"rename":  "Cross-file rename, import-aware. dry_run to preview.",
	"verify":  "Run build/typecheck. Auto-detects go/npm/cargo.",
	"init":    "Force re-index the repository.",
	"diff":    "Retrieve stored diff from last large edit.",
}

// ParamDesc holds parameter descriptions shared between MCP and CLI.
var ParamDesc = map[string]string{
	// Shared
	"budget":     "Max response tokens",
	"file":       "File path",
	"files":      "Paths or file:symbol entries",
	"symbol":     "Symbol name",
	"dry_run":    "Preview without applying",
	"content":    "File contents",

	// read
	"start_line":  "Start line",
	"end_line":    "End line",
	"symbols":     "Append symbol list",
	"signatures":  "Signatures only (75-86% fewer tokens)",
	"depth":       "Depth: 1=sigs, 2=collapsed blocks",
	"full":        "Force full content (skip delta)",

	// edit
	"old_text":  "Text to find",
	"new_text":  "Replacement text",
	"regex":     "Treat pattern as regex",
	"all":       "Replace all matches",

	// write
	"mkdir":  "Create parent dirs",
	"append": "Append to file",
	"after":  "Insert after symbol",
	"inside": "Insert inside container",

	// search
	"pattern": "Search pattern",
	"body":    "Include source in results",
	"text":    "Text search mode",
	"include": "File glob(s) to include",
	"exclude": "File glob(s) to exclude",
	"context": "Lines of context",

	// map
	"dir":    "Filter by directory",
	"glob":   "Filter by file glob",
	"type":   "Filter by symbol type",
	"grep":   "Filter by name pattern",
	"locals": "Include local variables",

	// explore
	"callers": "Include callers",
	"deps":    "Include dependencies",
	"gather":  "Full context: callers + tests",

	// refs
	"impact": "Transitive callers",
	"chain":  "Find call path from symbol to target (traces callers of target backward)",

	// rename
	"old_name": "Current name",
	"new_name": "New name",
	"scope":    "Limit to glob pattern",

	// verify
	"command": "Custom command (auto-detect if omitted)",
	"timeout": "Timeout in seconds",

	// do (was plan)
	"reads":     "Read queries: [{file, symbol?, budget?, signatures?, depth?}]",
	"queries":   "Any query: [{cmd: search|explore|refs|map|find|diff, ...params}]",
	"edits":     "Atomic edits: [{file, old_text, new_text}]",
	"writes":    "File writes: [{file, content, mkdir?, after?, inside?}]",
	"renames":   "Cross-file renames: [{old_name, new_name, dry_run?, scope?}]",
	"verify":    "true = auto-detect build check, string = custom command",
	"init_flag": "Force re-index before other operations",
}

// P is a shorthand for ParamDesc lookup.
func P(key string) string {
	if d, ok := ParamDesc[key]; ok {
		return d
	}
	return key
}
