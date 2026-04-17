// Package cmdspec is the single source of truth for edr command metadata.
//
// Instead of scattering command names, categories, valid flags, and
// descriptions across cmd/, dispatch/, and session/, every consumer
// derives that information from this registry.
package cmdspec

import (
	"strings"

	"github.com/spf13/pflag"
)

// boolNegValue is a pflag.Value that sets its target to the opposite boolean.
// Used for --no-<flag> hidden aliases.
type boolNegValue struct {
	target pflag.Value
}

func (v *boolNegValue) String() string { return "false" }
func (v *boolNegValue) Set(s string) error {
	if s == "true" || s == "" {
		return v.target.Set("false")
	}
	return v.target.Set("true")
}
func (v *boolNegValue) Type() string { return "bool" }

// Category classifies a command for session and dispatch behavior.
type Category string

const (
	CatRead         Category = "read"          // read-only queries
	CatWrite        Category = "write"         // file mutations
	CatGlobalMutate Category = "global_mutate" // global-state mutations (reset, rename)
	CatMeta         Category = "meta"          // non-mutating utilities (verify, diff)
)

// FlagType enumerates the supported flag value types.
type FlagType int

const (
	FlagBool FlagType = iota
	FlagInt
	FlagString
	FlagStringSlice
)

// FlagSpec describes a single command flag/parameter.
type FlagSpec struct {
	Name    string   // canonical name (underscore convention, matches JSON)
	Alias   string   // short alias (e.g. "sig" for "signatures"); registered on standalone commands
	Type    FlagType // value type
	Default any      // default value: bool, int, string, or []string
	Desc    string   // human-readable description
}

// Spec describes a single edr command.
type Spec struct {
	Name       string   // command name
	Desc       string   // short description
	Category   Category // session/dispatch classification
	MinArgs    int      // cobra min args
	MaxArgs    int      // cobra max args (-1 = unlimited)
	StdinKey   string   // non-empty = command reads content from stdin under this flag name
	FileScoped bool     // first arg is a file path (enables DispatchMulti parallelism)
	Internal   bool     // not a public command (no cobra registration, dispatch-only)
	Flags      []FlagSpec

	// Session behavior flags — which post-processing stages apply.
	DiffEdit  bool // slim edit responses (stores large diffs for retrieval)
	DeltaRead bool // delta reads (return diff from last-seen version)
	BodyTrack bool // body tracking (dedup seen bodies in gather/search)
}

// Registry is the canonical list of all edr commands.
// focusFlags are shared between "focus" and its "read" alias.
var focusFlags = []FlagSpec{
	{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response tokens"},
	{Name: "signatures", Alias: "sig", Type: FlagBool, Default: false, Desc: "Signatures only (75-86% fewer tokens)"},
	{Name: "skeleton", Type: FlagBool, Default: false, Desc: "Skeleton view: blocks collapsed"},
	{Name: "lines", Type: FlagString, Default: "", Desc: "Line range (e.g. 10:50)"},
	{Name: "full", Type: FlagBool, Default: false, Desc: "Force full content (skip session delta)"},
	{Name: "symbols", Type: FlagBool, Default: false, Desc: "Include symbol list in read result"},
	{Name: "expand", Type: FlagString, Default: "", Desc: "Include related signatures: deps (default), callers, or both"},
	{Name: "no_expand", Type: FlagBool, Default: false, Desc: "Suppress auto-expand of dep signatures"},
}

// orientFlags are shared between "orient" and its "map" alias.
var orientFlags = []FlagSpec{
	{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response tokens (default 2000)"},
	{Name: "full", Type: FlagBool, Default: false, Desc: "Return full results (no default budget cap)"},
	{Name: "dir", Type: FlagString, Default: "", Desc: "Filter by directory"},
	{Name: "glob", Type: FlagString, Default: "", Desc: "Filter by file glob"},
	{Name: "type", Type: FlagString, Default: "", Desc: "Filter by symbol type"},
	{Name: "grep", Type: FlagString, Default: "", Desc: "Filter by name pattern"},
	{Name: "lang", Type: FlagString, Default: "", Desc: "Filter by language (e.g. go, python, javascript)"},
	{Name: "body", Type: FlagString, Default: "", Desc: "Filter to symbols whose body contains this text"},
}

var filesFlags = []FlagSpec{
	{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response tokens (default 2000)"},
	{Name: "full", Type: FlagBool, Default: false, Desc: "Return full results (no default budget cap)"},
	{Name: "regex", Type: FlagBool, Default: false, Desc: "Treat pattern as a regular expression (Go syntax)"},
	{Name: "glob", Type: FlagString, Default: "", Desc: "Limit results to files matching this path glob (e.g. \"**/*.go\")"},
}

var Registry = []*Spec{
	// --- Primary commands (shown in --help) ---
	{
		Name: "orient", Desc: "Structural overview of repo or directory. Filters: grep, body, glob, type, lang.",
		Category: CatRead, MinArgs: 0, MaxArgs: 1,
		Flags: orientFlags,
	},
	{
		Name: "focus", Desc: "Read file or symbol with context. Use file:symbol for targeted reads.",
		Category: CatRead, MinArgs: 1, MaxArgs: -1, FileScoped: true,
		DeltaRead: true, BodyTrack: true,
		Flags: focusFlags,
	},
	{
		Name: "edit", Desc: "Edit, write, or create files. Use --verify to check build.",
		Category: CatWrite, MinArgs: 0, MaxArgs: 2, StdinKey: "new_text", FileScoped: true,
		DiffEdit: true,
		Flags: []FlagSpec{
			// Text edit flags
			{Name: "old_text", Type: FlagString, Default: "", Desc: "Text to find", Alias: "old"},
			{Name: "new_text", Type: FlagString, Default: "", Desc: "Replacement text", Alias: "new"},
			{Name: "all", Type: FlagBool, Default: false, Desc: "Replace all matches"},
			{Name: "in", Type: FlagString, Default: "", Desc: "Scope text match to within a symbol body (file:Symbol)"},
			{Name: "where", Type: FlagString, Default: "", Desc: "Symbol query — resolves file and scopes edit automatically"},
			{Name: "delete", Type: FlagBool, Default: false, Desc: "Delete matched text or symbol"},
			{Name: "fuzzy", Type: FlagBool, Default: false, Desc: "Allow whitespace/indentation-only mismatches"},
			{Name: "lines", Type: FlagString, Default: "", Desc: "Line range as start:end"},
			{Name: "start_line", Type: FlagInt, Default: 0, Desc: "Start line"},
			{Name: "end_line", Type: FlagInt, Default: 0, Desc: "End line"},
			{Name: "insert_at", Type: FlagInt, Default: 0, Desc: "Insert new text before line N"},
			{Name: "move_after", Type: FlagString, Default: "", Desc: "Move symbol after another symbol"},
			{Name: "expect_hash", Alias: "hash", Type: FlagString, Default: "", Desc: "Reject edit if file hash doesn't match"},
			{Name: "refresh_hash", Type: FlagBool, Default: false, Desc: "On hash mismatch, refresh from disk and retry once"},
			{Name: "read_back", Type: FlagBool, Default: true, Desc: "Include updated context in response"},
			// Write/create flags (when no --old, acts as write)
			{Name: "content", Type: FlagString, Default: "", Desc: "Content to write (creates or overwrites file)"},
			{Name: "inside", Type: FlagString, Default: "", Desc: "Insert inside container"},
			{Name: "after", Type: FlagString, Default: "", Desc: "Place after this symbol"},
			{Name: "append", Type: FlagBool, Default: false, Desc: "Append to file"},
			{Name: "mkdir", Type: FlagBool, Default: false, Desc: "Create parent dirs"},
			// Shared
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
			{Name: "verify", Type: FlagBool, Default: false, Desc: "Run build verification after edit"},
			{Name: "no_verify", Type: FlagBool, Default: false, Desc: "Deprecated: verify is now opt-in"},
		},
	},
	{
		Name: "rename", Desc: "Rename a symbol across all references. Semantic find-and-replace.",
		Category: CatWrite, MinArgs: 1, MaxArgs: 2, FileScoped: true,
		DiffEdit: true,
		Flags: []FlagSpec{
			{Name: "new_name", Alias: "to", Type: FlagString, Default: "", Desc: "New name for the symbol (required)"},
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
			{Name: "verify", Type: FlagBool, Default: false, Desc: "Run build verification after rename"},
			{Name: "cross_file", Type: FlagBool, Default: false, Desc: "Rename across all files, not just the defining file"},
			{Name: "force", Type: FlagBool, Default: false, Desc: "With --cross-file, proceed even if the name is ambiguous (defined by multiple symbols)"},
			{Name: "comments", Type: FlagString, Default: "rewrite", Desc: "How to handle matches inside comments: 'rewrite' (default) or 'skip'"},
		},
	},
	{
		Name: "extract", Desc: "Extract lines from a function into a new function.",
		Category: CatWrite, MinArgs: 1, MaxArgs: 2, FileScoped: true,
		DiffEdit: true,
		Flags: []FlagSpec{
			{Name: "name", Type: FlagString, Default: "", Desc: "Name for the extracted function (required)"},
			{Name: "lines", Type: FlagString, Default: "", Desc: "Line range to extract (e.g. 10-20)"},
			{Name: "call", Type: FlagString, Default: "", Desc: "Replacement call expression (default: name())"},
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
			{Name: "verify", Type: FlagBool, Default: false, Desc: "Run build verification after extract"},
		},
	},
	{
		Name: "changesig", Desc: "Add or remove a parameter from a function and update all call sites.",
		Category: CatWrite, MinArgs: 1, MaxArgs: 2, FileScoped: true,
		DiffEdit: true,
		Flags: []FlagSpec{
			{Name: "add", Type: FlagString, Default: "", Desc: "Parameter declaration to add (e.g. \"ctx context.Context\")"},
			{Name: "at", Type: FlagInt, Default: -1, Desc: "Position to insert/remove (0-indexed; -1 = end)"},
			{Name: "callarg", Type: FlagString, Default: "", Desc: "Argument value to insert at call sites"},
			{Name: "remove", Type: FlagInt, Default: -1, Desc: "Position of parameter to remove (0-indexed)"},
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
			{Name: "verify", Type: FlagBool, Default: false, Desc: "Run build verification after change"},
			{Name: "cross_file", Type: FlagBool, Default: false, Desc: "Update call sites across all files, not just the defining file"},
			{Name: "force", Type: FlagBool, Default: false, Desc: "With --cross-file, proceed even if the name is ambiguous (defined by multiple symbols)"},
		},
	},
	// --- Internal commands (dispatch-only, no CLI surface) ---
	{
		Name: "search", Desc: "Internal: symbol and text search, used by batch -s.",
		Category: CatRead, MinArgs: 1, MaxArgs: 1, Internal: true,
		BodyTrack: true,
		Flags: []FlagSpec{
			{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response lines"},
			{Name: "limit", Type: FlagInt, Default: 0, Desc: "Max results"},
			{Name: "body", Type: FlagBool, Default: false, Desc: "Search symbol bodies"},
			{Name: "text", Type: FlagBool, Default: false, Desc: "Plain text search"},
			{Name: "regex", Type: FlagBool, Default: false, Desc: "Treat pattern as regex"},
			{Name: "include", Type: FlagStringSlice, Default: nil, Desc: "Include files matching glob"},
			{Name: "exclude", Type: FlagStringSlice, Default: nil, Desc: "Exclude files matching glob"},
			{Name: "context", Type: FlagInt, Default: 0, Desc: "Lines of context around matches"},
			{Name: "in", Type: FlagString, Default: "", Desc: "Scope search to file or file:symbol"},
			{Name: "no_group", Type: FlagBool, Default: false, Desc: "Don't group results by file"},
		},
	},
	{
		Name: "write", Desc: "Internal: used by edit --content routing.",
		Category: CatWrite, MinArgs: 1, MaxArgs: 1, StdinKey: "content", FileScoped: true, Internal: true,
		Flags: []FlagSpec{
			{Name: "mkdir", Type: FlagBool, Default: false, Desc: "Create parent dirs"},
			{Name: "after", Type: FlagString, Default: "", Desc: "Place after this symbol"},
			{Name: "inside", Type: FlagString, Default: "", Desc: "Insert inside container"},
			{Name: "content", Type: FlagString, Default: "", Desc: "Content to write"},
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
			{Name: "append", Type: FlagBool, Default: false, Desc: "Append to file"},
		},
	},
	{
		Name: "verify", Desc: "Internal: used by edit auto-verify.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 0, Internal: true,
		Flags: []FlagSpec{
			{Name: "command", Type: FlagString, Default: "", Desc: "Custom command"},
			{Name: "level", Type: FlagString, Default: "", Desc: "Verification level: build (default) or test"},
			{Name: "test", Type: FlagBool, Default: false, Desc: "Shorthand for --level test"},
			{Name: "timeout", Type: FlagInt, Default: 0, Desc: "Timeout in seconds"},
		},
	},
	{
		Name: "undo", Desc: "Revert to the last auto-checkpoint. Undoes the most recent edit/write.",
		Category: CatGlobalMutate, MinArgs: 0, MaxArgs: 0,
		Flags: []FlagSpec{
			{Name: "no_save", Type: FlagBool, Default: false, Desc: "Skip pre-restore safety checkpoint"},
		},
	},
	{
		Name: "status", Desc: "Session status: build state, stale assumptions, external changes.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 0,
		Flags: []FlagSpec{
			{Name: "focus", Type: FlagString, Default: "", Desc: "Set session focus (empty string clears)"},
			{Name: "reset", Type: FlagBool, Default: false, Desc: "Clear session and checkpoints"},
			{Name: "debug", Type: FlagBool, Default: false, Desc: "Show internal session/checkpoint paths for diagnostics"},
		},
	},
	{
		Name: "session", Desc: "Manage sessions.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 0,
		Internal: true,
		Flags:    []FlagSpec{},
	},
	{
		Name: "setup", Desc: "Install edr into a project and inject agent instructions.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 1,
		Flags: []FlagSpec{
			{Name: "global", Type: FlagBool, Default: false, Desc: "Install global instructions without prompting"},
			{Name: "no_global", Type: FlagBool, Default: false, Desc: "Skip global instruction prompt"},
			{Name: "generic", Type: FlagBool, Default: false, Desc: "Print instructions to stdout"},
			{Name: "force", Type: FlagBool, Default: false, Desc: "Replace existing instructions with latest version"},
			{Name: "skip_index", Type: FlagBool, Default: false, Desc: "Skip indexing (only install instructions)"},
			{Name: "json", Type: FlagBool, Default: false, Desc: "Output JSON instead of human-readable text"},
			{Name: "status", Type: FlagBool, Default: false, Desc: "Show installation status without modifying anything"},
			{Name: "uninstall", Type: FlagBool, Default: false, Desc: "Remove edr instructions from all global configs"},
		},
	},
	// --- Trigram index management ---
	{
		Name: "index", Desc: "Build or inspect the trigram search index.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 0,
		Flags: []FlagSpec{
			{Name: "status", Type: FlagBool, Default: false, Desc: "Show index stats without building"},
			{Name: "rebuild", Type: FlagBool, Default: false, Desc: "Explicit rebuild (default behavior; accepted for clarity)"},
		},
	},
	{
		Name: "files", Desc: "Find files containing a text pattern (trigram-accelerated).",
		Category: CatRead, MinArgs: 1, MaxArgs: 1,
		Flags: filesFlags,
	},
	{
		Name: "refs-to", Desc: "List references to a symbol (scope-aware, single-file for v1).",
		Category: CatRead, MinArgs: 1, MaxArgs: 1, FileScoped: true,
		Flags: []FlagSpec{
			{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max refs to return (0 = unlimited)"},
		},
	},
	{
		Name: "bench", Desc: "Benchmark common operations on the current repo.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 0,
		Flags: []FlagSpec{},
	},
}

// --- Lookup ---

var byName map[string]*Spec

// aliasToCanonical maps CLI alias names (e.g. "sig") to canonical flag names
// (e.g. "signatures"). Used by extractFlags to normalize alias usage.
var aliasToCanonical map[string]string

func init() {
	byName = make(map[string]*Spec, len(Registry))
	aliasToCanonical = make(map[string]string)
	for _, s := range Registry {
		byName[s.Name] = s
		for _, f := range s.Flags {
			if f.Alias != "" {
				aliasToCanonical[strings.ReplaceAll(f.Alias, "_", "-")] = strings.ReplaceAll(f.Name, "_", "-")
			}
		}
	}
}

// CanonicalFlagName returns the canonical name for a flag, resolving aliases.
// If the name is not an alias, it is returned unchanged.
func CanonicalFlagName(name string) string {
	if canonical, ok := aliasToCanonical[name]; ok {
		return canonical
	}
	return name
}

// ByName returns the spec for a command, or nil if unknown.
func ByName(name string) *Spec { return byName[name] }

// Names returns all registered command names.
func Names() []string {
	out := make([]string, len(Registry))
	for i, s := range Registry {
		out[i] = s.Name
	}
	return out
}

// --- Classification helpers ---

// IsRead returns true for read-category commands (read, map, search).
func IsRead(name string) bool {
	s := byName[name]
	return s != nil && s.Category == CatRead
}

// IsWrite returns true for write-category commands (edit, write).
func IsWrite(name string) bool {
	s := byName[name]
	return s != nil && s.Category == CatWrite
}

// IsGlobalMutating returns true for commands that mutate global state (reset, rename).
func IsGlobalMutating(name string) bool {
	s := byName[name]
	return s != nil && s.Category == CatGlobalMutate
}

// ModifiesState returns true for commands that modify files or global state.
// Used for session invalidation — covers CatWrite and CatGlobalMutate.
func ModifiesState(name string) bool {
	s := byName[name]
	return s != nil && (s.Category == CatWrite || s.Category == CatGlobalMutate)
}

// IsFileScoped returns true if the command's first arg is a file path.
func IsFileScoped(name string) bool {
	s := byName[name]
	return s != nil && s.FileScoped
}

// IsDiffEdit returns true for commands that produce diffs (edit).
func IsDiffEdit(name string) bool {
	s := byName[name]
	return s != nil && s.DiffEdit
}

// IsDeltaRead returns true for commands that support delta reads (read).
func IsDeltaRead(name string) bool {
	s := byName[name]
	return s != nil && s.DeltaRead
}

// IsBodyTrack returns true for commands whose bodies are tracked for dedup (read, search).
func IsBodyTrack(name string) bool {
	s := byName[name]
	return s != nil && s.BodyTrack
}

// --- Batch key sets (replace do*KnownKeys in cmd/do.go) ---

// flagNames returns a set of flag names for a command.
func flagNames(name string) map[string]bool {
	s := byName[name]
	if s == nil {
		return nil
	}
	m := make(map[string]bool, len(s.Flags))
	for _, f := range s.Flags {
		m[f.Name] = true
	}
	return m
}

// DoBatchKeys returns valid top-level keys for edr_do JSON.
func DoBatchKeys() map[string]bool {
	return map[string]bool{
		"reads": true, "queries": true, "edits": true, "writes": true,
		"renames": true, "budget": true, "dry_run": true, "verify": true,
		"read_after_edit": true, "atomic": true,
		"post_edit_reads": true, "post_edit_queries": true,
		"read_query_order": true, "post_edit_order": true,
	}
}

// ReadBatchKeys returns valid keys for doRead/focus batch objects.
func ReadBatchKeys() map[string]bool {
	m := flagNames("focus")
	// Structural args used in batch JSON (not CLI flags).
	m["file"] = true
	m["symbol"] = true
	// Legacy JSON batch fields (not CLI flags, used by JSON batch path).
	m["start_line"] = true
	m["end_line"] = true
	m["depth"] = true
	m["symbols"] = true
	return m
}

// EditBatchKeys returns valid keys for doEdit batch objects.
func EditBatchKeys() map[string]bool {
	m := flagNames("edit")
	m["file"] = true
	m["symbol"] = true
	return m
}

// --- Cobra flag registration ---

// RegisterFlags registers all flags from the registry for the named command.
// This ensures CLI flags stay in sync with the canonical cmdspec registry.
// Flag names are converted from underscore to hyphen convention for CLI
// (e.g., "dry_run" → "dry-run") since dispatch's flagLookup handles both.
func RegisterFlags(flags *pflag.FlagSet, name string) {
	s := byName[name]
	if s == nil {
		return
	}
	for _, f := range s.Flags {
		cliName := strings.ReplaceAll(f.Name, "_", "-")
		switch f.Type {
		case FlagBool:
			def, _ := f.Default.(bool)
			flags.Bool(cliName, def, f.Desc)
		case FlagInt:
			def, _ := f.Default.(int)
			flags.Int(cliName, def, f.Desc)
		case FlagString:
			def, _ := f.Default.(string)
			flags.String(cliName, def, f.Desc)
		case FlagStringSlice:
			var def []string
			if f.Default != nil {
				def, _ = f.Default.([]string)
			}
			flags.StringSlice(cliName, def, f.Desc)
		}
		// Register alias as a hidden flag sharing the same underlying value
		if f.Alias != "" {
			aliasName := strings.ReplaceAll(f.Alias, "_", "-")
			canonical := flags.Lookup(cliName)
			if canonical != nil {
				flags.AddFlag(&pflag.Flag{
					Name:        aliasName,
					Usage:       f.Desc,
					Value:       canonical.Value,
					DefValue:    canonical.DefValue,
					NoOptDefVal: canonical.NoOptDefVal,
					Hidden:      true,
				})
			}
		}
		// Register --no-<flag> as hidden negation for bool flags that default to true
		if f.Type == FlagBool {
			def, _ := f.Default.(bool)
			if def {
				negName := "no-" + cliName
				canonical := flags.Lookup(cliName)
				if canonical != nil {
					flags.AddFlag(&pflag.Flag{
						Name:        negName,
						Usage:       "Disable " + cliName,
						Value:       &boolNegValue{target: canonical.Value},
						DefValue:    "false",
						NoOptDefVal: "true",
						Hidden:      true,
					})
				}
			}
		}
	}
}

// --- Description helpers ---

// ToolDescs returns a map of command name → description, matching the
// shape of the former ToolDesc map in cmd/toolinfo.go.
func ToolDescs() map[string]string {
	m := make(map[string]string, len(Registry))
	for _, s := range Registry {
		m[s.Name] = s.Desc
	}
	return m
}

// ParamDescs returns a merged map of flag name → description from all commands.
// First occurrence wins, so commands registered first in Registry take priority.
func ParamDescs() map[string]string {
	m := make(map[string]string)
	for _, s := range Registry {
		for _, f := range s.Flags {
			if _, exists := m[f.Name]; !exists {
				m[f.Name] = f.Desc
			}
		}
	}
	return m
}

// Levenshtein computes the edit distance between two strings.
func Levenshtein(a, b string) int {
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}
	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			curr[j] = min(curr[j-1]+1, min(prev[j]+1, prev[j-1]+cost))
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}
