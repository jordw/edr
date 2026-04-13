package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExtractSignatureCtx is like ExtractSignature but uses the context's source cache.
func ExtractSignatureCtx(ctx context.Context, sym SymbolInfo) string {
	data, err := CachedReadFile(ctx, sym.File)
	if err != nil || int(sym.EndByte) > len(data) {
		return sym.Type + " " + sym.Name
	}
	return ExtractSignatureFromSource(sym, data)
}

// ExtractSignatureFromSource is like ExtractSignature but takes pre-loaded source bytes,
// avoiding redundant file reads when processing multiple symbols from the same file.
func ExtractSignatureFromSource(sym SymbolInfo, src []byte) string {
	if int(sym.EndByte) > len(src) || sym.StartByte > sym.EndByte {
		return sym.Type + " " + sym.Name
	}
	body := string(src[sym.StartByte:sym.EndByte])

	ext := filepath.Ext(sym.File)
	switch ext {
	case ".go":
		return goSignature(body, sym.Type)
	case ".py":
		return pythonSignature(body)
	case ".js", ".jsx", ".ts", ".tsx",
		".rs", ".java", ".c", ".h",
		".cpp", ".cc", ".cxx", ".hpp", ".hxx", ".hh",
		".php", ".zig":
		return braceSignature(body)
	default:
		return firstLine(body)
	}
}

// goSignature extracts a Go function/type/var signature.
func goSignature(body, symType string) string {
	switch symType {
	case "function", "method":
		return braceSignature(body)
	case "type":
		// For structs/interfaces: "type Foo struct {" + field names
		if idx := strings.Index(body, "{"); idx >= 0 {
			header := strings.TrimRight(body[:idx+1], " \t\n") + " "
			// Extract field names from the body
			fields := extractGoFields(body[idx+1:])
			if fields != "" {
				return header + fields + " }"
			}
			return header + "... }"
		}
		return firstLine(body)
	default:
		// Variable/const: first line
		return firstLine(body)
	}
}

// extractGoFields extracts field names and types from a struct body.
// extractCFields extracts field declarations from a C/C++ struct body.
// Returns one compact line per field (e.g. "unsigned int nr_running;").
// Skips comments, preprocessor directives, and blank lines.
func extractCFields(body string) []string {
	lines := strings.Split(body, "\n")
	var fields []string
	inComment := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Track block comments
		if inComment {
			if idx := strings.Index(trimmed, "*/"); idx >= 0 {
				inComment = false
				trimmed = strings.TrimSpace(trimmed[idx+2:])
				if trimmed == "" {
					continue
				}
			} else {
				continue
			}
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inComment = true
			}
			continue
		}
		// Skip blanks, closing brace, comments, preprocessor
		if trimmed == "" || trimmed == "}" || trimmed == "};" {
			continue
		}
		if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// Strip trailing inline comments
		if idx := strings.Index(trimmed, "/*"); idx > 0 {
			trimmed = strings.TrimSpace(trimmed[:idx])
		}
		if idx := strings.Index(trimmed, "//"); idx > 0 {
			trimmed = strings.TrimSpace(trimmed[:idx])
		}
		if trimmed != "" {
			fields = append(fields, trimmed)
		}
	}
	return fields
}

func extractGoFields(body string) string {
	lines := strings.Split(body, "\n")
	var fields []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line == "}" || strings.HasPrefix(line, "//") {
			continue
		}
		// Take field name + type (first two tokens)
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			fields = append(fields, parts[0]+" "+parts[1])
		} else if len(parts) == 1 && parts[0] != "}" {
			fields = append(fields, parts[0]) // embedded type
		}
	}
	if len(fields) == 0 {
		return ""
	}
	const maxGoSigFields = 30
	if len(fields) > maxGoSigFields {
		return strings.Join(fields[:maxGoSigFields], "; ") + fmt.Sprintf("; // ... %d more fields", len(fields)-maxGoSigFields)
	}
	return strings.Join(fields, "; ")
}

// pythonSignature extracts a Python def/class signature.
func pythonSignature(body string) string {
	// Include decorator lines + the def/class line
	lines := strings.Split(body, "\n")
	var sig []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "@") {
			sig = append(sig, line)
			continue
		}
		if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "class ") {
			// Take up to the colon
			if idx := strings.Index(line, ":"); idx >= 0 {
				sig = append(sig, line[:idx+1])
			} else {
				sig = append(sig, line)
			}
			break
		}
		// If we hit a non-decorator, non-def line first, just take first line
		sig = append(sig, line)
		break
	}
	return strings.Join(sig, "\n")
}

// braceSignature extracts everything up to the opening brace, or the first line.
// Used by brace-delimited languages (JS, Rust, Java, C, C++, PHP, Zig).
func braceSignature(body string) string {
	if idx := strings.Index(body, "{"); idx >= 0 {
		return strings.TrimRight(body[:idx], " \t\n")
	}
	return firstLine(body)
}

// ExtractContainerStub generates a compact "interface view" of a container symbol.
// It returns the container header + each child's signature, without implementation bodies.
// This is dramatically cheaper than reading the full container body.
func ExtractContainerStub(container SymbolInfo, children []SymbolInfo) string {
	data, err := os.ReadFile(container.File)
	if err != nil {
		return container.Type + " " + container.Name
	}

	// Get the container's header line (up to and including the opening delimiter).
	// We don't use ExtractSignatureFromSource here because for types it returns
	// a compact single-line form that already includes "}", causing a dangling brace.
	ext := filepath.Ext(container.File)
	header := containerHeader(container, data, ext)

	var lines []string
	lines = append(lines, header)

	for _, child := range children {
		// Only include direct children (within the container's byte range)
		if child.StartByte < container.StartByte || child.EndByte > container.EndByte {
			continue
		}
		// Skip the container itself
		if child.Name == container.Name && child.StartByte == container.StartByte {
			continue
		}

		sig := ExtractSignatureFromSource(child, data)

		switch ext {
		case ".py":
			// Python: include docstring if present
			body := string(data[child.StartByte:child.EndByte])
			if doc := extractPythonDocstring(body); doc != "" {
				sig += "\n" + doc
			} else {
				sig += " ..."
			}
		case ".rb":
			sig += "\n    end"
		}

		lines = append(lines, sig)
	}

	// Struct/class fields aren't indexed as symbols in most languages.
	// When no child symbols were found, extract field lines from source.
	// Cap at maxSigFields to avoid dumping huge structs.
	const maxSigFields = 30
	if len(lines) == 1 {
		body := string(data[container.StartByte:container.EndByte])
		if idx := strings.Index(body, "{"); idx >= 0 {
			inner := body[idx+1:]
			if ext == ".go" {
				if fields := extractGoFields(inner); fields != "" {
					lines = append(lines, "    "+fields)
				}
			} else {
				if fields := extractCFields(inner); len(fields) > 0 {
					if len(fields) > maxSigFields {
						for _, f := range fields[:maxSigFields] {
							lines = append(lines, "\t"+f)
						}
						lines = append(lines, fmt.Sprintf("\t// ... %d more fields", len(fields)-maxSigFields))
					} else {
						for _, f := range fields {
							lines = append(lines, "\t"+f)
						}
					}
				}
			}
		}
	}

	// Go structs: also include receiver methods defined outside the struct body.
	if ext == ".go" && (container.Type == "type" || container.Type == "struct") {
		for _, child := range children {
			// Skip children inside the container (already handled above)
			if child.StartByte >= container.StartByte && child.EndByte <= container.EndByte {
				continue
			}
			if child.Type != "function" {
				continue
			}
			recv := extractGoReceiver(data, child)
			if recv == container.Name {
				sig := ExtractSignatureFromSource(child, data)
				lines = append(lines, "  "+sig)
			}
		}
	}

	// Add closing delimiter
	switch ext {
	case ".py":
		// No closing delimiter needed
	case ".rb":
		lines = append(lines, "end")
	default:
		body := string(data[container.StartByte:container.EndByte])
		if strings.Contains(body, "{") {
			lines = append(lines, "}")
		}
	}

	return strings.Join(lines, "\n")
}

// containerHeader returns just the opening line of a container (up to and including
// the opening brace/colon), suitable for use as a stub header.
func containerHeader(sym SymbolInfo, src []byte, ext string) string {
	if int(sym.EndByte) > len(src) {
		return sym.Type + " " + sym.Name
	}
	body := string(src[sym.StartByte:sym.EndByte])

	switch ext {
	case ".py":
		// Python: first line (class Foo:) or up to the colon
		return pythonSignature(body)
	case ".rb":
		return firstLine(body)
	default:
		// Brace-delimited languages: everything up to and including "{"
		if idx := strings.Index(body, "{"); idx >= 0 {
			return strings.TrimRight(body[:idx+1], " \t\n")
		}
		return firstLine(body)
	}
}

// extractPythonDocstring returns the docstring from a Python function body, if present.
func extractPythonDocstring(body string) string {
	bodyLines := strings.Split(body, "\n")
	for i := 1; i < len(bodyLines); i++ {
		trimmed := strings.TrimSpace(bodyLines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, `"""`) || strings.HasPrefix(trimmed, `'''`) {
			quote := trimmed[:3]
			if strings.Count(trimmed, quote) >= 2 {
				return "    " + trimmed
			}
			doc := []string{"    " + trimmed}
			for j := i + 1; j < len(bodyLines); j++ {
				doc = append(doc, bodyLines[j])
				if strings.Contains(bodyLines[j], quote) {
					break
				}
			}
			return strings.Join(doc, "\n")
		}
		break
	}
	return ""
}

// GoFileSignatures produces a signatures view of a Go file that groups
// receiver methods under their type definitions. Non-Go files return "".
func GoFileSignatures(file string, syms []SymbolInfo) string {
	if filepath.Ext(file) != ".go" {
		return ""
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}

	// Separate types, methods, and other top-level symbols
	type typeEntry struct {
		sig     string
		methods []string
		line    uint32
	}
	types := make(map[string]*typeEntry) // receiver name → entry
	var typeOrder []string
	var other []struct {
		sig  string
		line uint32
	}

	// Collect function/method spans to filter out locals
	type span struct{ start, end uint32 }
	var funcSpans []span
	for _, s := range syms {
		if s.File == file && (s.Type == "function" || s.Type == "method") {
			funcSpans = append(funcSpans, span{s.StartLine, s.EndLine})
		}
	}
	isLocal := func(s SymbolInfo) bool {
		if s.Type == "function" || s.Type == "method" || s.Type == "type" {
			return false
		}
		for _, fs := range funcSpans {
			if s.StartLine > fs.start && s.EndLine <= fs.end {
				return true
			}
		}
		return false
	}

	for _, s := range syms {
		if s.File != file || isLocal(s) {
			continue
		}
		sig := ExtractSignatureFromSource(s, data)
		switch s.Type {
		case "type":
			types[s.Name] = &typeEntry{sig: sig, line: s.StartLine}
			typeOrder = append(typeOrder, s.Name)
		case "function":
			// Go methods are typed "function" — check for receiver
			recv := extractGoReceiver(data, s)
			if recv != "" {
				if te, ok := types[recv]; ok {
					te.methods = append(te.methods, sig)
				} else {
					types[recv] = &typeEntry{sig: recv + " (external type)", methods: []string{sig}, line: s.StartLine}
					typeOrder = append(typeOrder, recv)
				}
			} else {
				other = append(other, struct {
					sig  string
					line uint32
				}{sig, s.StartLine})
			}
		default:
			other = append(other, struct {
				sig  string
				line uint32
			}{sig, s.StartLine})
		}
	}

	var lines []string
	// Output types with their methods grouped
	for _, name := range typeOrder {
		te := types[name]
		lines = append(lines, te.sig)
		for _, m := range te.methods {
			lines = append(lines, "  "+m)
		}
		if len(te.methods) > 0 {
			lines = append(lines, "")
		}
	}
	// Output other top-level symbols
	for _, o := range other {
		lines = append(lines, o.sig)
	}
	return strings.Join(lines, "\n")
}

// extractGoReceiver extracts the receiver type name from a Go method symbol.
// e.g. "func (s *Server) Handle()" → "Server"
func extractGoReceiver(src []byte, sym SymbolInfo) string {
	if int(sym.EndByte) > len(src) {
		return ""
	}
	body := string(src[sym.StartByte:sym.EndByte])
	// Find "func (" then extract receiver type
	idx := strings.Index(body, "func (")
	if idx < 0 {
		idx = strings.Index(body, "func(")
		if idx < 0 {
			return ""
		}
	}
	// Find closing paren of receiver
	start := strings.Index(body[idx:], "(")
	if start < 0 {
		return ""
	}
	end := strings.Index(body[idx+start:], ")")
	if end < 0 {
		return ""
	}
	recv := body[idx+start+1 : idx+start+end]
	// recv is like "s *Server" or "s Server" or "*Server"
	recv = strings.TrimSpace(recv)
	// Remove pointer star and variable name
	recv = strings.TrimPrefix(recv, "*")
	parts := strings.Fields(recv)
	if len(parts) == 0 {
		return ""
	}
	last := parts[len(parts)-1]
	return strings.TrimPrefix(last, "*")
}

// firstLine returns the first line of s.
func firstLine(s string) string {
	if idx := strings.Index(s, "\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}
