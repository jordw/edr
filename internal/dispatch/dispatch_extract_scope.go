package dispatch

import (
	"sort"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	scopestore "github.com/jordw/edr/internal/scope/store"
)

// findExternalLocalsScope uses scope binding to identify identifiers in
// the extracted block that must be threaded through the new function
// call. It replaces the regex-based findExternalLocals for supported
// languages (Go/TS/JS/JSX/Python).
//
// A ref in the extracted range is "missing" when:
//   1. It resolves to a nested-scope decl (parameter, local var, etc.)
//   2. That decl sits outside the extracted range (so the new function
//      would not see it unless it becomes a parameter)
//   3. The user has not already threaded it via --call
//
// Refs to file-scope decls (globals, imports, the enclosing function
// itself) are skipped — they remain in scope inside any new function.
//
// Returns (missing, true) on success. Returns (nil, false) to signal
// the caller should fall back to the regex-based path.
func findExternalLocalsScope(sym *index.SymbolInfo, fileData []byte, startLine, endLine int, callExpr string) ([]string, bool) {
	if !scopeSupported(sym.File) {
		return nil, false
	}
	result := scopestore.Parse(sym.File, fileData)
	if result == nil {
		return nil, false
	}

	// Compute byte range of the extracted block from 1-based line numbers.
	lineStarts := []int{0}
	for i, b := range fileData {
		if b == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	if startLine < 1 || startLine-1 >= len(lineStarts) {
		return nil, false
	}
	extractStart := uint32(lineStarts[startLine-1])
	var extractEnd uint32
	if endLine < len(lineStarts) {
		extractEnd = uint32(lineStarts[endLine])
	} else {
		extractEnd = uint32(len(fileData))
	}

	declByID := make(map[scope.DeclID]*scope.Decl, len(result.Decls))
	for i := range result.Decls {
		declByID[result.Decls[i].ID] = &result.Decls[i]
	}

	// Parse --call "name(a, b)" into the set of already-threaded arg names.
	threaded := map[string]bool{}
	if callExpr != "" {
		if lp := strings.Index(callExpr, "("); lp >= 0 {
			if rp := strings.LastIndex(callExpr, ")"); rp > lp {
				for _, arg := range splitParams(callExpr[lp+1 : rp]) {
					threaded[strings.TrimSpace(arg)] = true
				}
			}
		}
	}

	missing := []string{}
	seen := map[string]bool{}
	for _, ref := range result.Refs {
		if ref.Span.StartByte < extractStart || ref.Span.EndByte > extractEnd {
			continue
		}
		if ref.Binding.Kind != scope.BindResolved || ref.Binding.Decl == 0 {
			continue
		}
		local, ok := declByID[ref.Binding.Decl]
		if !ok {
			continue
		}
		// File-scope decl: global/import/enclosing-func; stays in scope.
		if sid := int(local.Scope) - 1; sid >= 0 && sid < len(result.Scopes) {
			if result.Scopes[sid].Kind == scope.ScopeFile {
				continue
			}
		}
		// Decl inside the extracted range is a new local for the new
		// function; the caller does not need to thread it.
		if local.Span.StartByte >= extractStart && local.Span.EndByte <= extractEnd {
			continue
		}
		// Already threaded, or already recorded.
		if threaded[local.Name] || seen[local.Name] {
			continue
		}
		seen[local.Name] = true
		missing = append(missing, local.Name)
	}
	sort.Strings(missing)
	return missing, true
}
