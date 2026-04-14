// Outline produces depth-limited views of source files and symbols.
// Uses regex-based symbol boundaries and text-based block collapsing.
package index

import (
	"fmt"
	"os"
	"strings"
)

// OutlineFile produces a depth-limited view of a source file.
// depth=1: signatures only. depth=2+: skeleton with blocks collapsed.
func OutlineFile(path string, depth int) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return OutlineFileFromSource(path, src, depth)
}

// OutlineFileFromSource is like OutlineFile but takes pre-loaded source bytes.
func OutlineFileFromSource(path string, src []byte, depth int) (string, error) {
	if !Supported(path) {
		return "", fmt.Errorf("unsupported language for %s", path)
	}

	syms := Parse(path, src)

	if depth <= 1 {
		// Signatures: one line per symbol
		var lines []string
		for _, sym := range syms {
			sig := ExtractSignatureFromSource(sym, src)
			if sig != "" {
				lines = append(lines, sig)
			}
		}
		return strings.Join(lines, "\n"), nil
	}

	// Skeleton: full file with symbol bodies collapsed
	return collapseFile(src, syms, depth), nil
}

// OutlineSymbol produces a depth-limited view of a specific symbol.
func OutlineSymbol(path string, sym SymbolInfo, depth int) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return OutlineSymbolFromSource(path, sym, src, depth)
}

// OutlineSymbolFromSource is like OutlineSymbol but takes pre-loaded source bytes.
func OutlineSymbolFromSource(path string, sym SymbolInfo, src []byte, depth int) (string, error) {
	if depth <= 1 {
		return ExtractSignatureFromSource(sym, src), nil
	}

	// Return the symbol body with inner blocks collapsed
	if int(sym.EndByte) > len(src) || sym.StartByte > sym.EndByte {
		return string(src[sym.StartByte:]), nil
	}
	body := src[sym.StartByte:sym.EndByte]

	// For depth=2, collapse control-flow blocks inside the body
	return collapseBlocks(string(body)), nil
}

// collapseFile produces a skeleton view: symbol bodies replaced with "..."
// while keeping signatures and structure visible.
func collapseFile(src []byte, syms []SymbolInfo, depth int) string {
	lines := strings.Split(string(src), "\n")
	if len(syms) == 0 {
		return string(src)
	}

	// Build a set of line ranges to collapse (symbol bodies, not signatures).
	// For each symbol, keep the first line (signature) and collapse the rest.
	var collapses []collapse
	for _, sym := range syms {
		if sym.EndLine <= sym.StartLine+1 {
			continue // single-line symbol, nothing to collapse
		}
		// Keep the signature line(s), collapse the body
		bodyStart := int(sym.StartLine) + 1 // line after signature
		bodyEnd := int(sym.EndLine) - 1     // line before closing brace/end
		if bodyStart <= bodyEnd {
			collapses = append(collapses, collapse{bodyStart, bodyEnd})
		}
	}

	if len(collapses) == 0 {
		return string(src)
	}

	// Merge overlapping/nested collapses — keep outermost
	merged := mergeCollapses(collapses)

	// Build output
	var out []string
	collapsed := make(map[int]bool) // lines to skip (1-based)
	for _, c := range merged {
		for i := c.start; i <= c.end; i++ {
			collapsed[i] = true
		}
	}

	// Preserve signature and closing lines of nested symbols (e.g. methods
	// inside a class). Without this, a container collapse swallows all nested
	// symbol headers, reducing the skeleton to just the outer wrapper.
	for _, sym := range syms {
		delete(collapsed, int(sym.StartLine))
		delete(collapsed, int(sym.EndLine))
	}

	// Track which collapses we've emitted "..." for
	emitted := make(map[int]bool)
	for i, line := range lines {
		lineNum := i + 1
		if collapsed[lineNum] {
			// Find which collapse this belongs to
			for _, c := range merged {
				if lineNum == c.start && !emitted[c.start] {
					// Emit "..." with proper indentation
					indent := extractLeadingWS(lines[c.start-2]) // indent of signature line
					out = append(out, indent+"\t...")
					emitted[c.start] = true
					break
				}
			}
			continue
		}
		out = append(out, line)
	}

	return strings.Join(out, "\n")
}

// collapseBlocks collapses control-flow blocks inside a symbol body.
func collapseBlocks(body string) string {
	lines := strings.Split(body, "\n")
	var out []string

	cfKeywords := []string{"if ", "if(", "for ", "for(", "while ", "while(", "switch ", "switch(",
		"try ", "try{", "catch ", "catch(", "else ", "else{", "select ", "select{",
		"match ", "match{"}

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Check if this line starts a control-flow block
		isCF := false
		for _, kw := range cfKeywords {
			if strings.HasPrefix(trimmed, kw) {
				isCF = true
				break
			}
		}

		if isCF && strings.Contains(line, "{") {
			// Emit the header line, then skip the body
			out = append(out, line)
			depth := 0
			for j := i; j < len(lines); j++ {
				for _, ch := range lines[j] {
					if ch == '{' {
						depth++
					} else if ch == '}' {
						depth--
					}
				}
				if depth <= 0 && j > i {
					indent := extractLeadingWS(line)
					out = append(out, indent+"\t...")
					out = append(out, lines[j]) // closing }
					i = j + 1
					break
				}
			}
			if depth > 0 {
				// Never closed — just output remaining lines
				out = append(out, lines[i:]...)
				break
			}
		} else {
			out = append(out, line)
			i++
		}
	}

	return strings.Join(out, "\n")
}

// mergeCollapses merges overlapping collapse ranges, keeping outermost.
func mergeCollapses(collapses []collapse) []collapse {
	if len(collapses) == 0 {
		return nil
	}
	// Since symbols are already ordered by start line, just merge overlaps
	merged := []collapse{collapses[0]}
	for _, c := range collapses[1:] {
		last := &merged[len(merged)-1]
		if c.start <= last.end+1 {
			if c.end > last.end {
				last.end = c.end
			}
		} else {
			merged = append(merged, c)
		}
	}
	return merged
}

type collapse struct{ start, end int }

func extractLeadingWS(line string) string {
	for i, ch := range line {
		if ch != ' ' && ch != '\t' {
			return line[:i]
		}
	}
	return line
}
