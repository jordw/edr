package lexkit

// StringScanner is a callback the caller provides to SkipBalanced so
// that string and comment tokens in the body don't perturb depth
// tracking. If the scanner is currently positioned at a string or
// comment token, the callback should consume it and return true.
// Otherwise return false without advancing.
type StringScanner func(*Scanner) bool

// SkipBalanced consumes a balanced (open, close) block. The scanner must
// be positioned at an `open` byte. If strs is non-nil, it is given the
// first chance to consume string/comment tokens in the body so their
// contents don't affect depth.
//
// If the block is unterminated the scanner advances to EOF.
func (s *Scanner) SkipBalanced(open, close byte, strs StringScanner) {
	if s.Pos >= len(s.Src) || s.Src[s.Pos] != open {
		return
	}
	s.Pos++
	depth := 1
	for s.Pos < len(s.Src) && depth > 0 {
		if strs != nil && strs(s) {
			continue
		}
		c := s.Src[s.Pos]
		switch {
		case c == open:
			depth++
			s.Pos++
		case c == close:
			depth--
			s.Pos++
		case c == '\n':
			s.Line++
			s.Pos++
		default:
			s.Pos++
		}
	}
}

// SkipAngles consumes a balanced < ... > block starting at '<'. Used for
// TS/Java/Rust generics. Naive depth counting — does not handle
// shift-operator edge cases, which is fine for generics inside type
// signatures where shifts don't appear.
func (s *Scanner) SkipAngles() {
	if s.Pos >= len(s.Src) || s.Src[s.Pos] != '<' {
		return
	}
	s.Pos++
	depth := 1
	for s.Pos < len(s.Src) && depth > 0 {
		c := s.Src[s.Pos]
		switch c {
		case '<':
			depth++
			s.Pos++
		case '>':
			depth--
			s.Pos++
		case '\n':
			s.Line++
			s.Pos++
		default:
			s.Pos++
		}
	}
}