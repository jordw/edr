package index

// Adapters that convert hand-written parser output (RubyResult,
// TSResult) into the canonical SymbolInfo slice used by the rest of
// the index pipeline. Kept in their own file to isolate the routing
// layer from parseFile's caching logic.

// computeLineOffsets returns a slice where offsets[i] is the byte
// offset at which line i+1 starts. offsets[0] is always 0. Trailing
// entry is the byte position just past the last newline.
func computeLineOffsets(src []byte) []uint32 {
	offsets := make([]uint32, 1, 64)
	for i, b := range src {
		if b == '\n' {
			offsets = append(offsets, uint32(i+1))
		}
	}
	return offsets
}

// lineStartByte returns the byte offset of the first character of the
// given 1-based line, or 0 for invalid input.
func lineStartByte(offsets []uint32, line int) uint32 {
	if line <= 0 || line > len(offsets) {
		return 0
	}
	return offsets[line-1]
}

// lineEndByte returns the byte offset just past the end of the given
// 1-based line (i.e., the start of the next line or srcLen at EOF).
func lineEndByte(offsets []uint32, line int, srcLen int) uint32 {
	if line <= 0 {
		return 0
	}
	if line >= len(offsets) {
		return uint32(srcLen)
	}
	return offsets[line]
}

// rubyToSymbolInfo converts a RubyResult into []SymbolInfo. Byte
// offsets are derived from the line offset table since the Ruby parser
// tracks only line numbers. Ruby "module" is mapped to SymbolInfo type
// "class" to match the ecosystem convention (resolve_rank treats
// "class" as a shape-containing container; "module" is unknown).
func rubyToSymbolInfo(file string, src []byte, r RubyResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		typ := s.Type
		if typ == "module" {
			typ = "class"
		}
		out[i] = SymbolInfo{
			Type:        typ,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

// tsToSymbolInfo converts a TSResult into []SymbolInfo.
func tsToSymbolInfo(file string, src []byte, r TSResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

// goToSymbolInfo converts a GoResult into []SymbolInfo. GoSymbol has no
// Parent field since Go methods are linked to their receiver type by
// name (not index), so ParentIndex is always -1.
func goToSymbolInfo(file string, src []byte, r GoResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: -1,
		}
	}
	return out
}

// rubyImportsToInfo converts Ruby imports to the canonical ImportInfo slice.
func rubyImportsToInfo(file string, r RubyResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

// tsImportsToInfo converts TS imports to ImportInfo.
func tsImportsToInfo(file string, r TSResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

// goImportsToInfo converts Go imports to ImportInfo, preserving aliases.
func goImportsToInfo(file string, r GoResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path, Alias: imp.Alias}
	}
	return out
}

// pyImportsToInfo converts Python imports to ImportInfo.
func pyImportsToInfo(file string, r PyResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

// cppToSymbolInfo converts a CppResult into []SymbolInfo.
func cppToSymbolInfo(file string, src []byte, r CppResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

func rustToSymbolInfo(file string, src []byte, r RustResult) []SymbolInfo {
	if len(r.Symbols) == 0 { return nil }
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{Type: s.Type, Name: s.Name, File: file,
			StartLine: uint32(s.StartLine), EndLine: uint32(s.EndLine),
			StartByte: lineStartByte(offsets, s.StartLine),
			EndByte: lineEndByte(offsets, s.EndLine, srcLen), ParentIndex: s.Parent}
	}
	return out
}

func rustImportsToInfo(file string, r RustResult) []ImportInfo {
	if len(r.Imports) == 0 { return nil }
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

func javaToSymbolInfo(file string, src []byte, r JavaResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

func javaImportsToInfo(file string, r JavaResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

func csharpToSymbolInfo(file string, src []byte, r CSharpResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

func csharpImportsToInfo(file string, r CSharpResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

func cppImportsToInfo(file string, r CppResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

func kotlinToSymbolInfo(file string, src []byte, r KotlinResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

func kotlinImportsToInfo(file string, r KotlinResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

func swiftToSymbolInfo(file string, src []byte, r SwiftResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

func swiftImportsToInfo(file string, r SwiftResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

func scalaToSymbolInfo(file string, src []byte, r ScalaResult) []SymbolInfo {
	if len(r.Symbols) == 0 { return nil }
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{Type: s.Type, Name: s.Name, File: file,
			StartLine: uint32(s.StartLine), EndLine: uint32(s.EndLine),
			StartByte: lineStartByte(offsets, s.StartLine),
			EndByte: lineEndByte(offsets, s.EndLine, srcLen), ParentIndex: s.Parent}
	}
	return out
}

func scalaImportsToInfo(file string, r ScalaResult) []ImportInfo {
	if len(r.Imports) == 0 { return nil }
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

func phpToSymbolInfo(file string, src []byte, r PhpResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}

func phpImportsToInfo(file string, r PhpResult) []ImportInfo {
	if len(r.Imports) == 0 {
		return nil
	}
	out := make([]ImportInfo, len(r.Imports))
	for i, imp := range r.Imports {
		out[i] = ImportInfo{File: file, ImportPath: imp.Path}
	}
	return out
}

// pythonToSymbolInfo converts a PyResult into []SymbolInfo.
func pythonToSymbolInfo(file string, src []byte, r PyResult) []SymbolInfo {
	if len(r.Symbols) == 0 {
		return nil
	}
	offsets := computeLineOffsets(src)
	srcLen := len(src)
	out := make([]SymbolInfo, len(r.Symbols))
	for i, s := range r.Symbols {
		out[i] = SymbolInfo{
			Type:        s.Type,
			Name:        s.Name,
			File:        file,
			StartLine:   uint32(s.StartLine),
			EndLine:     uint32(s.EndLine),
			StartByte:   lineStartByte(offsets, s.StartLine),
			EndByte:     lineEndByte(offsets, s.EndLine, srcLen),
			ParentIndex: s.Parent,
		}
	}
	return out
}