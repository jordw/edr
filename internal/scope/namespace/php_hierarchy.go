package namespace

// PHPClassHierarchy records one PHP class/interface/trait
// declaration's supers and the byte range of its body.
//
// `class Foo extends Bar implements I1, I2`
// `interface IFoo extends IBar`
// `trait T { use Other; }`
type PHPClassHierarchy struct {
	Name        string
	IsInterface bool
	IsTrait     bool
	Supers      []string
	BodyStart   uint32
	BodyEnd     uint32
}

// PHPFindClassHierarchy scans PHP source for class/interface/
// trait declarations with extends/implements clauses, and `use
// Trait` lines inside class bodies.
func PHPFindClassHierarchy(src []byte) []PHPClassHierarchy {
	s := string(src)
	var out []PHPClassHierarchy
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
		if s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
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
		isTrait := false
		switch {
		case i+5 <= len(s) && s[i:i+5] == "class":
			kw = "class"
		case i+9 <= len(s) && s[i:i+9] == "interface":
			kw = "interface"
			isInterface = true
		case i+5 <= len(s) && s[i:i+5] == "trait":
			kw = "trait"
			isTrait = true
		}
		if kw == "" {
			i++
			continue
		}
		if i > 0 && isPHPIdentByte(s[i-1]) {
			i++
			continue
		}
		after := i + len(kw)
		if after < len(s) && isPHPIdentByte(s[after]) {
			i++
			continue
		}
		j := skipPHPSpace(s, after)
		nameStart := j
		for j < len(s) && isPHPIdentByte(s[j]) {
			j++
		}
		if j == nameStart {
			i = j + 1
			continue
		}
		name := s[nameStart:j]
		var supers []string
		// `extends` and `implements` clauses up to `{`.
		for j < len(s) {
			j = skipPHPSpace(s, j)
			if j >= len(s) || s[j] == '{' {
				break
			}
			if j+7 <= len(s) && s[j:j+7] == "extends" && (j+7 == len(s) || !isPHPIdentByte(s[j+7])) {
				j += 7
				j = readPHPSuperList(s, j, &supers)
				continue
			}
			if j+10 <= len(s) && s[j:j+10] == "implements" && (j+10 == len(s) || !isPHPIdentByte(s[j+10])) {
				j += 10
				j = readPHPSuperList(s, j, &supers)
				continue
			}
			j++
		}
		if j >= len(s) || s[j] != '{' {
			i = j
			continue
		}
		bodyStart := j
		// Walk to matching `}`, depth-tracked.
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
			// `use Trait;` inside class body adds a super.
			if depth == 1 && k+3 <= len(s) && s[k:k+3] == "use" && (k == 0 || !isPHPIdentByte(s[k-1])) && (k+3 == len(s) || !isPHPIdentByte(s[k+3])) {
				k += 3
				k = skipPHPSpace(s, k)
				start := k
				for k < len(s) && (isPHPIdentByte(s[k]) || s[k] == '\\') {
					k++
				}
				if k > start {
					name := s[start:k]
					// Take last segment of namespaced names.
					for idx := len(name) - 1; idx >= 0; idx-- {
						if name[idx] == '\\' {
							name = name[idx+1:]
							break
						}
					}
					supers = append(supers, name)
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
		out = append(out, PHPClassHierarchy{
			Name:        name,
			IsInterface: isInterface,
			IsTrait:     isTrait,
			Supers:      supers,
			BodyStart:   uint32(bodyStart),
			BodyEnd:     uint32(k),
		})
		i = k
	}
	return out
}

func readPHPSuperList(s string, j int, supers *[]string) int {
	for j < len(s) {
		j = skipPHPSpace(s, j)
		if j >= len(s) || s[j] == '{' {
			return j
		}
		if j+7 <= len(s) && s[j:j+7] == "extends" && (!isPHPIdentByte(s[j+7])) {
			return j
		}
		if j+10 <= len(s) && s[j:j+10] == "implements" && (!isPHPIdentByte(s[j+10])) {
			return j
		}
		start := j
		for j < len(s) && (isPHPIdentByte(s[j]) || s[j] == '\\') {
			j++
		}
		if j > start {
			ident := s[start:j]
			for idx := len(ident) - 1; idx >= 0; idx-- {
				if ident[idx] == '\\' {
					ident = ident[idx+1:]
					break
				}
			}
			*supers = append(*supers, ident)
		}
		j = skipPHPSpace(s, j)
		if j < len(s) && s[j] == ',' {
			j++
			continue
		}
		return j
	}
	return j
}

func skipPHPSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r') {
		i++
	}
	return i
}

func isPHPIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// PHPRelatedTypes returns the transitive set of class/interface/
// trait names related to className.
func PHPRelatedTypes(src []byte, className string) []string {
	hier := PHPFindClassHierarchy(src)
	byName := map[string]PHPClassHierarchy{}
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
