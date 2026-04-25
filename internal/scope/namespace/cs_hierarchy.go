package namespace

// CSClassHierarchy records one C# class/interface declaration's
// supers and the byte range of its body.
//
//	class Foo : Bar, IBaz, IQux { … }
//	  Name="Foo"  Supers=["Bar","IBaz","IQux"]
type CSClassHierarchy struct {
	Name        string
	IsInterface bool
	Supers      []string
	BodyStart   uint32
	BodyEnd     uint32
}

// CSFindClassHierarchy scans C# source for `class`, `interface`,
// `struct`, and `record` declarations with optional `: Foo, Bar`
// inheritance lists. C# uses a single `:` for both extends and
// implements (resolved at compile time).
//
// Generic params `<T, …>` after the name are tolerated; `where T :
// Constraint` clauses between the name and the body are skipped.
func CSFindClassHierarchy(src []byte) []CSClassHierarchy {
	s := string(src)
	var out []CSClassHierarchy
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
		if s[i] == '"' || s[i] == '\'' {
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
		var kw string
		isInterface := false
		switch {
		case i+5 <= len(s) && s[i:i+5] == "class":
			kw = "class"
		case i+9 <= len(s) && s[i:i+9] == "interface":
			kw = "interface"
			isInterface = true
		case i+6 <= len(s) && s[i:i+6] == "struct":
			kw = "struct"
		case i+6 <= len(s) && s[i:i+6] == "record":
			kw = "record"
		}
		if kw == "" {
			i++
			continue
		}
		if i > 0 && isCSIdentByte(s[i-1]) {
			i++
			continue
		}
		after := i + len(kw)
		if after < len(s) && isCSIdentByte(s[after]) {
			i++
			continue
		}
		j := skipCSSpace(s, after)
		nameStart := j
		for j < len(s) && isCSIdentByte(s[j]) {
			j++
		}
		if j == nameStart {
			i = j + 1
			continue
		}
		name := s[nameStart:j]
		j = skipCSSpace(s, j)
		// Generic params `<T, …>`
		if j < len(s) && s[j] == '<' {
			depth := 1
			j++
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '<':
					depth++
				case '>':
					depth--
				}
				j++
			}
		}
		j = skipCSSpace(s, j)
		var supers []string
		if j < len(s) && s[j] == ':' {
			j++
			j = readCSSuperList(s, j, &supers)
		}
		// Skip `where T : …` clauses up to `{`.
		for j < len(s) && s[j] != '{' {
			j++
		}
		if j >= len(s) || s[j] != '{' {
			i = j
			continue
		}
		bodyStart := j
		depth := 1
		k := j + 1
		for k < len(s) && depth > 0 {
			if k+1 < len(s) && s[k] == '/' && s[k+1] == '/' {
				for k < len(s) && s[k] != '\n' {
					k++
				}
				continue
			}
			if k+1 < len(s) && s[k] == '/' && s[k+1] == '*' {
				k += 2
				for k+1 < len(s) && !(s[k] == '*' && s[k+1] == '/') {
					k++
				}
				if k+1 < len(s) {
					k += 2
				}
				continue
			}
			if s[k] == '"' || s[k] == '\'' {
				q := s[k]
				k++
				for k < len(s) && s[k] != q {
					if s[k] == '\\' && k+1 < len(s) {
						k += 2
						continue
					}
					k++
				}
				if k < len(s) {
					k++
				}
				continue
			}
			switch s[k] {
			case '{':
				depth++
			case '}':
				depth--
			}
			k++
		}
		out = append(out, CSClassHierarchy{
			Name:        name,
			IsInterface: isInterface,
			Supers:      supers,
			BodyStart:   uint32(bodyStart),
			BodyEnd:     uint32(k),
		})
		i = k
	}
	return out
}

func readCSSuperList(s string, j int, supers *[]string) int {
	for j < len(s) {
		j = skipCSSpace(s, j)
		if j >= len(s) || s[j] == '{' {
			return j
		}
		// `where` clause (constraints) terminates the super list.
		if j+5 <= len(s) && s[j:j+5] == "where" && (j+5 == len(s) || !isCSIdentByte(s[j+5])) {
			return j
		}
		start := j
		for j < len(s) && (isCSIdentByte(s[j]) || s[j] == '.') {
			j++
		}
		if j > start {
			ident := s[start:j]
			if idx := lastDot(ident); idx >= 0 {
				ident = ident[idx+1:]
			}
			*supers = append(*supers, ident)
		}
		j = skipCSSpace(s, j)
		if j < len(s) && s[j] == '<' {
			depth := 1
			j++
			for j < len(s) && depth > 0 {
				switch s[j] {
				case '<':
					depth++
				case '>':
					depth--
				}
				j++
			}
		}
		j = skipCSSpace(s, j)
		if j < len(s) && s[j] == ',' {
			j++
			continue
		}
		return j
	}
	return j
}

func skipCSSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isCSIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// CSRelatedTypes returns the transitive set of class/interface
// names related to className via the inheritance graph in src.
func CSRelatedTypes(src []byte, className string) []string {
	hier := CSFindClassHierarchy(src)
	byName := map[string]CSClassHierarchy{}
	for _, h := range hier {
		byName[h.Name] = h
	}
	related := map[string]bool{}
	stack := []string{className}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if h, ok := byName[n]; ok {
			for _, b := range h.Supers {
				if !related[b] {
					related[b] = true
					stack = append(stack, b)
				}
			}
		}
		for _, h := range hier {
			for _, b := range h.Supers {
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
