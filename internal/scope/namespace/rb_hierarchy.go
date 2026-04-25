package namespace

import "strings"

// RbClassHierarchy records one Ruby class/module declaration's
// supers and the byte range of its body.
//
// `class Foo < Bar`         → Supers=["Bar"]
// `module Mod ... include X` → Supers=["X"] (mixins)
type RbClassHierarchy struct {
	Name      string
	IsModule  bool
	Supers    []string
	BodyStart uint32
	BodyEnd   uint32
}

// RbFindClassHierarchy scans Ruby source for top-level `class
// Name < Super` and `module Name` declarations. Inside each body
// it records every `include X` / `extend X` / `prepend X` mixin
// as additional supers.
//
// Body span uses the same indentation/keyword approach as
// PyFindClassHierarchy: from the first newline after the
// `class`/`module` line to the matching `end` keyword (depth-
// tracked, comment-aware).
func RbFindClassHierarchy(src []byte) []RbClassHierarchy {
	s := string(src)
	var out []RbClassHierarchy
	i := 0
	for i < len(s) {
		// Comment to end of line.
		if s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		// String literal — skip contents.
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
		isModule := false
		if i+5 <= len(s) && s[i:i+5] == "class" {
			kw = "class"
		} else if i+6 <= len(s) && s[i:i+6] == "module" {
			kw = "module"
			isModule = true
		}
		if kw == "" {
			i++
			continue
		}
		if i > 0 && isRbIdentByte(s[i-1]) {
			i++
			continue
		}
		after := i + len(kw)
		if after < len(s) && isRbIdentByte(s[after]) {
			i++
			continue
		}
		j := skipRbSpace(s, after)
		nameStart := j
		for j < len(s) && (isRbIdentByte(s[j]) || s[j] == ':') {
			j++
		}
		if j == nameStart {
			i = j + 1
			continue
		}
		name := s[nameStart:j]
		// Strip any leading `::` or qualified path.
		if idx := strings.LastIndex(name, "::"); idx >= 0 {
			name = name[idx+2:]
		}
		var supers []string
		j = skipRbSpace(s, j)
		if j < len(s) && s[j] == '<' && !isModule {
			j++
			j = skipRbSpace(s, j)
			start := j
			for j < len(s) && (isRbIdentByte(s[j]) || s[j] == ':') {
				j++
			}
			if j > start {
				sup := s[start:j]
				if idx := strings.LastIndex(sup, "::"); idx >= 0 {
					sup = sup[idx+2:]
				}
				supers = append(supers, sup)
			}
		}
		// Walk to end of decl line.
		for j < len(s) && s[j] != '\n' {
			j++
		}
		if j < len(s) {
			j++
		}
		bodyStart := j
		// Walk to matching `end`, tracking nested class/module/def/
		// if/while/begin/case keywords that open additional blocks.
		depth := 1
		for j < len(s) && depth > 0 {
			if s[j] == '#' {
				for j < len(s) && s[j] != '\n' {
					j++
				}
				continue
			}
			if s[j] == '"' || s[j] == '\'' {
				q := s[j]
				j++
				for j < len(s) && s[j] != q {
					if s[j] == '\\' && j+1 < len(s) {
						j += 2
						continue
					}
					j++
				}
				if j < len(s) {
					j++
				}
				continue
			}
			// Recognize block-opening keywords (newline/space-bounded).
			if isRbBlockOpen(s, j) {
				depth++
				j = skipRbBlockKw(s, j)
				continue
			}
			// Mixin: `include X` / `extend X` / `prepend X`.
			if depth == 1 {
				if mixed, after := readRbMixin(s, j); mixed != "" {
					supers = append(supers, mixed)
					j = after
					continue
				}
			}
			// `end` keyword.
			if j+3 <= len(s) && s[j:j+3] == "end" &&
				(j == 0 || !isRbIdentByte(s[j-1])) &&
				(j+3 == len(s) || !isRbIdentByte(s[j+3])) {
				depth--
				j += 3
				continue
			}
			j++
		}
		out = append(out, RbClassHierarchy{
			Name:      name,
			IsModule:  isModule,
			Supers:    supers,
			BodyStart: uint32(bodyStart),
			BodyEnd:   uint32(j),
		})
		i = j
	}
	return out
}

// readRbMixin recognizes `include X`, `extend X`, `prepend X` at
// position j (must be at a word boundary). Returns the mixin name
// and the position after it, or "" if no match.
func readRbMixin(s string, j int) (string, int) {
	if j > 0 && isRbIdentByte(s[j-1]) {
		return "", j
	}
	for _, kw := range []string{"include", "extend", "prepend"} {
		if j+len(kw) > len(s) {
			continue
		}
		if s[j:j+len(kw)] != kw {
			continue
		}
		end := j + len(kw)
		if end < len(s) && isRbIdentByte(s[end]) {
			continue
		}
		k := skipRbSpace(s, end)
		nameStart := k
		for k < len(s) && (isRbIdentByte(s[k]) || s[k] == ':') {
			k++
		}
		if k > nameStart {
			name := s[nameStart:k]
			if idx := strings.LastIndex(name, "::"); idx >= 0 {
				name = name[idx+2:]
			}
			return name, k
		}
	}
	return "", j
}

// isRbBlockOpen reports whether the keyword at s[j] opens a
// block that needs a matching `end`.
func isRbBlockOpen(s string, j int) bool {
	if j > 0 && isRbIdentByte(s[j-1]) {
		return false
	}
	for _, kw := range []string{"def", "begin", "do", "case"} {
		if j+len(kw) <= len(s) && s[j:j+len(kw)] == kw {
			end := j + len(kw)
			if end == len(s) || !isRbIdentByte(s[end]) {
				return true
			}
		}
	}
	// `if` / `while` / `until` are tricky because they can be
	// statement modifiers (`puts x if cond`). Conservatively count
	// them as block-openers only at line start.
	if j > 0 && s[j-1] != '\n' && s[j-1] != ' ' && s[j-1] != '\t' {
		return false
	}
	for _, kw := range []string{"if", "unless", "while", "until"} {
		if j+len(kw) <= len(s) && s[j:j+len(kw)] == kw {
			end := j + len(kw)
			if end == len(s) || !isRbIdentByte(s[end]) {
				return true
			}
		}
	}
	return false
}

func skipRbBlockKw(s string, j int) int {
	for _, kw := range []string{"def", "begin", "do", "case", "if", "unless", "while", "until"} {
		if j+len(kw) <= len(s) && s[j:j+len(kw)] == kw {
			return j + len(kw)
		}
	}
	return j + 1
}

func skipRbSpace(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	return i
}

func isRbIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}

// RbRelatedTypes returns the transitive set of class/module
// names related to className via inheritance (super) and mixins
// (include/extend/prepend) within src.
func RbRelatedTypes(src []byte, className string) []string {
	hier := RbFindClassHierarchy(src)
	byName := map[string]RbClassHierarchy{}
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
