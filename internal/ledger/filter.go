package ledger

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ParsePredicate parses a single --filter argument like "file=tools/**",
// "container=Foo/bar", or "container~=handle" into a Predicate.
func ParsePredicate(arg string) (Predicate, error) {
	// Check for suffix operator first (longer match).
	if i := strings.Index(arg, "~="); i > 0 {
		field := strings.TrimSpace(arg[:i])
		value := arg[i+2:]
		if err := checkField(field); err != nil {
			return Predicate{}, err
		}
		return Predicate{Field: field, Op: "suffix", Value: value}, nil
	}
	if i := strings.Index(arg, "="); i > 0 {
		field := strings.TrimSpace(arg[:i])
		value := arg[i+1:]
		if err := checkField(field); err != nil {
			return Predicate{}, err
		}
		op := "eq"
		if field == "file" {
			op = "glob"
		}
		return Predicate{Field: field, Op: op, Value: value}, nil
	}
	return Predicate{}, fmt.Errorf("filter: expected field=value or field~=value, got %q", arg)
}

func checkField(field string) error {
	switch field {
	case "file", "container":
		return nil
	}
	return fmt.Errorf("filter: unknown field %q (expected file or container)", field)
}

// Matches reports whether a site satisfies this predicate.
//
// For "eq": exact string match against the field; commas in Value are
// OR-combined (any match satisfies).
// For "suffix": case-sensitive suffix match; commas OR'd.
// For "glob": doublestar-style path match against the file field; commas OR'd.
func (p Predicate) Matches(s *Site) bool {
	values := strings.Split(p.Value, ",")
	target := ""
	switch p.Field {
	case "file":
		target = s.File
	case "container":
		target = formatContainer(s.Container)
	default:
		return false
	}
	for _, v := range values {
		if matchOp(p.Op, target, v) {
			return true
		}
	}
	return false
}

func matchOp(op, target, value string) bool {
	switch op {
	case "eq":
		return target == value
	case "suffix":
		return strings.HasSuffix(target, value)
	case "glob":
		// Use filepath.Match for simple patterns; fall back to segment-prefix
		// for ** patterns which Match doesn't support.
		if strings.Contains(value, "**") {
			return globDoubleStar(value, target)
		}
		ok, _ := filepath.Match(value, target)
		return ok
	}
	return false
}

// globDoubleStar implements a minimal **-aware glob: "a/**" matches any path
// starting with "a/"; "a/**/b" matches any path with "a/" prefix and "b" at
// a segment boundary. Good enough for --filter's primary use.
func globDoubleStar(pattern, path string) bool {
	parts := strings.Split(pattern, "**")
	if len(parts) == 1 {
		ok, _ := filepath.Match(pattern, path)
		return ok
	}
	pos := 0
	for i, part := range parts {
		if part == "" {
			continue
		}
		if i == 0 {
			if !strings.HasPrefix(path[pos:], part) {
				return false
			}
			pos += len(part)
			continue
		}
		idx := strings.Index(path[pos:], part)
		if idx < 0 {
			return false
		}
		pos += idx + len(part)
	}
	// Last segment: if pattern ended with **, any suffix is fine; else must
	// reach end of path.
	if strings.HasSuffix(pattern, "**") {
		return true
	}
	return pos == len(path)
}

// formatContainer renders a ContainerStep path as "Kind:Name/Kind:Name" for
// predicate matching and display. For steps with empty Kind or Name, the
// separator is omitted.
func formatContainer(steps []ContainerStep) string {
	if len(steps) == 0 {
		return ""
	}
	parts := make([]string, 0, len(steps))
	for _, s := range steps {
		switch {
		case s.Kind == "" && s.Name == "":
			continue
		case s.Kind == "":
			parts = append(parts, s.Name)
		case s.Name == "":
			parts = append(parts, s.Kind+":")
		default:
			parts = append(parts, s.Kind+":"+s.Name)
		}
	}
	return strings.Join(parts, "/")
}

// Apply filters a ledger's Sites by the Filter's predicates (AND'd).
// It mutates l.Sites, l.Counts, l.ExcludedCounts, l.Total and returns the
// number of sites removed.
func (l *Ledger) ApplyFilter() int {
	if l.Filter == nil || len(l.Filter.Predicates) == 0 {
		return 0
	}
	pre := len(l.Sites)
	l.Filter.PreFilterTotal = pre
	kept := l.Sites[:0]
	excluded := make(map[Tier]int)
	for i := range l.Sites {
		s := &l.Sites[i]
		match := true
		for _, p := range l.Filter.Predicates {
			if !p.Matches(s) {
				match = false
				break
			}
		}
		if match {
			kept = append(kept, *s)
		} else {
			excluded[s.Tier]++
		}
	}
	l.Sites = kept
	l.ExcludedCounts = excluded
	l.RecomputeCounts()
	return pre - len(kept)
}

// RecomputeCounts regenerates Counts and Total from Sites. Called after any
// mutation (filter, apply) to keep invariants.
func (l *Ledger) RecomputeCounts() {
	counts := make(map[Tier]int)
	for _, s := range l.Sites {
		counts[s.Tier]++
	}
	l.Counts = counts
	l.Total = len(l.Sites)
}
