package namespace

// CppClassHierarchy records one C++ class/struct declaration's
// supers and the byte range of its body.
//
//	class Foo : public Bar, protected Baz, private Qux { … }
//	  Name="Foo"  Supers=["Bar","Baz","Qux"]
type CppClassHierarchy struct {
	Name      string
	IsStruct  bool
	Supers    []string
	BodyStart uint32
	BodyEnd   uint32
}

// CppFindClassHierarchy scans C++ source for class/struct
// declarations with optional `: public/protected/private Bar`
// inheritance lists.
//
// Recognized:
//
//	class Foo { … };                      (no bases)
//	class Foo : public Bar { … };
//	struct Foo : Bar, public Baz { … };
//	template <typename T> class Foo : public Bar<T> { … };
//
// Forward declarations (`class Foo;`) are skipped — they have a
// `;` instead of `{`.
//
// v1 limits:
//   - Templates: generic params on the class are tolerated (the
//     `<T>` after the name is skipped); generic args on the
//     supers (`Bar<T>`) collapse to the bare ident.
//   - Nested classes inside other classes/namespaces aren't
//     captured; only top-level / namespace-scope decls.
func CppFindClassHierarchy(src []byte) []CppClassHierarchy {
	s := string(src)
	var out []CppClassHierarchy
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
		isStruct := false
		switch {
		case i+5 <= len(s) && s[i:i+5] == "class":
			kw = "class"
		case i+6 <= len(s) && s[i:i+6] == "struct":
			kw = "struct"
			isStruct = true
		}
		if kw == "" {
			i++
			continue
		}
		if i > 0 && isCppIdentByte(s[i-1]) {
			i++
			continue
		}
		after := i + len(kw)
		if after < len(s) && isCppIdentByte(s[after]) {
			i++
			continue
		}
		j := skipCppSpace(s, after)
		nameStart := j
		for j < len(s) && isCppIdentByte(s[j]) {
			j++
		}
		if j == nameStart {
			i = j + 1
			continue
		}
		name := s[nameStart:j]
		j = skipCppSpace(s, j)
		// Generic params `<T, …>` on the class itself.
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
		j = skipCppSpace(s, j)
		// Forward decl: ends with `;` before `{`. Skip.
		if j < len(s) && s[j] == ';' {
			i = j + 1
			continue
		}
		var supers []string
		if j < len(s) && s[j] == ':' {
			j++
			j = readCppSuperList(s, j, &supers)
		}
		// Skip until the opening brace; `final` keyword may
		// appear between supers and body.
		for j < len(s) && s[j] != '{' && s[j] != ';' {
			j++
		}
		if j >= len(s) || s[j] != '{' {
			i = j + 1
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
		out = append(out, CppClassHierarchy{
			Name:      name,
			IsStruct:  isStruct,
			Supers:    supers,
			BodyStart: uint32(bodyStart),
			BodyEnd:   uint32(k),
		})
		i = k
	}
	return out
}

// readCppSuperList reads a comma-separated list of super-class
// specifiers after the `:` in a class/struct head. Each specifier
// can carry an access keyword (`public`/`protected`/`private`)
// and `virtual`. The base ident (last segment of qualified
// names, generics stripped) is appended to supers.
func readCppSuperList(s string, j int, supers *[]string) int {
	for j < len(s) {
		j = skipCppSpace(s, j)
		if j >= len(s) || s[j] == '{' || s[j] == ';' {
			return j
		}
		// Skip access / virtual keywords.
		for {
			j = skipCppSpace(s, j)
			advanced := false
			for _, kw := range []string{"public", "protected", "private", "virtual"} {
				if j+len(kw) <= len(s) && s[j:j+len(kw)] == kw {
					end := j + len(kw)
					if end == len(s) || !isCppIdentByte(s[end]) {
						j = end
						advanced = true
						break
					}
				}
			}
			if !advanced {
				break
			}
		}
		j = skipCppSpace(s, j)
		// Read qualified ident `ns::ns::Name` or just `Name`.
		start := j
		for j < len(s) && (isCppIdentByte(s[j]) || s[j] == ':') {
			j++
		}
		if j > start {
			ident := s[start:j]
			// Strip namespace prefixes — keep last `::`-separated segment.
			for k := len(ident) - 2; k >= 0; k-- {
				if ident[k] == ':' && ident[k+1] == ':' {
					ident = ident[k+2:]
					break
				}
			}
			*supers = append(*supers, ident)
		}
		j = skipCppSpace(s, j)
		// Generic args `<T>`
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
		j = skipCppSpace(s, j)
		if j < len(s) && s[j] == ',' {
			j++
			continue
		}
		return j
	}
	return j
}

func skipCppSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isCppIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// CppRelatedTypes returns the transitive set of class/struct
// names related to className via inheritance in src.
func CppRelatedTypes(src []byte, className string) []string {
	hier := CppFindClassHierarchy(src)
	byName := map[string]CppClassHierarchy{}
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
