package index

import (
	"os"
	"path/filepath"
	"strings"
)

// ExtractSignature returns just the signature of a symbol — the first line(s)
// up to the opening brace or colon, without the body. This is much cheaper
// than the full source and provides enough info for understanding APIs.
func ExtractSignature(sym SymbolInfo) string {
	data, err := os.ReadFile(sym.File)
	if err != nil || int(sym.EndByte) > len(data) {
		return sym.Type + " " + sym.Name
	}
	return ExtractSignatureFromSource(sym, data)
}

// ExtractSignatureFromSource is like ExtractSignature but takes pre-loaded source bytes,
// avoiding redundant file reads when processing multiple symbols from the same file.
func ExtractSignatureFromSource(sym SymbolInfo, src []byte) string {
	if int(sym.EndByte) > len(src) {
		return sym.Type + " " + sym.Name
	}
	body := string(src[sym.StartByte:sym.EndByte])

	ext := filepath.Ext(sym.File)
	switch ext {
	case ".go":
		return goSignature(body, sym.Type)
	case ".py":
		return pythonSignature(body)
	case ".js", ".jsx", ".ts", ".tsx":
		return jsSignature(body)
	case ".rs":
		return rustSignature(body)
	case ".java":
		return javaSignature(body)
	case ".rb":
		return rubySignature(body)
	case ".c", ".h", ".cpp", ".cc":
		return cSignature(body)
	default:
		return firstLine(body)
	}
}

// goSignature extracts a Go function/type/var signature.
func goSignature(body, symType string) string {
	switch symType {
	case "function", "method":
		// Take everything up to the opening brace
		if idx := strings.Index(body, "{"); idx >= 0 {
			sig := strings.TrimRight(body[:idx], " \t\n")
			return sig
		}
		return firstLine(body)
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

// jsSignature extracts a JS/TS function signature.
func jsSignature(body string) string {
	// Take everything up to the opening brace
	if idx := strings.Index(body, "{"); idx >= 0 {
		sig := strings.TrimRight(body[:idx], " \t\n")
		return sig
	}
	// Arrow functions without braces: take first line
	return firstLine(body)
}

// rustSignature extracts a Rust fn/struct/impl signature.
func rustSignature(body string) string {
	if idx := strings.Index(body, "{"); idx >= 0 {
		return strings.TrimRight(body[:idx], " \t\n")
	}
	return firstLine(body)
}

// javaSignature extracts a Java method/class signature.
func javaSignature(body string) string {
	if idx := strings.Index(body, "{"); idx >= 0 {
		return strings.TrimRight(body[:idx], " \t\n")
	}
	return firstLine(body)
}

// rubySignature extracts a Ruby def/class signature.
func rubySignature(body string) string {
	return firstLine(body)
}

// cSignature extracts a C/C++ function/struct signature.
func cSignature(body string) string {
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

	// Go structs/interfaces: fields aren't indexed as symbols.
	// Fall back to extractGoFields which parses them from source.
	if ext == ".go" && len(lines) == 1 {
		body := string(data[container.StartByte:container.EndByte])
		if idx := strings.Index(body, "{"); idx >= 0 {
			fields := extractGoFields(body[idx+1:])
			if fields != "" {
				lines = append(lines, "    "+fields)
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
		return rubySignature(body)
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

// firstLine returns the first line of s.
func firstLine(s string) string {
	if idx := strings.Index(s, "\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}
