// Package ledger defines the output contract for semantic commands that
// classify occurrences — rename and find-refs. See spec docs for the full
// design; this file is the canonical type surface.
package ledger

// SchemaVersion is the wire-format version. Clients should verify this.
const SchemaVersion = "ledger/1"

// Command identifies which semantic command produced this ledger.
type Command string

const (
	CommandRename   Command = "rename"
	CommandFindRefs Command = "find-refs"
)

// Scope describes the file range the resolver searched.
type Scope string

const (
	ScopeSameFile  Scope = "same-file"
	ScopeCrossFile Scope = "cross-file"
)

// Tier is the classification bucket for a site.
type Tier string

const (
	TierDefinite          Tier = "definite"
	TierAmbiguousDispatch Tier = "ambiguous-dispatch"
	TierAmbiguousImport   Tier = "ambiguous-import"
	TierShadowed          Tier = "shadowed"
	TierLexicalNoise      Tier = "lexical-noise"
)

// TierOrder is the canonical iteration order for tiers in all outputs.
var TierOrder = []Tier{
	TierDefinite,
	TierAmbiguousDispatch,
	TierAmbiguousImport,
	TierShadowed,
	TierLexicalNoise,
}

// shortIDLetter returns the one-letter prefix used in ShortIDs for the tier.
func shortIDLetter(t Tier) string {
	switch t {
	case TierDefinite:
		return "d"
	case TierAmbiguousDispatch:
		return "m"
	case TierAmbiguousImport:
		return "i"
	case TierShadowed:
		return "s"
	case TierLexicalNoise:
		return "n"
	}
	return "?"
}

// Role describes what kind of occurrence a site is, lexically.
type Role string

const (
	RoleDef           Role = "def"
	RoleDecl          Role = "decl"
	RoleCall          Role = "call"
	RoleRef           Role = "ref"
	RoleField         Role = "field"
	RoleComment       Role = "comment"
	RoleString        Role = "string"
	RoleUnrelatedDecl Role = "unrelated-decl"
)

// ReasonCode is the machine-readable justification for a site's tier.
type ReasonCode string

const (
	ReasonResolvedDef           ReasonCode = "resolved-def"
	ReasonResolvedDecl          ReasonCode = "resolved-decl"
	ReasonResolvedCall          ReasonCode = "resolved-call"
	ReasonResolvedRef           ReasonCode = "resolved-ref"
	ReasonScopeShadow           ReasonCode = "scope-shadow"
	ReasonInsideComment         ReasonCode = "inside-comment"
	ReasonInsideString          ReasonCode = "inside-string"
	ReasonStructFieldKey        ReasonCode = "struct-field-key"
	ReasonUnrelatedDeclSameName ReasonCode = "unrelated-decl-same-name"
	ReasonUnresolvedDispatch    ReasonCode = "unresolved-dispatch"
	ReasonUnresolvedImport      ReasonCode = "unresolved-import"
)

// Ledger is the canonical output of a resolve+classify pass.
type Ledger struct {
	Version        string         `json:"version"`
	Command        Command        `json:"command"`
	Target         Target         `json:"target"`
	Scope          Scope          `json:"scope"`
	Filter         *Filter        `json:"filter,omitempty"`
	Sites          []Site         `json:"sites"`
	Counts         map[Tier]int   `json:"counts"`
	ExcludedCounts map[Tier]int   `json:"excluded_counts,omitempty"`
	Total          int            `json:"total"`
	NextActions    []Action       `json:"next_actions"`
	Warnings       []Warning      `json:"warnings,omitempty"`
	Render         *RenderHints   `json:"render,omitempty"`

	Rename   *RenamePayload   `json:"rename,omitempty"`
	FindRefs *FindRefsPayload `json:"find_refs,omitempty"`
}

// Target describes the symbol the command is operating on.
type Target struct {
	Name      string `json:"name"`
	File      string `json:"file"`
	Line      int    `json:"line"`
	Kind      string `json:"kind"`
	Signature string `json:"signature,omitempty"`
}

// Site is one classified occurrence.
type Site struct {
	SiteKey    string          `json:"site_key"`
	ShortID    string          `json:"short_id"`
	File       string          `json:"file"`
	ByteRange  [2]int          `json:"byte_range"`
	Line       int             `json:"line"`
	Col        int             `json:"col"`
	Tier       Tier            `json:"tier"`
	Role       Role            `json:"role"`
	Container  []ContainerStep `json:"container,omitempty"`
	ReasonCode ReasonCode      `json:"reason_code"`
	Reason     string          `json:"reason,omitempty"`
	Snippet    Snippet         `json:"snippet"`
}

// ContainerStep is one level in the qualified container path.
type ContainerStep struct {
	Kind string `json:"kind"` // "package" | "file" | "class" | "struct" | "function" | "method" | "block"
	Name string `json:"name"` // may be "" for anonymous
}

// Snippet is source context around a site.
type Snippet struct {
	ContextRange [2]int   `json:"context_range"`
	Lines        []string `json:"lines"`
	Before       []string `json:"before,omitempty"`
	After        []string `json:"after,omitempty"`
}

// Filter describes user-provided narrowing predicates, AND'd together.
type Filter struct {
	Predicates     []Predicate `json:"predicates"`
	PreFilterTotal int         `json:"pre_filter_total"`
}

// Predicate is one field=value (or field~=value) narrowing term.
type Predicate struct {
	Field string `json:"field"` // "file" | "container"
	Op    string `json:"op"`    // "glob" | "eq" | "suffix"
	Value string `json:"value"`
}

// Action is one entry in the suggested next_actions menu.
type Action struct {
	Label   string   `json:"label"`
	Kind    string   `json:"kind"` // "apply" | "expand" | "rescope" | "refine"
	Cmd     []string `json:"cmd"`
	Applies int      `json:"applies,omitempty"`
}

// Warning is a structured notice emitted by the resolver/classifier.
type Warning struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// RenderHints carries plain-rendering metadata. JSON clients can ignore.
type RenderHints struct {
	ShownCounts map[Tier]int `json:"shown_counts,omitempty"`
	Truncated   map[Tier]int `json:"truncated,omitempty"`
}

// RenamePayload is the rename-specific command payload.
type RenamePayload struct {
	From    string          `json:"from"`
	To      string          `json:"to"`
	Edits   map[string]Edit `json:"edits"`
	Applied *ApplyResult    `json:"applied,omitempty"`
}

// Edit is the proposed byte-level change at one site.
type Edit struct {
	OldBytes    string `json:"old_bytes"`
	Replacement string `json:"replacement"`
}

// ApplyResult records what an apply pass did. Present only after --apply-*.
type ApplyResult struct {
	Tiers []Tier   `json:"tiers,omitempty"`
	Keys  []string `json:"keys"`
	Files []string `json:"files"`
	OK    bool     `json:"ok"`
}

// FindRefsPayload is reserved; find-refs carries no command-specific state in v1.
type FindRefsPayload struct{}
