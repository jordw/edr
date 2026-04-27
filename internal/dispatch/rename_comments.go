package dispatch

import "bytes"

// commentSyntaxFor returns a small bitmask describing which comment styles
// the file's language uses. Approximate but enough to cover the languages we
// support: line comments via // for C-family/JS/TS/Rust/Go, # for Python/Ruby
// /shell/Makefile, -- for SQL/Lua, ; for Lisp/asm. Block comments via /* */
// for C-family/JS/TS/Rust/Go, """ for Python.
func commentSyntaxFor(file string) commentSyntax {
	ext := ""
	for i := len(file) - 1; i >= 0; i-- {
		if file[i] == '.' {
			ext = file[i:]
			break
		}
		if file[i] == '/' {
			break
		}
	}
	switch ext {
	case ".c", ".h", ".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
		".m", ".mm", ".java", ".kt", ".kts", ".scala", ".sc",
		".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".mts", ".cts",
		".go", ".rs", ".swift", ".cs", ".dart", ".groovy", ".php":
		return commentSyntax{slashSlash: true, slashStar: true}
	case ".py", ".pyi":
		return commentSyntax{hash: true, tripleQuote: true}
	case ".rb", ".sh", ".bash", ".zsh", ".pl", ".pm", ".tcl", ".cmake":
		return commentSyntax{hash: true}
	case ".lua", ".sql":
		return commentSyntax{dashDash: true}
	}
	// Default: slash-slash + slash-star covers most curly-brace languages we
	// haven't enumerated.
	return commentSyntax{slashSlash: true, slashStar: true}
}

type commentSyntax struct {
	slashSlash  bool
	slashStar   bool
	hash        bool
	dashDash    bool
	tripleQuote bool
}

// positionInComment reports whether the byte at `pos` in `data` falls inside
// a comment, scanning back to the start of the line for line-comments and
// scanning the file for the most recent unterminated block-comment opener.
//
// Cheap and approximate — we don't tokenize strings, so a comment marker
// inside a string literal will be misclassified. For the rename use case the
// trade-off is fine: a false positive (treating "// foo" inside a string as
// a comment) just means we report it as a comment edit, which is at worst
// noisy in the summary, never an incorrect edit.
func positionInComment(data []byte, pos int, syn commentSyntax) bool {
	if pos < 0 || pos >= len(data) {
		return false
	}
	// Scan back to the line start.
	lineStart := pos
	for lineStart > 0 && data[lineStart-1] != '\n' {
		lineStart--
	}
	for i := lineStart; i < pos; i++ {
		c := data[i]
		if syn.slashSlash && c == '/' && i+1 < len(data) && data[i+1] == '/' {
			return true
		}
		if syn.hash && c == '#' {
			return true
		}
		if syn.dashDash && c == '-' && i+1 < len(data) && data[i+1] == '-' {
			return true
		}
	}
	// Block-comment scan: look for the nearest /* before pos that isn't
	// closed before pos. (Skip when the language doesn't use /* */.)
	if syn.slashStar {
		open := bytes.LastIndex(data[:pos], []byte("/*"))
		if open >= 0 {
			close := bytes.Index(data[open:pos], []byte("*/"))
			if close < 0 {
				return true
			}
		}
	}
	return false
}

// findCommentMentions returns spans for word-bounded occurrences of name
// inside comment regions of data (per syn). Used by --update-comments to
// rewrite doc/comment mentions of the renamed symbol that the symbol-graph
// resolver does not return as refs (it only expands the *declaration's*
// leading doc comment, not arbitrary mentions in unrelated comments).
func findCommentMentions(data []byte, name string, syn commentSyntax) []span {
	if name == "" {
		return nil
	}
	nb := []byte(name)
	var out []span
	i := 0
	for i+len(nb) <= len(data) {
		idx := bytes.Index(data[i:], nb)
		if idx < 0 {
			break
		}
		abs := i + idx
		absEnd := abs + len(nb)
		leftOK := abs == 0 || !isIdentByte(data[abs-1])
		rightOK := absEnd >= len(data) || !isIdentByte(data[absEnd])
		if leftOK && rightOK && positionInComment(data, abs, syn) {
			out = append(out, span{uint32(abs), uint32(absEnd)})
		}
		i = abs + 1
	}
	return out
}

// countNameInCode counts word-bounded occurrences of name in data that are
// NOT inside a comment (per syn). Used as a sanity check against the resolver:
// if the resolver finds fewer code spans than this returns, some references
// were missed (or are intentionally skipped — shadowed locals, look-alikes
// in string literals — so this is signal not proof).
func countNameInCode(data []byte, name string, syn commentSyntax) int {
	if name == "" {
		return 0
	}
	nb := []byte(name)
	count := 0
	i := 0
	for i+len(nb) <= len(data) {
		idx := bytes.Index(data[i:], nb)
		if idx < 0 {
			break
		}
		abs := i + idx
		absEnd := abs + len(nb)
		leftOK := abs == 0 || !isIdentByte(data[abs-1])
		rightOK := absEnd >= len(data) || !isIdentByte(data[absEnd])
		if leftOK && rightOK && !positionInComment(data, abs, syn) {
			count++
		}
		i = abs + 1
	}
	return count
}
