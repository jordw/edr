package dispatch

import (
	"context"
	"os"
	"strings"

	"github.com/jordw/edr/internal/index"
)

// attachReadBack reads context around the edit point and attaches it to the result.
// If the edit targeted a symbol, reads the full updated symbol body.
// Otherwise, reads ~10 lines above/below the diff location.
func attachReadBack(ctx context.Context, db index.SymbolStore, result any) (any, error) {
	m, ok := result.(map[string]any)
	if !ok {
		return result, nil
	}
	status, _ := m["status"].(string)
	if status != "applied" {
		return result, nil
	}

	relFile, _ := m["file"].(string)
	if relFile == "" {
		return result, nil
	}
	file, err := db.ResolvePath(relFile)
	if err != nil {
		return result, nil // best-effort
	}

	// Read the post-edit file and attach context around the edit.
	data, err := os.ReadFile(file)
	if err != nil {
		return result, nil
	}

	// If we know which symbol was edited, return its full updated body.
	if symName, ok := m["symbol"].(string); ok && symName != "" {
		db.InvalidateFiles(ctx, []string{file})
		sym, sErr := db.GetSymbol(ctx, file, symName)
		if sErr == nil && int(sym.EndByte) <= len(data) {
			m["read_back"] = map[string]any{
				"symbol":  symName,
				"lines":   [2]int{int(sym.StartLine), int(sym.EndLine)},
				"content": string(data[sym.StartByte:sym.EndByte]),
			}
			return result, nil
		}
	}

	// Otherwise, find the symbol containing the edit location.
	// Parse the post-edit file directly (fast, no cache issues).
	diff, _ := m["diff"].(string)
	editLine := diffStartLine(diff)
	if editLine > 0 {
		syms := index.Parse(file, data)
		var best *index.SymbolInfo
		for i := range syms {
			s := &syms[i]
			if int(s.StartLine) <= editLine && editLine <= int(s.EndLine) {
				if best == nil || s.StartLine >= best.StartLine {
					best = s
				}
			}
		}
		if best != nil && int(best.EndByte) <= len(data) {
			m["read_back"] = map[string]any{
				"symbol":  best.Name,
				"lines":   [2]int{int(best.StartLine), int(best.EndLine)},
				"content": string(data[best.StartByte:best.EndByte]),
			}
			return result, nil
		}
	}

	// Fallback: return lines around the edit point
	if editLine > 0 {
		lines := strings.SplitAfter(string(data), "\n")
		start := editLine - 5
		if start < 1 {
			start = 1
		}
		end := editLine + 5
		if end > len(lines) {
			end = len(lines)
		}
		m["read_back"] = map[string]any{
			"lines":   [2]int{start, end},
			"content": strings.Join(lines[start-1:end], ""),
		}
	}
	return result, nil
}

// diffStartLine extracts the new-file start line from a unified diff header.
// Parses the @@ -a,b +c,d @@ line and returns c.
func diffStartLine(diff string) int {
	hunkStart := 0
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "@@") {
			idx := strings.Index(line, "+")
			if idx < 0 {
				continue
			}
			rest := line[idx+1:]
			n := 0
			for _, ch := range rest {
				if ch >= '0' && ch <= '9' {
					n = n*10 + int(ch-'0')
				} else {
					break
				}
			}
			hunkStart = n
			continue
		}
		// Find first actual change line (+ or -)
		if hunkStart > 0 && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) {
			return hunkStart
		}
		if hunkStart > 0 && (strings.HasPrefix(line, " ") || line == "") {
			hunkStart++
		}
	}
	return 0
}
