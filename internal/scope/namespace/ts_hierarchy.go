package namespace

// TSClassHierarchy records one class/interface declaration's
// superclass + implemented-interface names, plus the byte range of
// the decl's body.
//
//	class Foo extends Bar implements I1, I2 { … }
//	  Name="Foo"  IsInterface=false  Supers=["Bar","I1","I2"]
type TSClassHierarchy struct {
	Name        string
	IsInterface bool
	Supers      []string
	BodyStart   uint32
	BodyEnd     uint32
}

// TSFindClassHierarchy scans TS/JS source for top-level class and
// interface declarations, parsing `extends` / `implements` clauses
// and recording the body span.
func TSFindClassHierarchy(src []byte) []TSClassHierarchy {
	s := string(src)
	var out []TSClassHierarchy
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
		var kw string
		isInterface := false
		if i+5 <= len(s) && s[i:i+5] == "class" {
			kw = "class"
		} else if i+9 <= len(s) && s[i:i+9] == "interface" {
			kw = "interface"
			isInterface = true
		}
		if kw == "" {
			i++
			continue
		}
		if i > 0 && isTSIdentByte(s[i-1]) {
			i++
			continue
		}
		after := i + len(kw)
		if after < len(s) && isTSIdentByte(s[after]) {
			i++
			continue
		}
		j := skipTSSpace(s, after)
		nameStart := j
		for j < len(s) && isTSIdentByte(s[j]) {
			j++
		}
		if j == nameStart {
			i = j + 1
			continue
		}
		name := s[nameStart:j]
		// Skip generic params <T, ...>
		j = skipTSSpace(s, j)
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
		var supers []string
		for j < len(s) && s[j] != '{' {
			j = skipTSSpace(s, j)
			if j+7 <= len(s) && s[j:j+7] == "extends" && (j+7 == len(s) || !isTSIdentByte(s[j+7])) {
				j += 7
				j = readTSSuperList(s, j, &supers)
				continue
			}
			if j+10 <= len(s) && s[j:j+10] == "implements" && (j+10 == len(s) || !isTSIdentByte(s[j+10])) {
				j += 10
				j = readTSSuperList(s, j, &supers)
				continue
			}
			if j >= len(s) {
				break
			}
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
			if s[k] == '"' || s[k] == '\'' || s[k] == '`' {
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
		out = append(out, TSClassHierarchy{
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

// readTSSuperList reads a comma-separated list of type refs
// following `extends` or `implements`, appends each ident (with
// generic params stripped) to supers, and returns the index after
// the last ref. Stops at `{`, `extends`, `implements`, or EOF.
func readTSSuperList(s string, j int, supers *[]string) int {
	for j < len(s) {
		j = skipTSSpace(s, j)
		if j >= len(s) || s[j] == '{' {
			return j
		}
		// Detect next clause keyword to stop this list.
		if j+10 <= len(s) && s[j:j+10] == "implements" && !isTSIdentByte(s[j+10]) {
			return j
		}
		if j+7 <= len(s) && s[j:j+7] == "extends" && !isTSIdentByte(s[j+7]) {
			return j
		}
		// Read identifier (possibly qualified like ns.Name).
		start := j
		for j < len(s) && (isTSIdentByte(s[j]) || s[j] == '.') {
			j++
		}
		if j > start {
			ident := s[start:j]
			// For qualified names, keep only the last segment
			// (that's what the rename target matches by name).
			if idx := lastDot(ident); idx >= 0 {
				ident = ident[idx+1:]
			}
			*supers = append(*supers, ident)
		}
		// Skip generic args <...>
		j = skipTSSpace(s, j)
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
		j = skipTSSpace(s, j)
		if j < len(s) && s[j] == ',' {
			j++
			continue
		}
		return j
	}
	return j
}

func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
