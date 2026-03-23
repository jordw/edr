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
	CatGlobalMutate Category = "global_mutate" // global-state mutations (reindex, rename)
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
var Registry = []*Spec{
	{
		Name: "read", Desc: "Read file or symbol. Use file:symbol for targeted reads.",
		Category: CatRead, MinArgs: 1, MaxArgs: -1, FileScoped: true,
		DeltaRead: true, BodyTrack: true,
		Flags: []FlagSpec{
			{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response tokens"},
			{Name: "signatures", Alias: "sig", Type: FlagBool, Default: false, Desc: "Signatures only (75-86% fewer tokens)"},
			{Name: "skeleton", Type: FlagBool, Default: false, Desc: "Skeleton view: blocks collapsed"},
			{Name: "lines", Type: FlagString, Default: "", Desc: "Line range (e.g. 10:50)"},
			{Name: "full", Type: FlagBool, Default: false, Desc: "Force full content (skip session delta)"},
			{Name: "symbols", Type: FlagBool, Default: false, Desc: "Include symbol list in read result"},
		},
	},
	{
		Name: "write", Desc: "Write file. Modes: plain write, --inside container, --after symbol.",
		Category: CatWrite, MinArgs: 1, MaxArgs: 1, StdinKey: "content", FileScoped: true,
		Flags: []FlagSpec{
			{Name: "mkdir", Type: FlagBool, Default: false, Desc: "Create parent dirs"},
			{Name: "after", Type: FlagString, Default: "", Desc: "Place after this symbol"},
			{Name: "inside", Type: FlagString, Default: "", Desc: "Insert inside container"},
			{Name: "content", Type: FlagString, Default: "", Desc: "Content to write (alternative to stdin)"},
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
						{Name: "append", Type: FlagBool, Default: false, Desc: "Append to file"},
		},
	},
	{
		Name: "edit", Desc: "Edit file by exact text match, symbol replacement, or line range.",
		Category: CatWrite, MinArgs: 1, MaxArgs: 2, StdinKey: "new_text", FileScoped: true,
		DiffEdit: true,
		Flags: []FlagSpec{
			{Name: "start_line", Type: FlagInt, Default: 0, Desc: "Start line"},
			{Name: "end_line", Type: FlagInt, Default: 0, Desc: "End line"},
			{Name: "old_text", Type: FlagString, Default: "", Desc: "Text to find", Alias: "old"},
			{Name: "new_text", Type: FlagString, Default: "", Desc: "Replacement text", Alias: "new"},
			{Name: "all", Type: FlagBool, Default: false, Desc: "Replace all matches"},
			{Name: "in", Type: FlagString, Default: "", Desc: "Scope text match to within a symbol body (file:Symbol)"},
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
			{Name: "expect_hash", Alias: "hash", Type: FlagString, Default: "", Desc: "Reject edit if file hash doesn't match"},
			{Name: "delete", Type: FlagBool, Default: false, Desc: "Delete matched text or symbol (equivalent to --new-text \"\")"},
			{Name: "lines", Type: FlagString, Default: "", Desc: "Line range as start:end (shorthand for --start-line/--end-line)"},
			{Name: "insert_at", Type: FlagInt, Default: 0, Desc: "Insert new text before line N (zero-width insertion)"},
			{Name: "fuzzy", Type: FlagBool, Default: false, Desc: "Allow whitespace/indentation-only mismatches (cannot combine with --all)"},
			{Name: "move_after", Type: FlagString, Default: "", Desc: "Move symbol after another symbol (same file only)"},
		},
	},
	{
		Name: "map", Desc: "Symbol map of repo or file. Filters: dir, glob, type, grep.",
		Category: CatRead, MinArgs: 0, MaxArgs: 1,
		Flags: []FlagSpec{
			{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response tokens (default 2000)"},
			{Name: "full", Type: FlagBool, Default: false, Desc: "Return full results (no default budget cap)"},
			{Name: "dir", Type: FlagString, Default: "", Desc: "Filter by directory"},
			{Name: "glob", Type: FlagString, Default: "", Desc: "Filter by file glob"},
			{Name: "type", Type: FlagString, Default: "", Desc: "Filter by symbol type"},
			{Name: "grep", Type: FlagString, Default: "", Desc: "Filter by name pattern"},
			{Name: "lang", Type: FlagString, Default: "", Desc: "Filter by language (e.g. go, python, javascript)"},
		},
	},
	{
		Name: "search", Desc: "Search symbols or text. Auto-detects mode from flags.",
		Category: CatRead, MinArgs: 1, MaxArgs: 1,
		BodyTrack: true,
		Flags: []FlagSpec{
			{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response tokens (default 2000)"},
			{Name: "full", Type: FlagBool, Default: false, Desc: "Return full results (no default budget cap)"},
			{Name: "text", Type: FlagBool, Default: false, Desc: "Text search mode"},
			{Name: "include", Type: FlagStringSlice, Default: []string(nil), Desc: "File glob(s) to include"},
			{Name: "exclude", Type: FlagStringSlice, Default: []string(nil), Desc: "File glob(s) to exclude"},
			{Name: "context", Type: FlagInt, Default: 0, Desc: "Lines of context"},
			{Name: "limit", Type: FlagInt, Default: 0, Desc: "Max number of results to return"},
			{Name: "regex", Type: FlagBool, Default: false, Desc: "Use regex matching"},
			{Name: "body", Type: FlagBool, Default: true, Desc: "Include symbol body in results (use --no-body to suppress)"},
			{Name: "no_group", Type: FlagBool, Default: false, Desc: "Disable file grouping in text results"},
			{Name: "in", Type: FlagString, Default: "", Desc: "Search within a symbol body (file:Symbol) or line range (file:N-M)"},
			{Name: "lines", Type: FlagString, Default: "", Desc: "Line range (e.g. 4200:4260)"},
		},
	},
	{
		Name: "refs", Desc: "Find all references to a symbol. --impact for transitive callers.",
		Category: CatRead, MinArgs: 1, MaxArgs: 2, FileScoped: true,
		DeltaRead: true, BodyTrack: true,
		Flags: []FlagSpec{
			{Name: "impact", Type: FlagBool, Default: false, Desc: "Transitive callers"},
			{Name: "chain", Type: FlagString, Default: "", Desc: "Find call path to target"},
			{Name: "depth", Type: FlagInt, Default: 3, Desc: "Max traversal depth"},
			{Name: "callers", Type: FlagBool, Default: false, Desc: "Include callers"},
			{Name: "deps", Type: FlagBool, Default: false, Desc: "Include dependencies"},
			{Name: "body", Type: FlagBool, Default: false, Desc: "Include source in results"},
			{Name: "signatures", Type: FlagBool, Default: false, Desc: "Signatures only"},
			{Name: "budget", Type: FlagInt, Default: 0, Desc: "Max response tokens"},
		},
	},
	{
		Name: "rename", Desc: "Cross-file rename with import-aware resolution. --dry-run to preview.",
		Category: CatGlobalMutate, MinArgs: 2, MaxArgs: 2,
		Flags: []FlagSpec{
			{Name: "dry_run", Type: FlagBool, Default: false, Desc: "Preview without applying"},
		},
	},
	{
		Name: "reset", Desc: "Clean slate: reindex, clear session, clear checkpoints.",
		Category: CatGlobalMutate, MinArgs: 0, MaxArgs: 0,
		Flags: []FlagSpec{
			{Name: "index", Type: FlagBool, Default: false, Desc: "Rebuild index only"},
			{Name: "session", Type: FlagBool, Default: false, Desc: "Clear session only"},
		},
	},
	{
		Name: "verify", Desc: "Run build/typecheck or tests. Auto-detects go/npm/cargo.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 0,
		Flags: []FlagSpec{
			{Name: "command", Type: FlagString, Default: "", Desc: "Custom command (auto-detect if omitted)"},
			{Name: "level", Type: FlagString, Default: "", Desc: "Verification level: build (default) or test"},
			{Name: "test", Type: FlagBool, Default: false, Desc: "Shorthand for --level test"},
			{Name: "timeout", Type: FlagInt, Default: 0, Desc: "Timeout in seconds"},
		},
	},
	{
		Name: "run", Desc: "Run a command, show only what changed since last run.",
		Category: CatMeta, MinArgs: 1, MaxArgs: -1,
		Flags: []FlagSpec{
			{Name: "full", Type: FlagBool, Default: false, Desc: "Bypass diff, show full output"},
			{Name: "reset", Type: FlagBool, Default: false, Desc: "Clear baseline before running (treat as first run)"},
		},
	},
	{
		Name: "context", Desc: "Session context: recent ops, build state, action items.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 0,
		Flags: []FlagSpec{
			{Name: "focus", Type: FlagString, Default: "", Desc: "Set session focus (empty string clears)"},
			{Name: "count", Type: FlagInt, Default: 10, Desc: "Number of recent ops to show"},
		},
	},
	{
		Name: "checkpoint", Desc: "Snapshot or restore session state. Subcommands: create, restore, list, diff, drop.",
		Category: CatMeta, MinArgs: 0, MaxArgs: 1,
		Flags: []FlagSpec{
			{Name: "label", Type: FlagString, Default: "", Desc: "Label for the checkpoint"},
			{Name: "restore", Type: FlagString, Default: "", Desc: "Restore to checkpoint ID"},
			{Name: "list", Type: FlagBool, Default: false, Desc: "List all checkpoints"},
			{Name: "diff", Type: FlagString, Default: "", Desc: "Show changes since checkpoint ID"},
			{Name: "drop", Type: FlagString, Default: "", Desc: "Delete a checkpoint"},
			{Name: "no_save", Type: FlagBool, Default: false, Desc: "Skip pre-restore safety checkpoint"},
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
	// Hidden command aliases: old names → new specs
	byName["reindex"] = byName["reset"]
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

// IsRead returns true for read-category commands (read, map, search, refs).
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

// IsDeltaRead returns true for commands that support delta reads (read, refs).
func IsDeltaRead(name string) bool {
	s := byName[name]
	return s != nil && s.DeltaRead
}

// IsBodyTrack returns true for commands whose bodies are tracked for dedup (read, refs, search).
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
		"reindex": true, "init": true, "read_after_edit": true,
	}
}

// ReadBatchKeys returns valid keys for doRead batch objects.
func ReadBatchKeys() map[string]bool {
	m := flagNames("read")
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

// WriteBatchKeys returns valid keys for doWrite batch objects.
func WriteBatchKeys() map[string]bool {
	m := flagNames("write")
	m["file"] = true
	return m
}

// RenameBatchKeys returns valid keys for doRename batch objects.
func RenameBatchKeys() map[string]bool {
	m := flagNames("rename")
	m["old_name"] = true
	m["new_name"] = true
	return m
}

// QueryBatchKeys returns valid keys for doQuery batch objects.
// This is the union of all read-category commands' flag names,
// plus structural fields (cmd, file, symbol, pattern).
func QueryBatchKeys() map[string]bool {
	m := map[string]bool{
		"cmd": true, "file": true, "symbol": true,
		"budget": true, "pattern": true,
	}
	for _, s := range Registry {
		if s.Category == CatRead {
			for _, f := range s.Flags {
				m[f.Name] = true
			}
		}
	}
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
