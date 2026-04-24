package namespace

import (
	"strings"
)

// tsFileDefaultExportName scans src for a top-level `export default`
// clause and returns the name being exported, or "" when the file
// has no default export or the default is an anonymous expression.
//
// Recognized forms:
//
//	export default function foo() {}
//	export default class Foo {}
//	export default foo;
//	export default foo = value;
//
// Comments and string literals are skipped. Only the first default
// export is returned (a file with two `export default` is a
// compile error in TS/JS anyway).
func tsFileDefaultExportName(src []byte) string {
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
		// Look for `export default` at a word boundary.
		if !(i+14 <= len(s) && s[i:i+6] == "export") {
			i++
			continue
		}
		if i > 0 && isTSIdentByte(s[i-1]) {
			i++
			continue
		}
		j := i + 6
		j = skipTSSpace(s, j)
		if j+7 > len(s) || s[j:j+7] != "default" {
			i++
			continue
		}
		j += 7
		if j < len(s) && isTSIdentByte(s[j]) {
			i++
			continue
		}
		j = skipTSSpace(s, j)
		// Skip `function`, `async function`, `class` keywords.
		for _, kw := range []string{"async function", "function", "class", "abstract class"} {
			if j+len(kw) <= len(s) && s[j:j+len(kw)] == kw {
				after := j + len(kw)
				if after < len(s) && !isTSIdentByte(s[after]) {
					j = skipTSSpace(s, after)
					break
				}
			}
		}
		// The next ident is the name being default-exported.
		nameStart := j
		for j < len(s) && isTSIdentByte(s[j]) {
			j++
		}
		if j > nameStart {
			return s[nameStart:j]
		}
		return ""
	}
	return ""
}

// tsCJSRequireBindings scans src for patterns of the form
//
//	const { a, b: renamed, c } = require('./mod')
//
// and
//
//	const mod = require('./mod')
//
// returning each binding as a spec. Each spec records the module
// path, the original name from the module, the local binding name,
// and the byte span of the original-name token in the source so
// the dispatch layer can emit a precise rewrite span for it.
//
// Intentional v1 limits:
//   - Dynamic `require` calls (variable arg) aren't tracked.
//   - Assignments like `let x = require(…).X` aren't tracked.
//   - Nested destructuring is not tracked.
type tsCJSBinding struct {
	ModPath       string
	OrigName      string
	LocalName     string
	OrigNameStart uint32
	OrigNameEnd   uint32
}

func tsFindCJSBindings(src []byte) []tsCJSBinding {
	s := string(src)
	var out []tsCJSBinding
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
		// Look for `const ` / `let ` / `var ` starting a require binding.
		keywordLen := 0
		for _, kw := range []string{"const ", "let ", "var "} {
			if i+len(kw) <= len(s) && s[i:i+len(kw)] == kw {
				if i > 0 && isTSIdentByte(s[i-1]) {
					break
				}
				keywordLen = len(kw)
				break
			}
		}
		if keywordLen == 0 {
			i++
			continue
		}
		j := i + keywordLen
		j = skipTSSpace(s, j)
		if j < len(s) && s[j] == '{' {
			braceStart := j + 1
			braceEnd := strings.IndexByte(s[braceStart:], '}')
			if braceEnd < 0 {
				i++
				continue
			}
			braceEndAbs := braceStart + braceEnd
			k := braceEndAbs + 1
			k = skipTSSpace(s, k)
			if k < len(s) && s[k] == '=' {
				k++
				k = skipTSSpace(s, k)
				if k+7 < len(s) && s[k:k+7] == "require" {
					after := k + 7
					if after < len(s) && !isTSIdentByte(s[after]) {
						after = skipTSSpace(s, after)
						if after < len(s) && s[after] == '(' {
							after++
							after = skipTSSpace(s, after)
							mod, modEnd, ok := readTSStringLiteral(s, after)
							if ok {
								_ = modEnd
								// Parse each spec inside braces.
								specStart := braceStart
								for p := braceStart; p <= braceEndAbs; p++ {
									if p == braceEndAbs || s[p] == ',' {
										if p > specStart {
											off := specStart
											for off < p && (s[off] == ' ' || s[off] == '\t' || s[off] == '\n' || s[off] == '\r') {
												off++
											}
											origStart := off
											origEnd := off
											for origEnd < p && isTSIdentByte(s[origEnd]) {
												origEnd++
											}
											origName := s[origStart:origEnd]
											after2 := origEnd
											for after2 < p && (s[after2] == ' ' || s[after2] == '\t') {
												after2++
											}
											localName := origName
											if after2 < p && s[after2] == ':' {
												after2++
												for after2 < p && (s[after2] == ' ' || s[after2] == '\t') {
													after2++
												}
												ls := after2
												le := after2
												for le < p && isTSIdentByte(s[le]) {
													le++
												}
												localName = s[ls:le]
											}
											if origName != "" && localName != "" {
												out = append(out, tsCJSBinding{
													ModPath:       mod,
													OrigName:      origName,
													LocalName:     localName,
													OrigNameStart: uint32(origStart),
													OrigNameEnd:   uint32(origEnd),
												})
											}
										}
										specStart = p + 1
									}
								}
							}
						}
					}
				}
			}
		}
		i = j
	}
	return out
}


// TSFileDefaultExportName is the dispatch-package-facing wrapper
// for default-export detection.
func TSFileDefaultExportName(src []byte) string {
	return tsFileDefaultExportName(src)
}

// TSCJSBinding mirrors tsCJSBinding for the dispatch package.
type TSCJSBinding struct {
	ModPath       string
	OrigName      string
	LocalName     string
	OrigNameStart uint32
	OrigNameEnd   uint32
}

// TSFindCJSBindings returns CJS `const { X } = require('…')` bindings
// in src.
func TSFindCJSBindings(src []byte) []TSCJSBinding {
	raw := tsFindCJSBindings(src)
	out := make([]TSCJSBinding, 0, len(raw))
	for _, b := range raw {
		out = append(out, TSCJSBinding{
			ModPath:       b.ModPath,
			OrigName:      b.OrigName,
			LocalName:     b.LocalName,
			OrigNameStart: b.OrigNameStart,
			OrigNameEnd:   b.OrigNameEnd,
		})
	}
	return out
}


// TSModuleExportsShorthand finds `module.exports = { … }` clauses
// with property-shorthand references to locally-declared names.
// Returns spans for each shorthand ident matching sym.Name.
//
// Only handles the simple top-level pattern:
//   module.exports = { a, b, c };
// Not:
//   module.exports.foo = foo;
//   exports.foo = foo;
//   Object.assign(module.exports, { … });
//
// sym.File is scanned directly by the dispatch layer; this helper
// returns byte ranges for the shorthand idents whose NAME matches
// the argument (typically sym.Name).
func TSModuleExportsShorthand(src []byte, name string) []TSReExportSpan {
	s := string(src)
	var out []TSReExportSpan
	needle := "module.exports"
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], needle)
		if idx < 0 {
			break
		}
		j := i + idx + len(needle)
		i = j // advance past in case this site doesn't match
		j = skipTSSpace(s, j)
		if j >= len(s) || s[j] != '=' {
			continue
		}
		j++
		j = skipTSSpace(s, j)
		if j >= len(s) || s[j] != '{' {
			continue
		}
		braceStart := j + 1
		braceEnd := strings.IndexByte(s[braceStart:], '}')
		if braceEnd < 0 {
			continue
		}
		braceEndAbs := braceStart + braceEnd
		specStart := braceStart
		for p := braceStart; p <= braceEndAbs; p++ {
			if p == braceEndAbs || s[p] == ',' {
				if p > specStart {
					off := specStart
					for off < p && (s[off] == ' ' || s[off] == '\t' || s[off] == '\n' || s[off] == '\r') {
						off++
					}
					nameStart := off
					nameEnd := off
					for nameEnd < p && isTSIdentByte(s[nameEnd]) {
						nameEnd++
					}
					ident := s[nameStart:nameEnd]
					// Shorthand only — `key: value` has a colon
					// between nameEnd and p. Skip those.
					after := nameEnd
					for after < p && (s[after] == ' ' || s[after] == '\t') {
						after++
					}
					if after < p && s[after] == ':' {
						specStart = p + 1
						continue
					}
					if ident == name {
						out = append(out, TSReExportSpan{
							OrigName:      ident,
							LocalName:     ident,
							OrigNameStart: uint32(nameStart),
							OrigNameEnd:   uint32(nameEnd),
						})
					}
				}
				specStart = p + 1
			}
		}
	}
	return out
}
