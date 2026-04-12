package lexkit

// ScanIdent reads an identifier starting at the current position and
// returns a zero-copy byte slice into Src. The slice remains valid for
// the lifetime of Src; callers should compare against byte-slice
// constants (bytes.Equal) to avoid allocation, and only convert to
// string when actually recording a symbol name.
//
// Returns nil if the current byte doesn't satisfy isStart or the scanner
// is at EOF.
func (s *Scanner) ScanIdent(isStart, isCont func(byte) bool) []byte {
	if s.Pos >= len(s.Src) || !isStart(s.Src[s.Pos]) {
		return nil
	}
	start := s.Pos
	s.Pos++
	for s.Pos < len(s.Src) && isCont(s.Src[s.Pos]) {
		s.Pos++
	}
	return s.Src[start:s.Pos]
}

// Character class predicates. Language parsers can use these directly or
// compose their own.

// IsASCIIDigit reports whether c is an ASCII digit 0-9.
func IsASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }

// IsASCIIAlpha reports whether c is an ASCII letter a-z or A-Z.
func IsASCIIAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// IsASCIIAlnum reports whether c is an ASCII letter or digit.
func IsASCIIAlnum(c byte) bool { return IsASCIIAlpha(c) || IsASCIIDigit(c) }

// IsDefaultIdentStart matches ASCII letters, underscore, and any high-bit
// byte (conservatively permitting UTF-8 identifiers). Suitable as the
// isStart predicate for most languages.
func IsDefaultIdentStart(c byte) bool {
	return c == '_' || IsASCIIAlpha(c) || c >= 0x80
}

// IsDefaultIdentCont is IsDefaultIdentStart plus ASCII digits.
func IsDefaultIdentCont(c byte) bool {
	return IsDefaultIdentStart(c) || IsASCIIDigit(c)
}