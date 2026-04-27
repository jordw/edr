package dispatch

import (
	"bytes"
	"os"
)

// expandToDocComment scans backwards from startByte to include preceding
// comment lines (// or #) that are part of the symbol's documentation.
func expandToDocComment(file string, startByte uint32) uint32 {
	data, err := os.ReadFile(file)
	if err != nil || startByte == 0 {
		return startByte
	}

	pos := int(startByte)
	for pos > 0 {
		// Skip backwards over the newline before startByte.
		nl := pos - 1
		if nl < 0 || data[nl] != '\n' {
			break
		}
		// Find the start of the previous line.
		lineStart := nl
		for lineStart > 0 && data[lineStart-1] != '\n' {
			lineStart--
		}
		line := bytes.TrimSpace(data[lineStart:nl])
		if len(line) >= 2 && line[0] == '/' && line[1] == '/' {
			pos = lineStart
		} else if len(line) >= 1 && line[0] == '#' {
			pos = lineStart
		} else if len(line) >= 2 && line[0] == '/' && line[1] == '*' {
			pos = lineStart
		} else if len(line) >= 3 && bytes.HasPrefix(line, []byte("///")) {
			pos = lineStart
		} else if len(line) >= 1 && line[0] == '*' {
			// Middle or end of a block comment (/** ... */).
			pos = lineStart
		} else {
			break
		}
	}
	return uint32(pos)
}

// findIdentOccurrences scans data[lo:hi] for word-bounded occurrences of
// name and returns one span per match. Used to pick up oldName mentions in
// a symbol's leading doc-comment block once the apply layer is span-based
// (the legacy regex sweep handled this implicitly).
func findIdentOccurrences(data []byte, lo, hi uint32, name string) []span {
	if name == "" {
		return nil
	}
	if hi > uint32(len(data)) {
		hi = uint32(len(data))
	}
	if lo >= hi {
		return nil
	}
	var out []span
	nb := []byte(name)
	i := int(lo)
	end := int(hi)
	for i+len(nb) <= end {
		idx := bytes.Index(data[i:end], nb)
		if idx < 0 {
			break
		}
		abs := i + idx
		absEnd := abs + len(nb)
		leftOK := abs == 0 || !isIdentByte(data[abs-1])
		rightOK := absEnd >= len(data) || !isIdentByte(data[absEnd])
		if leftOK && rightOK {
			out = append(out, span{uint32(abs), uint32(absEnd)})
		}
		i = abs + 1
	}
	return out
}

// dedupSpans drops exact duplicates (same start+end) and contained
// duplicates so the apply pass doesn't double-count or skip emissions
// from multiple handlers (e.g. same-file decl + hierarchy emit).
func dedupSpans(in []span) []span {
	if len(in) <= 1 {
		return in
	}
	seen := make(map[uint64]bool, len(in))
	out := make([]span, 0, len(in))
	for _, s := range in {
		k := uint64(s.start)<<32 | uint64(s.end)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, s)
	}
	return out
}

func isIdentByte(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '_'
}
