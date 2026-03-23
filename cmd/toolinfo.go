package cmd

import "github.com/jordw/edr/internal/cmdspec"

// toolinfo.go — tool descriptions and parameter help text.
// ToolDesc is derived from the canonical registry in cmdspec.
// ParamDesc merges registry-derived descriptions with batch-mode entries.

// ToolDesc holds the description for each tool.
var ToolDesc = func() map[string]string {
	m := cmdspec.ToolDescs()
	// Extra entries not in the registry.
	return m
}()

// ParamDesc holds parameter descriptions for CLI help text.
// Most entries are derived from cmdspec; serve-mode and structural fields are added here.
var ParamDesc = func() map[string]string {
	m := cmdspec.ParamDescs()
	// Structural / shared fields not tied to a specific command.
	m["file"] = "File path"
	m["files"] = "Paths or file:symbol entries"
	m["symbol"] = "Symbol name"
	m["content"] = "File contents"
	m["pattern"] = "Search pattern"
	m["old_name"] = "Current name"
	m["new_name"] = "New name"
	m["start_line"] = "Start line"
	m["end_line"] = "End line"
	// Batch fields.
	m["reads"] = "Read queries: [{file, symbol?, budget?, signatures?, depth?}]"
	m["queries"] = "Any query: [{cmd: search|explore|refs|map|find|diff, ...params}]"
	m["edits"] = "Atomic edits: [{file, old_text, new_text}] or [{file, symbol, new_text}] or [{file, start_line, end_line, new_text}] Supports all flag."
	m["writes"] = "File writes: [{file, content, mkdir?, after?, inside?}]"
	m["renames"] = "Cross-file renames: [{old_name, new_name, dry_run?}]"
	m["verify"] = `true = auto-detect build check, "build"/"test" = shortcut for level, other string = custom command`
	m["init_flag"] = "Force reset before other operations"
	m["read_after_edit"] = "Read edited file signatures after applying edits (saves a round trip, shows updated API shape)"
	return m
}()

// P is a shorthand for ParamDesc lookup (CLI help).
func P(key string) string {
	if d, ok := ParamDesc[key]; ok {
		return d
	}
	return key
}
