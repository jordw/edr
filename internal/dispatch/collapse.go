package dispatch

import (
	"fmt"
	"path/filepath"
	"strings"
)

// collapseBoilerplate collapses license headers and import blocks in source
// code to reduce context. Returns the collapsed content.
// Only called for full-file reads without --full.
func collapseBoilerplate(content, filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if !collapsibleExt[ext] {
		return content
	}

	lines := strings.SplitAfter(content, "\n")
	if len(lines) < 10 {
		return content // too short to bother
	}

	var out []string
	i := 0
	n := len(lines)

	// Phase 1: collapse license/copyright comment block at file start.
	// Skip blank lines, then detect a contiguous comment block.
	for i < n && strings.TrimSpace(lines[i]) == "" {
		out = append(out, lines[i])
		i++
	}

	licStart := i
	licEnd := detectCommentBlock(lines, i, ext)
	if licEnd > licStart && licEnd-licStart >= 3 && looksLikeLicense(lines[licStart:licEnd]) {
		out = append(out, fmt.Sprintf("// [lines %d-%d: license/copyright comment]\n", licStart+1, licEnd))
		i = licEnd
	}

	// Phase 2: find and collapse import block.
	for i < n {
		impStart, impEnd := detectImportBlock(lines, i, ext)
		if impEnd > impStart && impEnd-impStart >= 4 {
			// Emit lines before the import block
			out = append(out, lines[i:impStart]...)
			count := countImports(lines[impStart:impEnd], ext)
			out = append(out, fmt.Sprintf("// [lines %d-%d: %d imports]\n", impStart+1, impEnd, count))
			i = impEnd
			break
		}
		// No import block found from this position; emit one line and advance
		out = append(out, lines[i])
		i++
		// Stop scanning after we're past the typical header area
		if i > 50 {
			break
		}
	}

	// Emit remaining lines
	if i < n {
		out = append(out, lines[i:]...)
	}

	return strings.Join(out, "")
}

var collapsibleExt = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true,
	".ts": true, ".tsx": true, ".java": true, ".rs": true,
	".c": true, ".h": true, ".cpp": true, ".cc": true,
	".cs": true, ".kt": true, ".rb": true, ".php": true,
	".zig": true, ".lua": true,
}

// detectCommentBlock finds the end of a contiguous comment block starting at line i.
func detectCommentBlock(lines []string, start int, ext string) int {
	i := start
	n := len(lines)

	// Block comment: /* ... */
	trimmed := strings.TrimSpace(lines[i])
	if strings.HasPrefix(trimmed, "/*") {
		for i < n {
			if strings.Contains(lines[i], "*/") {
				return i + 1
			}
			i++
		}
		return start // unclosed, bail
	}

	// Line comments: // or #
	commentPrefix := "//"
	if ext == ".py" || ext == ".rb" {
		commentPrefix = "#"
	}

	for i < n {
		t := strings.TrimSpace(lines[i])
		if t == "" {
			// Allow one blank line inside comment block
			if i+1 < n && strings.HasPrefix(strings.TrimSpace(lines[i+1]), commentPrefix) {
				i++
				continue
			}
			break
		}
		if !strings.HasPrefix(t, commentPrefix) {
			break
		}
		i++
	}
	return i
}

// looksLikeLicense returns true if the comment block likely contains license text.
func looksLikeLicense(lines []string) bool {
	text := strings.ToLower(strings.Join(lines, " "))
	markers := []string{"license", "copyright", "permission", "warranty", "redistribution", "apache", "mit ", "bsd", "gpl", "mozilla"}
	for _, m := range markers {
		if strings.Contains(text, m) {
			return true
		}
	}
	return false
}

// detectImportBlock finds an import block starting at or after line i.
// Returns (start, end) indices. If no block found, returns (i, i).
func detectImportBlock(lines []string, start int, ext string) (int, int) {
	n := len(lines)

	switch ext {
	case ".go":
		// Go: import ( ... ) or single import "..."
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if t == "import (" {
				end := i + 1
				for end < n {
					if strings.TrimSpace(lines[end]) == ")" {
						return i, end + 1
					}
					end++
				}
			}
		}

	case ".py":
		// Python: consecutive import/from lines
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "from ") {
				end := i + 1
				for end < n {
					te := strings.TrimSpace(lines[end])
					if te == "" {
						end++
						continue
					}
					if !strings.HasPrefix(te, "import ") && !strings.HasPrefix(te, "from ") {
						break
					}
					end++
				}
				if end-i >= 4 {
					return i, end
				}
			}
		}

	case ".js", ".jsx", ".ts", ".tsx":
		// JS/TS: consecutive import/require lines
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "import ") || strings.HasPrefix(t, "const ") && strings.Contains(t, "require(") {
				end := i + 1
				for end < n {
					te := strings.TrimSpace(lines[end])
					if te == "" {
						end++
						continue
					}
					if !strings.HasPrefix(te, "import ") &&
						!(strings.HasPrefix(te, "const ") && strings.Contains(te, "require(")) {
						break
					}
					end++
				}
				if end-i >= 4 {
					return i, end
				}
			}
		}

	case ".java", ".kt":
		// Java/Kotlin: consecutive import lines
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "import ") {
				end := i + 1
				for end < n {
					te := strings.TrimSpace(lines[end])
					if te == "" {
						end++
						continue
					}
					if !strings.HasPrefix(te, "import ") {
						break
					}
					end++
				}
				if end-i >= 4 {
					return i, end
				}
			}
		}

	case ".rs":
		// Rust: consecutive use lines
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "use ") {
				end := i + 1
				for end < n {
					te := strings.TrimSpace(lines[end])
					if te == "" {
						end++
						continue
					}
					if !strings.HasPrefix(te, "use ") {
						break
					}
					end++
				}
				if end-i >= 4 {
					return i, end
				}
			}
		}

	case ".c", ".h", ".cpp", ".cc":
		// C/C++: consecutive #include lines
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "#include") {
				end := i + 1
				for end < n {
					te := strings.TrimSpace(lines[end])
					if te == "" {
						end++
						continue
					}
					if !strings.HasPrefix(te, "#include") {
						break
					}
					end++
				}
				if end-i >= 4 {
					return i, end
				}
			}
		}

	case ".cs":
		// C#: consecutive using lines
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "using ") {
				end := i + 1
				for end < n {
					te := strings.TrimSpace(lines[end])
					if te == "" {
						end++
						continue
					}
					if !strings.HasPrefix(te, "using ") {
						break
					}
					end++
				}
				if end-i >= 4 {
					return i, end
				}
			}
		}

	case ".php":
		// PHP: consecutive use lines (after namespace)
		for i := start; i < n && i < start+30; i++ {
			t := strings.TrimSpace(lines[i])
			if strings.HasPrefix(t, "use ") {
				end := i + 1
				for end < n {
					te := strings.TrimSpace(lines[end])
					if te == "" {
						end++
						continue
					}
					if !strings.HasPrefix(te, "use ") {
						break
					}
					end++
				}
				if end-i >= 4 {
					return i, end
				}
			}
		}
	}

	return start, start
}

// countImports counts the actual import lines (not blank lines) in a block.
func countImports(lines []string, ext string) int {
	count := 0
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || t == "import (" || t == ")" {
			continue
		}
		count++
	}
	return count
}
