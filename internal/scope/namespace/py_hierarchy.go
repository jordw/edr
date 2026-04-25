package namespace

// PyClassHierarchy records one Python class declaration's base
// classes and the byte range of its body. Populated by
// PyFindClassHierarchy.
//
// `class Foo(Bar, Mixin):`
//
//	Name = "Foo"  Bases = ["Bar", "Mixin"]
type PyClassHierarchy struct {
	Name      string
	Bases     []string
	BodyStart uint32 // first byte after the `:` (start of indented block)
	BodyEnd   uint32 // first byte at the dedent (start of next sibling stmt or EOF)
}

// PyFindClassHierarchy scans Python source for `class Name(...):`
// declarations, parsing each base list and the indented body span.
//
// Body span is approximated by the bytes from the line after the
// `class` line to the next dedented line at the original indent
// level. Comments and strings are tolerated.
//
// Limitations:
//   - Decorators above the class are skipped before parsing the
//     `class` keyword.
//   - Keyword bases like `metaclass=...` inside parens are
//     skipped.
//   - Nested classes inside other classes / functions are NOT
//     captured (only top-level / module-level class decls).
func PyFindClassHierarchy(src []byte) []PyClassHierarchy {
	s := string(src)
	var out []PyClassHierarchy
	i := 0
	for i < len(s) {
		// Each iteration must start at a line boundary or column 0.
		// Skip leading whitespace on the line for indentation track.
		lineStart := i
		indent := 0
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			indent++
			i++
		}
		// Empty line / EOF.
		if i >= len(s) || s[i] == '\n' {
			if i < len(s) {
				i++
			}
			continue
		}
		// Skip line comment.
		if s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		// Only consider class decls at indent 0 (top-level for v1).
		if indent != 0 {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		// Decorators (@deco) on lines preceding the class are
		// already at indent 0. We just skip them as separate lines
		// — the next class decl will be captured normally.
		if !(i+5 <= len(s) && s[i:i+5] == "class") {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		// Word-boundary check.
		if i+5 < len(s) && isPyIdentByte(s[i+5]) {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		_ = lineStart
		j := i + 5
		j = pySkipSpace(s, j)
		nameStart := j
		for j < len(s) && isPyIdentByte(s[j]) {
			j++
		}
		if j == nameStart {
			i = j + 1
			continue
		}
		name := s[nameStart:j]
		var bases []string
		j = pySkipSpace(s, j)
		if j < len(s) && s[j] == '(' {
			j++
			depth := 1
			specStart := j
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '(':
					depth++
				case ')':
					depth--
					if depth == 0 {
						bases = appendPyBase(bases, s[specStart:j])
					}
				case ',':
					if depth == 1 {
						bases = appendPyBase(bases, s[specStart:j])
						specStart = j + 1
					}
				}
				j++
			}
		}
		j = pySkipSpace(s, j)
		if j >= len(s) || s[j] != ':' {
			// Malformed — skip.
			for j < len(s) && s[j] != '\n' {
				j++
			}
			i = j
			continue
		}
		j++ // past `:`
		// Body: from the next line to the next non-blank line with
		// indent <= 0.
		for j < len(s) && s[j] != '\n' {
			j++
		}
		if j < len(s) {
			j++ // past newline
		}
		bodyStart := j
		for j < len(s) {
			lineIdx := j
			lineIndent := 0
			for j < len(s) && (s[j] == ' ' || s[j] == '\t') {
				lineIndent++
				j++
			}
			// Blank line / comment line — keep going.
			if j >= len(s) || s[j] == '\n' || s[j] == '#' {
				if j < len(s) {
					for j < len(s) && s[j] != '\n' {
						j++
					}
					if j < len(s) {
						j++
					}
				}
				continue
			}
			// Non-blank line at indent 0 → body ended.
			if lineIndent == 0 {
				j = lineIdx
				break
			}
			// Indented line — part of body. Skip past it.
			for j < len(s) && s[j] != '\n' {
				j++
			}
			if j < len(s) {
				j++
			}
		}
		out = append(out, PyClassHierarchy{
			Name:      name,
			Bases:     bases,
			BodyStart: uint32(bodyStart),
			BodyEnd:   uint32(j),
		})
		i = j
	}
	return out
}

func appendPyBase(bases []string, raw string) []string {
	// Trim whitespace, ignore keyword bases (`metaclass=...`),
	// strip generic-style brackets.
	end := len(raw)
	for end > 0 && (raw[end-1] == ' ' || raw[end-1] == '\t' || raw[end-1] == '\n' || raw[end-1] == '\r') {
		end--
	}
	start := 0
	for start < end && (raw[start] == ' ' || raw[start] == '\t' || raw[start] == '\n' || raw[start] == '\r') {
		start++
	}
	if start >= end {
		return bases
	}
	spec := raw[start:end]
	// Skip keyword args.
	for k := 0; k < len(spec); k++ {
		if spec[k] == '=' {
			return bases
		}
		if spec[k] == ' ' || spec[k] == '\t' {
			break
		}
	}
	// Strip subscript like `Generic[T]` → `Generic`.
	for k := 0; k < len(spec); k++ {
		if spec[k] == '[' || spec[k] == '(' {
			spec = spec[:k]
			break
		}
	}
	// Take the last segment of dotted names (typing.Optional → Optional).
	for k := len(spec) - 1; k >= 0; k-- {
		if spec[k] == '.' {
			spec = spec[k+1:]
			break
		}
	}
	spec2 := ""
	for _, c := range []byte(spec) {
		if isPyIdentByte(c) {
			spec2 += string(c)
		} else {
			break
		}
	}
	if spec2 != "" {
		bases = append(bases, spec2)
	}
	return bases
}

func pySkipSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func isPyIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// PyRelatedTypes returns the transitive set of class names related
// to className via inheritance (bases + descendants) within src.
func PyRelatedTypes(src []byte, className string) []string {
	hier := PyFindClassHierarchy(src)
	byName := map[string]PyClassHierarchy{}
	for _, h := range hier {
		byName[h.Name] = h
	}
	related := map[string]bool{}
	stack := []string{className}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if h, ok := byName[n]; ok {
			for _, b := range h.Bases {
				if !related[b] {
					related[b] = true
					stack = append(stack, b)
				}
			}
		}
		for _, h := range hier {
			for _, b := range h.Bases {
				if b == n && !related[h.Name] {
					related[h.Name] = true
					stack = append(stack, h.Name)
					break
				}
			}
		}
	}
	out := make([]string, 0, len(related))
	for k := range related {
		out = append(out, k)
	}
	return out
}
