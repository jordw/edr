// Package golang adds Go scope-builder helpers shared by package
// dispatch and namespace.
package golang

// PackageClause extracts the \`package foo\` clause from a Go source
// file. Returns "" if missing or malformed. Stops after the first
// non-comment statement.
//
// Shared helper so package dispatch and package namespace use the
// same parsing rules. Cheap byte scan — no allocation beyond the
// returned string.
func PackageClause(src []byte) string {
	i := 0
	n := len(src)
	for i < n {
		// Skip whitespace.
		for i < n && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r' || src[i] == '\n') {
			i++
		}
		if i >= n {
			return ""
		}
		// Line comment.
		if i+1 < n && src[i] == '/' && src[i+1] == '/' {
			for i < n && src[i] != '\n' {
				i++
			}
			continue
		}
		// Block comment.
		if i+1 < n && src[i] == '/' && src[i+1] == '*' {
			i += 2
			for i+1 < n && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i += 2
			continue
		}
		break
	}
	const kw = "package"
	if i+len(kw) > n || string(src[i:i+len(kw)]) != kw {
		return ""
	}
	i += len(kw)
	for i < n && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	start := i
	for i < n && isIdentByte(src[i]) {
		i++
	}
	return string(src[start:i])
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') ||
		(c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') ||
		c == '_'
}
