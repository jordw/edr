package index

import (
	"fmt"
	"strings"
)

// FileError records a per-file failure during parsing.
type FileError struct {
	File  string `json:"file"`
	Phase string `json:"phase"`
	Err   error  `json:"-"`
	Msg   string `json:"error"`
}

// ImportInfo represents an import statement in a source file.
type ImportInfo struct {
	File       string // importing file path
	ImportPath string // raw import string
	Alias      string // "", ".", alias name, or "*"
}

// RefInfo represents a reference edge from one symbol to an identifier.
type RefInfo struct {
	FromSymbolID int64
	ToName       string
	Line         uint32
	Kind         string // "identifier", "type", "field", "call"
}

// AmbiguousSymbolError is returned when a symbol name resolves to multiple definitions.
type AmbiguousSymbolError struct {
	Name       string
	Root       string
	Candidates []SymbolInfo
}

func (e *AmbiguousSymbolError) Error() string {
	var parts []string
	for _, c := range e.Candidates {
		rel := c.File
		if e.Root != "" && strings.HasPrefix(rel, e.Root+"/") {
			rel = rel[len(e.Root)+1:]
		}
		parts = append(parts, fmt.Sprintf("%s:%d (%s)", rel, c.StartLine, c.Type))
	}
	return fmt.Sprintf("symbol %q is ambiguous (%d definitions): %s — use [file] <symbol> to disambiguate",
		e.Name, len(e.Candidates), strings.Join(parts, ", "))
}

// preferDefinition picks the struct/class/type definition from a list of same-name symbols.
func preferDefinition(results []SymbolInfo) *SymbolInfo {
	typeKinds := map[string]bool{
		"type": true, "struct": true, "class": true,
		"enum": true, "interface": true, "module": true,
	}
	var types []int
	for i, s := range results {
		if typeKinds[s.Type] {
			types = append(types, i)
		}
	}
	if len(types) == 1 {
		return &results[types[0]]
	}
	return nil
}
