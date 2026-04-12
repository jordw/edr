package lexkit

// ScanSimpleString scans a string literal starting at the opening quote
// (Src[Pos] must equal quote) through the matching close quote. It
// handles backslash escapes but does not recognize any interpolation
// syntax. Line is updated for any '\n' in the body. If the string is
// unterminated the scanner advances to EOF.
func (s *Scanner) ScanSimpleString(quote byte) {
	if s.Pos >= len(s.Src) || s.Src[s.Pos] != quote {
		return
	}
	s.Pos++ // opening quote
	for s.Pos < len(s.Src) {
		c := s.Src[s.Pos]
		if c == '\\' && s.Pos+1 < len(s.Src) {
			if s.Src[s.Pos+1] == '\n' {
				s.Line++
			}
			s.Pos += 2
			continue
		}
		if c == '\n' {
			s.Line++
			s.Pos++
			continue
		}
		if c == quote {
			s.Pos++
			return
		}
		s.Pos++
	}
}

// ScanInterpolatedString scans a string literal that supports
// interpolation. It starts at the opening quote and consumes through the
// matching close. When it encounters interpOpen inside the body, it
// calls onInterp with the scanner positioned just past interpOpen;
// onInterp must advance the scanner past the matching '}'.
//
// Used for TS template literals, Ruby double-quoted strings, shell
// backticks, etc. E.g. for TS: quote is backtick, interpOpen is "${".
// Pass an empty interpOpen to disable interpolation; the function then
// behaves like ScanSimpleString.
func (s *Scanner) ScanInterpolatedString(quote byte, interpOpen string, onInterp func(*Scanner)) {
	if s.Pos >= len(s.Src) || s.Src[s.Pos] != quote {
		return
	}
	s.Pos++
	for s.Pos < len(s.Src) {
		c := s.Src[s.Pos]
		if c == '\\' && s.Pos+1 < len(s.Src) {
			if s.Src[s.Pos+1] == '\n' {
				s.Line++
			}
			s.Pos += 2
			continue
		}
		if c == '\n' {
			s.Line++
			s.Pos++
			continue
		}
		if c == quote {
			s.Pos++
			return
		}
		if len(interpOpen) > 0 && s.StartsWith(interpOpen) {
			s.Advance(len(interpOpen))
			onInterp(s)
			continue
		}
		s.Pos++
	}
}

// ScanSlashRegex scans a JS/Ruby-style regex literal: opening '/',
// through the closing '/' (with char-class awareness), plus any trailing
// ASCII-letter flags. Call only when context indicates a regex is valid
// (typically tracked by the caller as a regexOK flag).
func (s *Scanner) ScanSlashRegex() {
	if s.Pos >= len(s.Src) || s.Src[s.Pos] != '/' {
		return
	}
	s.Pos++ // opening /
	inClass := false
	for s.Pos < len(s.Src) {
		c := s.Src[s.Pos]
		if c == '\\' && s.Pos+1 < len(s.Src) {
			s.Pos += 2
			continue
		}
		if c == '[' {
			inClass = true
			s.Pos++
			continue
		}
		if c == ']' {
			inClass = false
			s.Pos++
			continue
		}
		if c == '\n' {
			s.Line++
			s.Pos++
			continue
		}
		if c == '/' && !inClass {
			s.Pos++
			for s.Pos < len(s.Src) && IsASCIIAlpha(s.Src[s.Pos]) {
				s.Pos++
			}
			return
		}
		s.Pos++
	}
}