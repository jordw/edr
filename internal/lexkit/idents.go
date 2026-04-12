package lexkit

// ScanIdent reads an identifier starting at the current position and
// returns a zero-copy byte slice into Src. The slice remains valid for
// the lifetime of Src; callers should compare against byte-slice
// constants (bytes.Equal) to avoid allocation, and only convert to
// string when actually recording a symbol name.
//
// Returns nil if the current byte doesn't satisfy isStart or the scanner
// is at EOF. Note: function-pointer callbacks prevent inlining on hot
// paths — prefer ScanIdentTable (~3-4× faster) when using fixed rules.
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

// ScanIdentTable is a faster variant of ScanIdent that takes precomputed
// 256-byte lookup tables instead of function callbacks. One memory load
// per byte, no function-call overhead. Use this on hot paths.
func (s *Scanner) ScanIdentTable(start, cont *[256]bool) []byte {
	if s.Pos >= len(s.Src) || !start[s.Src[s.Pos]] {
		return nil
	}
	startPos := s.Pos
	s.Pos++
	for s.Pos < len(s.Src) && cont[s.Src[s.Pos]] {
		s.Pos++
	}
	return s.Src[startPos:s.Pos]
}

// DefaultIdentStart and DefaultIdentCont are the canonical lookup tables
// for ASCII-letter + underscore + high-bit identifiers (letters and
// underscores to start; add digits for continuation).
var (
	DefaultIdentStart [256]bool
	DefaultIdentCont  [256]bool
)

func init() {
	for i := 0; i < 256; i++ {
		c := byte(i)
		isStart := c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c >= 0x80
		DefaultIdentStart[i] = isStart
		DefaultIdentCont[i] = isStart || (c >= '0' && c <= '9')
	}
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