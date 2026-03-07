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
	body := string(data[sym.StartByte:sym.EndByte])

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

// firstLine returns the first line of s.
func firstLine(s string) string {
	if idx := strings.Index(s, "\n"); idx >= 0 {
		return s[:idx]
	}
	return s
}
