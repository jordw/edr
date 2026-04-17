package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

func runExtract(ctx context.Context, db index.SymbolStore, root string, args []string, flags map[string]any) (any, error) {
	newName := flagString(flags, "name", "")
	if newName == "" {
		return nil, fmt.Errorf("extract: --name <function_name> is required")
	}
	dryRun := flagBool(flags, "dry_run", false)
	callExpr := flagString(flags, "call", "")

	// Resolve the source symbol.
	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}

	data, err := os.ReadFile(sym.File)
	if err != nil {
		return nil, fmt.Errorf("extract: read %s: %w", output.Rel(sym.File), err)
	}

	// Determine the line range to extract.
	linesSpec := flagString(flags, "lines", "")
	if linesSpec == "" {
		return nil, fmt.Errorf("extract: --lines <start-end> is required")
	}
	startLine, endLine, err := parseColonRange(linesSpec)
	if err != nil {
		return nil, fmt.Errorf("extract: --lines: %w", err)
	}

	// Convert to 0-indexed line numbers.
	lines := bytes.Split(data, []byte("\n"))
	if startLine < 1 || endLine < startLine || endLine > len(lines) {
		return nil, fmt.Errorf("extract: line range %d-%d out of bounds (file has %d lines)", startLine, endLine, len(lines))
	}

	// Validate the range is within the source symbol.
	symStartLine := int(sym.StartLine)
	symEndLine := int(sym.EndLine)
	if startLine < symStartLine || endLine > symEndLine {
		return nil, fmt.Errorf("extract: line range %d-%d is outside symbol %s (%d-%d)",
			startLine, endLine, sym.Name, symStartLine, symEndLine)
	}

	// Extract the selected lines.
	// lines is 0-indexed, startLine/endLine are 1-indexed.
	extracted := lines[startLine-1 : endLine]

	// Static check: do any identifiers in the extracted block refer to
	// locals declared in the surrounding function but not threaded into the
	// new function via --call? If so, the extracted function will not
	// compile. Force the user to declare them explicitly.
	ext := strings.ToLower(filepath.Ext(sym.File))
	if missing := findExternalLocals(ext, lines, startLine, endLine, sym, data, flagString(flags, "call", "")); len(missing) > 0 {
		return nil, fmt.Errorf("extract: extracted block references %d local(s) from %s not threaded through --call: %s. "+
			"Re-run with --call %q to pass them as arguments",
			len(missing), sym.Name, strings.Join(missing, ", "),
			suggestedCall(flagString(flags, "name", ""), missing))
	}

	// Detect the indentation of the extracted block to de-indent for the new function.
	indent := detectMinIndent(extracted)
	var deindented []string
	for _, line := range extracted {
		s := string(line)
		if len(s) >= len(indent) && strings.HasPrefix(s, indent) {
			deindented = append(deindented, "\t"+s[len(indent):])
		} else if strings.TrimSpace(s) == "" {
			deindented = append(deindented, "")
		} else {
			deindented = append(deindented, "\t"+s)
		}
	}

	// Parse parameters from the --call expression if provided.
	// E.g. --call "helper(rq, flags)" => params = "rq, flags"
	var params string
	if callExpr != "" {
		if lp := strings.Index(callExpr, "("); lp >= 0 {
			if rp := strings.LastIndex(callExpr, ")"); rp > lp {
				params = strings.TrimSpace(callExpr[lp+1 : rp])
			}
		}
	}

	newFunc := buildExtractedFunction(ext, newName, params, deindented)

	// Build the call expression that replaces the extracted lines.
	if callExpr == "" {
		callExpr = newName + "()"
	}
	// Add statement terminator for languages that need it.
	if needsSemicolon(ext) && !strings.HasSuffix(callExpr, ";") {
		callExpr += ";"
	}
	// Indent the call to match the original indentation.
	callLine := indent + callExpr

	// Build new file content:
	// 1. Replace the extracted lines with the call
	// 2. Insert the new function after the source symbol
	var result []byte
	for i, line := range lines {
		lineNum := i + 1 // 1-indexed
		if lineNum == startLine {
			result = append(result, []byte(callLine)...)
			result = append(result, '\n')
		} else if lineNum > startLine && lineNum <= endLine {
			// Skip extracted lines.
			continue
		} else {
			result = append(result, line...)
			// Insert the new function after the symbol's closing line.
			if lineNum == symEndLine {
				result = append(result, '\n')
				result = append(result, '\n')
				result = append(result, []byte(newFunc)...)
			}
			if i < len(lines)-1 {
				result = append(result, '\n')
			}
		}
	}

	diff := edit.UnifiedDiff(output.Rel(sym.File), data, result)

	if dryRun {
		return map[string]any{
			"file":    output.Rel(sym.File),
			"status":  "dry_run",
			"diff":    diff,
			"message": fmt.Sprintf("extract %d lines from %s into %s", endLine-startLine+1, sym.Name, newName),
		}, nil
	}

	// Apply.
	hash := edit.HashBytes(data)
	tx := edit.NewTransaction()
	tx.Add(sym.File, 0, uint32(len(data)), string(result), hash)
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("extract: %w", err)
	}
	idx.MarkDirty(db.EdrDir(), output.Rel(sym.File))
	newHash, _ := edit.FileHash(sym.File)

	return map[string]any{
		"file":    output.Rel(sym.File),
		"status":  "applied",
		"diff":    diff,
		"hash":    newHash,
		"message": fmt.Sprintf("extract %d lines from %s into %s", endLine-startLine+1, sym.Name, newName),
	}, nil
}

// detectMinIndent finds the minimum leading whitespace across non-empty lines.
func detectMinIndent(lines [][]byte) string {
	min := ""
	first := true
	for _, line := range lines {
		s := string(line)
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		leading := s[:len(s)-len(strings.TrimLeft(s, " \t"))]
		if first || len(leading) < len(min) {
			min = leading
			first = false
		}
	}
	return min
}

// buildExtractedFunction wraps the deindented lines in a function declaration
// appropriate for the file's language.
func buildExtractedFunction(ext, name, params string, body []string) string {
	joined := strings.Join(body, "\n")

	switch ext {
	case ".py", ".pyi":
		// Python: def name(params):\n    body
		return fmt.Sprintf("def %s(%s):\n%s\n", name, params, joined)

	case ".rb":
		// Ruby: def name(params)\n  body\nend
		if params != "" {
			return fmt.Sprintf("def %s(%s)\n%s\nend\n", name, params, joined)
		}
		return fmt.Sprintf("def %s\n%s\nend\n", name, joined)

	case ".rs":
		// Rust: fn name(params) {\n    body\n}
		return fmt.Sprintf("fn %s(%s) {\n%s\n}\n", name, params, joined)

	case ".java", ".kt", ".kts", ".scala", ".sc":
		// Java/Kotlin/Scala: void name(params) {\n    body\n}
		return fmt.Sprintf("void %s(%s) {\n%s\n}\n", name, params, joined)

	case ".js", ".jsx", ".ts", ".tsx", ".mts", ".cts":
		// JavaScript/TypeScript: function name(params) {\n    body\n}
		return fmt.Sprintf("function %s(%s) {\n%s\n}\n", name, params, joined)

	case ".c", ".h", ".cpp", ".cc", ".hpp", ".cxx", ".hxx", ".hh":
		// C/C++: void name(params) {\n    body\n}
		return fmt.Sprintf("void %s(%s) {\n%s\n}\n", name, params, joined)

	default:
		// Go and fallback: func name(params) {\n    body\n}
		return fmt.Sprintf("func %s(%s) {\n%s\n}\n", name, params, joined)
	}
}

// findExternalLocals returns the names of identifiers used in the extracted
// block that are declared as locals in the surrounding function but not
// passed via --call. Returning these as an error prevents the user from
// silently producing an extracted function that references undefined symbols.
//
// The detection is heuristic and language-specific. For unsupported languages
// it returns nil, accepting false negatives over false positives — the user
// can still get a clean dry-run that they can read before applying.
func findExternalLocals(ext string, fileLines [][]byte, startLine, endLine int, sym *index.SymbolInfo, fileData []byte, callExpr string) []string {
	declRe := localDeclRegex(ext)
	if declRe == nil {
		return nil
	}

	// Build the surrounding function body excluding the extracted lines.
	// We index fileLines (1-based startLine/endLine) and the symbol's own
	// line range so we don't accidentally pick up declarations from
	// unrelated functions in the same file.
	symStart := int(sym.StartLine)
	symEnd := int(sym.EndLine)
	if symStart < 1 {
		symStart = 1
	}
	if symEnd > len(fileLines) {
		symEnd = len(fileLines)
	}
	var outside bytes.Buffer
	for i := symStart - 1; i < symEnd; i++ {
		ln := i + 1
		if ln >= startLine && ln <= endLine {
			continue
		}
		outside.Write(fileLines[i])
		outside.WriteByte('\n')
	}

	// Locals declared in the surrounding body.
	declared := map[string]bool{}
	for _, m := range declRe.FindAllSubmatch(outside.Bytes(), -1) {
		if len(m) >= 2 && len(m[1]) > 0 {
			declared[string(m[1])] = true
		}
	}
	// Add the function's own parameters — they're available in the parent
	// scope but are not "locals to thread through" because they'd be passed
	// by the caller of the extracted function the same way they're passed
	// to the parent.
	if pos := bytes.IndexByte(fileData[sym.StartByte:sym.EndByte], '('); pos >= 0 {
		body := fileData[sym.StartByte:sym.EndByte]
		close := findMatchingClose(body, pos)
		if close > pos {
			for _, p := range splitParams(string(body[pos+1 : close])) {
				if name := lastIdentifier(p); name != "" {
					declared[name] = true
				}
			}
		}
	}

	// Identifiers used in the extracted block.
	var extractedSrc bytes.Buffer
	for i := startLine - 1; i < endLine; i++ {
		extractedSrc.Write(fileLines[i])
		extractedSrc.WriteByte('\n')
	}
	used := map[string]bool{}
	for _, m := range identifierRe.FindAll(extractedSrc.Bytes(), -1) {
		used[string(m)] = true
	}

	// Names already passed via --call.
	threaded := map[string]bool{}
	if callExpr != "" {
		if lp := strings.Index(callExpr, "("); lp >= 0 {
			if rp := strings.LastIndex(callExpr, ")"); rp > lp {
				for _, arg := range splitParams(callExpr[lp+1 : rp]) {
					threaded[strings.TrimSpace(arg)] = true
				}
			}
		}
	}

	var missing []string
	seen := map[string]bool{}
	for name := range used {
		if !declared[name] || threaded[name] || seen[name] {
			continue
		}
		// Function parameters were added to `declared` above, so they're
		// already filtered. We leave them in `declared` because the user
		// needs to be reminded if a param is referenced — they must thread
		// it. But the param name itself is in declared, not in
		// "missing-from-threaded": if --call already mentions it, it's
		// threaded. If not, it's missing. That matches the behavior we want.
		seen[name] = true
		missing = append(missing, name)
	}
	sort.Strings(missing)
	return missing
}

// localDeclRegex returns a language-specific regex whose first capture group
// is the identifier name of a local variable declaration. nil means we don't
// have a checker for the language.
func localDeclRegex(ext string) *regexp.Regexp {
	switch ext {
	case ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh", ".m", ".mm":
		// C-family: match `<type> <name>` and `<type> *<name>` at start of
		// a statement. Type may be a keyword, a `struct X`/`enum X`/`union
		// X` form, or an identifier ending in `_t`.
		return regexp.MustCompile(`(?m)^\s*(?:const\s+|static\s+|register\s+|volatile\s+|extern\s+|auto\s+)*(?:int|long|short|char|float|double|unsigned|signed|bool|void|struct\s+\w+|enum\s+\w+|union\s+\w+|u8|u16|u32|u64|s8|s16|s32|s64|size_t|ssize_t|loff_t|gfp_t|pid_t|uid_t|gid_t|atomic_t|atomic64_t|spinlock_t|\w+_t)\s+\**\s*(\w+)`)
	case ".rs":
		return regexp.MustCompile(`\blet\s+(?:mut\s+)?(\w+)`)
	case ".go":
		return regexp.MustCompile(`(?m)\b(\w+)\s*:=`)
	case ".js", ".jsx", ".ts", ".tsx", ".mts", ".cts":
		return regexp.MustCompile(`\b(?:const|let|var)\s+(\w+)`)
	}
	return nil
}

var identifierRe = regexp.MustCompile(`\b[A-Za-z_][A-Za-z_0-9]*\b`)

// lastIdentifier returns the last identifier-looking token in s. Used to peel
// the name off a parameter declaration like `int flags` → `flags` or
// `struct rq *rq` → `rq`.
func lastIdentifier(s string) string {
	m := identifierRe.FindAllString(s, -1)
	if len(m) == 0 {
		return ""
	}
	return m[len(m)-1]
}

// suggestedCall produces a hint like `do_thing(cpu, rq)` that the user can
// paste into --call to thread the missing locals.
func suggestedCall(name string, missing []string) string {
	if name == "" {
		name = "newFn"
	}
	return fmt.Sprintf("%s(%s)", name, strings.Join(missing, ", "))
}

// needsSemicolon returns true for languages where a bare function call needs a trailing semicolon.
func needsSemicolon(ext string) bool {
	switch ext {
	case ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
		".java", ".kt", ".kts", ".scala", ".sc",
		".js", ".jsx", ".ts", ".tsx", ".mts", ".cts",
		".rs", ".m", ".mm":
		return true
	}
	return false
}
