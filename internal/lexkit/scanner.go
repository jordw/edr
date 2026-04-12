// Package lexkit provides shared scanning primitives for hand-written
// per-language symbol and import extractors.
//
// It centralizes the dangerous, edge-case-ridden parts of tokenizing
// source code — string bodies, comments, regex literals, balanced
// delimiters, line counting — so that language parsers can focus on
// keyword recognition and scope tracking.
//
// A typical parser wraps a Scanner and drives it from a top-level switch:
//
//	for !p.s.EOF() {
//	    c := p.s.Peek()
//	    switch {
//	    case c == '#':
//	        p.s.SkipLineComment()
//	    case c == '"':
//	        p.s.ScanInterpolatedString('"', "#{", p.onInterp)
//	    case lexkit.IsDefaultIdentStart(c):
//	        word := p.s.ScanIdent(lexkit.IsDefaultIdentStart, lexkit.IsDefaultIdentCont)
//	        p.handleIdent(word)
//	    // ...
//	    }
//	}
package lexkit

import "bytes"

// Scanner is a byte-position cursor over source code that tracks line
// numbers automatically.
type Scanner struct {
	Src  []byte
	Pos  int
	Line int // 1-based; incremented on each '\n' consumed via Advance/Next
}

// New creates a Scanner positioned at the start of src with Line=1.
func New(src []byte) Scanner {
	return Scanner{Src: src, Line: 1}
}

// EOF reports whether the scanner has consumed all of Src.
func (s *Scanner) EOF() bool { return s.Pos >= len(s.Src) }

// Peek returns the byte at the current position, or 0 at EOF.
func (s *Scanner) Peek() byte {
	if s.Pos >= len(s.Src) {
		return 0
	}
	return s.Src[s.Pos]
}

// PeekAt returns the byte at offset n from the current position, or 0 if
// out of bounds. PeekAt(0) is equivalent to Peek.
func (s *Scanner) PeekAt(n int) byte {
	i := s.Pos + n
	if i < 0 || i >= len(s.Src) {
		return 0
	}
	return s.Src[i]
}

// Next advances one byte, updating Line on '\n'.
func (s *Scanner) Next() {
	if s.Pos < len(s.Src) {
		if s.Src[s.Pos] == '\n' {
			s.Line++
		}
		s.Pos++
	}
}

// Advance advances n bytes, updating Line for any newlines consumed.
func (s *Scanner) Advance(n int) {
	for i := 0; i < n && s.Pos < len(s.Src); i++ {
		if s.Src[s.Pos] == '\n' {
			s.Line++
		}
		s.Pos++
	}
}

// StartsWith reports whether Src[Pos:] begins with prefix.
func (s *Scanner) StartsWith(prefix string) bool {
	if s.Pos+len(prefix) > len(s.Src) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if s.Src[s.Pos+i] != prefix[i] {
			return false
		}
	}
	return true
}

// SkipSpaces advances past ASCII space, tab, and CR. Does not consume newlines.
func (s *Scanner) SkipSpaces() {
	for s.Pos < len(s.Src) {
		c := s.Src[s.Pos]
		if c == ' ' || c == '\t' || c == '\r' {
			s.Pos++
			continue
		}
		return
	}
}

// SkipSpacesAndNewlines advances past all whitespace including newlines,
// updating Line for each '\n' consumed.
func (s *Scanner) SkipSpacesAndNewlines() {
	for s.Pos < len(s.Src) {
		c := s.Src[s.Pos]
		if c == ' ' || c == '\t' || c == '\r' {
			s.Pos++
			continue
		}
		if c == '\n' {
			s.Line++
			s.Pos++
			continue
		}
		return
	}
}

// SkipLineComment advances to the end of the current line without
// consuming the terminating '\n'. Call this after matching the comment
// start marker (e.g., "//" or "#").
func (s *Scanner) SkipLineComment() {
	rest := s.Src[s.Pos:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		s.Pos += i
	} else {
		s.Pos = len(s.Src)
	}
}

// SkipBlockComment advances through a block comment body and consumes
// the matching close marker. Call after matching the open marker. Updates
// Line for any newlines in the body. If the close marker is never found,
// the scanner advances to EOF.
func (s *Scanner) SkipBlockComment(close string) {
	closeBytes := []byte(close)
	for s.Pos < len(s.Src) {
		rest := s.Src[s.Pos:]
		i := bytes.Index(rest, closeBytes)
		if i < 0 {
			// Unterminated — count newlines to EOF
			for _, c := range rest {
				if c == '\n' {
					s.Line++
				}
			}
			s.Pos = len(s.Src)
			return
		}
		// Count newlines in the span before the close marker
		for _, c := range rest[:i] {
			if c == '\n' {
				s.Line++
			}
		}
		s.Pos += i + len(close)
		return
	}
}