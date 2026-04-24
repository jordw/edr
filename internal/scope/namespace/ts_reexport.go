package namespace

import (
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// tsReExport describes one `export … from '…'` clause found in a TS/JS
// barrel file. Each named clause becomes one tsReExport; `export *
// from '…'` sets star=true.
type tsReExport struct {
	origName  string // name as exported by the source module
	localName string // name under which this barrel re-exports
	modPath   string // module specifier (relative or tsconfig-path)
	star      bool   // true for `export * from '…'`
}

// findTSReExports scans src for `export { … } from '…'`, `export
// { X as Y } from '…'`, and `export * from '…'` clauses. Returns
// them in source order. Comments and string-literal contents are
// ignored via a minimal state machine.
//
// Intentional v1 limits:
//   - Multi-line blocks with unusual whitespace are handled;
//     deeply broken formatting may not be.
//   - `export { default as X } from '…'` re-exports the default
//     under X but the underlying decl is a default export which the
//     TS builder doesn't emit with a stable name. Parsed, but the
//     populator may not resolve it.
//   - `export type { X } from '…'` is treated identically to value
//     re-exports (type-only imports don't change rename behavior).
func findTSReExports(src []byte) []tsReExport {
	s := string(src)
	var out []tsReExport
	i := 0
	for i < len(s) {
		// Skip comments and strings.
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
			// Line comment to end of line.
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}
		if s[i] == '"' || s[i] == '\'' || s[i] == '`' {
			quote := s[i]
			i++
			for i < len(s) && s[i] != quote {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		// Look for the keyword `export` at a word boundary.
		if !(s[i] == 'e' && i+6 <= len(s) && s[i:i+6] == "export") {
			i++
			continue
		}
		if i > 0 {
			prev := s[i-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') ||
				(prev >= '0' && prev <= '9') || prev == '_' || prev == '$' {
				i++
				continue
			}
		}
		if i+6 < len(s) {
			next := s[i+6]
			if (next >= 'a' && next <= 'z') || (next >= 'A' && next <= 'Z') ||
				(next >= '0' && next <= '9') || next == '_' || next == '$' {
				i++
				continue
			}
		}
		// Parse the `export` clause.
		j := i + 6
		j = skipTSSpace(s, j)
		// Skip `type ` keyword if present.
		if j+4 < len(s) && s[j:j+4] == "type" {
			after := j + 4
			if after < len(s) && (s[after] == ' ' || s[after] == '\t' || s[after] == '\n') {
				j = skipTSSpace(s, after)
			}
		}
		if j < len(s) && s[j] == '*' {
			// export * from 'mod'; OR export * as Ns from 'mod';
			j++
			j = skipTSSpace(s, j)
			if j+2 < len(s) && s[j:j+2] == "as" {
				j += 2
				j = skipTSSpace(s, j)
				// Skip the alias ident.
				for j < len(s) && isTSIdentByte(s[j]) {
					j++
				}
				j = skipTSSpace(s, j)
			}
			if j+4 <= len(s) && s[j:j+4] == "from" {
				j += 4
				j = skipTSSpace(s, j)
				mod, after, ok := readTSStringLiteral(s, j)
				if ok {
					out = append(out, tsReExport{star: true, modPath: mod})
					i = after
					continue
				}
			}
			i = j
			continue
		}
		if j < len(s) && s[j] == '{' {
			// export { a, b as c, d } from 'mod';
			j++
			braceEnd := strings.IndexByte(s[j:], '}')
			if braceEnd < 0 {
				i = j
				continue
			}
			specs := s[j : j+braceEnd]
			j += braceEnd + 1
			j = skipTSSpace(s, j)
			if j+4 > len(s) || s[j:j+4] != "from" {
				i = j
				continue
			}
			j += 4
			j = skipTSSpace(s, j)
			mod, after, ok := readTSStringLiteral(s, j)
			if !ok {
				i = j
				continue
			}
			for _, raw := range strings.Split(specs, ",") {
				raw = strings.TrimSpace(raw)
				if raw == "" {
					continue
				}
				orig, local := parseTSExportSpec(raw)
				if orig == "" || local == "" {
					continue
				}
				out = append(out, tsReExport{
					origName:  orig,
					localName: local,
					modPath:   mod,
				})
			}
			i = after
			continue
		}
		i = j
	}
	return out
}

// parseTSExportSpec parses "foo", "foo as bar", "type foo", or
// "type foo as bar" into (origName, localName).
func parseTSExportSpec(spec string) (string, string) {
	spec = strings.TrimSpace(spec)
	spec = strings.TrimPrefix(spec, "type ")
	spec = strings.TrimSpace(spec)
	parts := strings.Split(spec, " as ")
	if len(parts) == 1 {
		name := strings.TrimSpace(parts[0])
		return name, name
	}
	if len(parts) == 2 {
		return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	}
	return "", ""
}

func skipTSSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isTSIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_' || c == '$'
}

// readTSStringLiteral reads a quoted string starting at s[i], returning
// the unquoted contents and the index after the closing quote.
func readTSStringLiteral(s string, i int) (string, int, bool) {
	if i >= len(s) {
		return "", i, false
	}
	q := s[i]
	if q != '"' && q != '\'' && q != '`' {
		return "", i, false
	}
	start := i + 1
	j := start
	for j < len(s) && s[j] != q {
		if s[j] == '\\' && j+1 < len(s) {
			j += 2
			continue
		}
		j++
	}
	if j >= len(s) {
		return "", i, false
	}
	return s[start:j], j + 1, true
}

// resolveTSBarrel chases `export { name } from '…'` chains, returning
// the ultimate DeclID and file path where `name` is actually
// declared. Returns nil if the chain can't be resolved.
//
// The `visited` set prevents infinite loops on circular barrels.
// Depth is capped at 8 as a defensive limit.
type tsBarrelHit struct {
	decl *scope.Decl
	file string
}

func resolveTSBarrel(r *TSResolver, file, name string, visited map[string]bool, depth int) *tsBarrelHit {
	if depth > 8 {
		return nil
	}
	if visited[file] {
		return nil
	}
	visited[file] = true
	res := r.Result(file)
	if res == nil {
		return nil
	}
	// Direct hit: file has a file-scope decl with that name.
	for i := range res.Decls {
		d := &res.Decls[i]
		if d.Name != name || d.Scope != scope.ScopeID(1) {
			continue
		}
		if d.Kind == scope.KindImport {
			continue
		}
		return &tsBarrelHit{decl: d, file: file}
	}
	// Scan re-exports.
	src := r.Source(file)
	for _, re := range findTSReExports(src) {
		if re.star {
			// Pursue star re-exports recursively.
			for _, next := range r.FilesForImport(re.modPath, file) {
				if hit := resolveTSBarrel(r, next, name, visited, depth+1); hit != nil {
					return hit
				}
			}
			continue
		}
		if re.localName != name {
			continue
		}
		for _, next := range r.FilesForImport(re.modPath, file) {
			if hit := resolveTSBarrel(r, next, re.origName, visited, depth+1); hit != nil {
				return hit
			}
		}
	}
	return nil
}


// ResolveTSBarrelForDispatch is the dispatch-package-facing wrapper
// around resolveTSBarrel. Returns the path of the file declaring
// name (chasing `export { name } from '…'` chains), or "".
func ResolveTSBarrelForDispatch(r *TSResolver, file, name string) string {
	visited := map[string]bool{}
	hit := resolveTSBarrel(r, file, name, visited, 0)
	if hit == nil {
		return ""
	}
	return hit.file
}

// TSReExportSpan is a re-export clause annotated with source byte
// ranges, for the dispatch package to emit rename spans.
type TSReExportSpan struct {
	OrigName       string
	LocalName      string
	ModPath        string
	Star           bool
	OrigNameStart  uint32
	OrigNameEnd    uint32
}

// FindTSReExportsWithSpans is the span-carrying variant of
// findTSReExports. Used by the dispatch layer to locate the exact
// byte range of `name` inside `export { name } from '…'` so the
// rename can rewrite just that token.
func FindTSReExportsWithSpans(src []byte) []TSReExportSpan {
	var out []TSReExportSpan
	s := string(src)
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '/' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if i+1 < len(s) && s[i] == '/' && s[i+1] == '*' {
			i += 2
			for i+1 < len(s) && !(s[i] == '*' && s[i+1] == '/') {
				i++
			}
			if i+1 < len(s) {
				i += 2
			}
			continue
		}
		if s[i] == '"' || s[i] == '\'' || s[i] == '`' {
			q := s[i]
			i++
			for i < len(s) && s[i] != q {
				if s[i] == '\\' && i+1 < len(s) {
					i += 2
					continue
				}
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		if !(s[i] == 'e' && i+6 <= len(s) && s[i:i+6] == "export") {
			i++
			continue
		}
		if i > 0 {
			prev := s[i-1]
			if isTSIdentByte(prev) {
				i++
				continue
			}
		}
		if i+6 < len(s) {
			next := s[i+6]
			if isTSIdentByte(next) {
				i++
				continue
			}
		}
		j := i + 6
		j = skipTSSpace(s, j)
		if j+4 < len(s) && s[j:j+4] == "type" {
			after := j + 4
			if after < len(s) && (s[after] == ' ' || s[after] == '\t' || s[after] == '\n') {
				j = skipTSSpace(s, after)
			}
		}
		if j < len(s) && s[j] == '*' {
			j++
			j = skipTSSpace(s, j)
			if j+2 < len(s) && s[j:j+2] == "as" {
				j += 2
				j = skipTSSpace(s, j)
				for j < len(s) && isTSIdentByte(s[j]) {
					j++
				}
				j = skipTSSpace(s, j)
			}
			if j+4 <= len(s) && s[j:j+4] == "from" {
				j += 4
				j = skipTSSpace(s, j)
				mod, after, ok := readTSStringLiteral(s, j)
				if ok {
					out = append(out, TSReExportSpan{Star: true, ModPath: mod})
					i = after
					continue
				}
			}
			i = j
			continue
		}
		if j < len(s) && s[j] == '{' {
			braceStart := j + 1
			braceEnd := strings.IndexByte(s[braceStart:], '}')
			if braceEnd < 0 {
				i = j
				continue
			}
			braceEndAbs := braceStart + braceEnd
			j = braceEndAbs + 1
			j = skipTSSpace(s, j)
			if j+4 > len(s) || s[j:j+4] != "from" {
				i = j
				continue
			}
			j += 4
			j = skipTSSpace(s, j)
			mod, after, ok := readTSStringLiteral(s, j)
			if !ok {
				i = j
				continue
			}
			// Walk each comma-separated spec inside { }, tracking
			// the ORIGINAL-name byte range relative to src.
			specStart := braceStart
			for p := braceStart; p <= braceEndAbs; p++ {
				if p == braceEndAbs || s[p] == ',' {
					if p > specStart {
						raw := strings.TrimLeft(s[specStart:p], " \t\n\r")
						lead := (p - len(raw)) - 0 // spec starts after leading space; we'll recompute via offset
						_ = lead
						// Compute offset of the first ident in raw within src
						// by trimming whitespace.
						offset := specStart
						for offset < p && (s[offset] == ' ' || s[offset] == '\t' || s[offset] == '\n' || s[offset] == '\r') {
							offset++
						}
						// Optional `type ` prefix.
						if offset+5 <= p && s[offset:offset+5] == "type " {
							offset += 5
							for offset < p && (s[offset] == ' ' || s[offset] == '\t') {
								offset++
							}
						}
						// origName is ident starting at offset.
						origStart := offset
						origEnd := offset
						for origEnd < p && isTSIdentByte(s[origEnd]) {
							origEnd++
						}
						origName := s[origStart:origEnd]
						// Detect ` as ` following.
						after := origEnd
						for after < p && (s[after] == ' ' || s[after] == '\t' || s[after] == '\n') {
							after++
						}
						localName := origName
						if after+2 <= p && s[after:after+2] == "as" {
							after += 2
							for after < p && (s[after] == ' ' || s[after] == '\t') {
								after++
							}
							localStart := after
							localEnd := after
							for localEnd < p && isTSIdentByte(s[localEnd]) {
								localEnd++
							}
							localName = s[localStart:localEnd]
						}
						if origName != "" && localName != "" {
							out = append(out, TSReExportSpan{
								OrigName:      origName,
								LocalName:     localName,
								ModPath:       mod,
								OrigNameStart: uint32(origStart),
								OrigNameEnd:   uint32(origEnd),
							})
						}
					}
					specStart = p + 1
				}
			}
			i = after
			continue
		}
		i = j
	}
	return out
}
