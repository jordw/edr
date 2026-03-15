package dispatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/edit"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
)

// writeContent extracts the content string from flags, checking common aliases.
func writeContent(flags map[string]any) string {
	for _, key := range []string{"content", "new_text", "body"} {
		if c := flagString(flags, key, ""); c != "" {
			return c
		}
	}
	return ""
}

// writeResult builds an EditResult from a commitResult.
func writeResult(file string, cr *commitResult, message string) output.EditResult {
	rel := output.Rel(file)
	indexErr := ""
	if len(cr.IndexErrors) > 0 {
		indexErr = cr.IndexErrors[rel]
	}
	return output.EditResult{OK: true, File: rel, Message: message, Hash: cr.Hashes[rel], Status: cr.Status, IndexError: indexErr}
}

func runWriteFile(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("write-file requires 1 argument: <file>")
	}
	content := writeContent(flags)
	mkdir := flagBool(flags, "mkdir", false)
	dryRun := flagBool(flags, "dry-run", false)

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	if mkdir && !dryRun {
		dir := file[:strings.LastIndex(file, "/")]
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("mkdir: %w", err)
		}
	}

	// Check if file exists to determine write strategy
	existingData, readErr := os.ReadFile(file)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, readErr
	}

	if errors.Is(readErr, os.ErrNotExist) {
		if dryRun {
			diff := edit.UnifiedDiff(output.Rel(file), nil, []byte(content))
			return map[string]any{"file": output.Rel(file), "diff": diff, "dry_run": true, "new_file": true}, nil
		}
		// New file: create empty, then commit content as insertion
		if err := os.WriteFile(file, nil, 0644); err != nil {
			return nil, err
		}
		cr, err := commitEdits(ctx, db, []resolvedEdit{{
			File: file, StartByte: 0, EndByte: 0, Replacement: content,
		}})
		if err != nil {
			os.Remove(file) // cleanup the empty file we just created
			return nil, err
		}
		return writeResult(file, cr, fmt.Sprintf("wrote %d bytes", len(content))), nil
	}

	// Guard: refuse to overwrite a non-empty file with empty content
	if content == "" && len(existingData) > 0 && !flagBool(flags, "force", false) {
		return nil, fmt.Errorf("refusing to overwrite non-empty file with empty content (use --force to override)")
	}

	if dryRun {
		diff := edit.UnifiedDiff(output.Rel(file), existingData, []byte(content))
		return map[string]any{"file": output.Rel(file), "diff": diff, "dry_run": true}, nil
	}

	// Overwrite existing: replace all content
	cr, err := commitEdits(ctx, db, []resolvedEdit{{
		File: file, StartByte: 0, EndByte: uint32(len(existingData)),
		Replacement: content, ExpectHash: edit.HashBytes(existingData),
	}})
	if err != nil {
		return nil, err
	}
	return writeResult(file, cr, fmt.Sprintf("wrote %d bytes", len(content))), nil
}

func runAppendFile(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("append-file requires 1 argument: <file>")
	}
	content := writeContent(flags)
	if content == "" {
		return nil, fmt.Errorf("append-file requires 'content' (or 'new_text' or 'body') in flags")
	}

	file := args[0]
	file, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}

	// Ensure there's a newline before appending
	sep := "\n"
	if len(data) > 0 && data[len(data)-1] == '\n' {
		sep = ""
	}
	insertion := sep + content + "\n"

	if flagBool(flags, "dry-run", false) {
		newData := append(append([]byte{}, data...), []byte(insertion)...)
		diff := edit.UnifiedDiff(output.Rel(file), data, newData)
		return map[string]any{"file": output.Rel(file), "diff": diff, "dry_run": true}, nil
	}

	cr, err := commitEdits(ctx, db, []resolvedEdit{{
		File: file, StartByte: uint32(len(data)), EndByte: uint32(len(data)),
		Replacement: insertion, ExpectHash: edit.HashBytes(data),
	}})
	if err != nil {
		return nil, err
	}
	return writeResult(file, cr, fmt.Sprintf("appended %d bytes", len(content))), nil
}

func runInsertAfter(ctx context.Context, db *index.DB, root string, args []string, flags map[string]any) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("insert-after requires 1-2 arguments: [file] <symbol>")
	}
	content := writeContent(flags)
	if content == "" {
		return nil, fmt.Errorf("insert-after requires 'content' (or 'new_text' or 'body') in flags")
	}

	sym, err := resolveSymbolArgs(ctx, db, root, args)
	if err != nil {
		return nil, err
	}

	hash, _ := edit.FileHash(sym.File)

	// Insert content after the symbol, with a blank line separator
	insertion := "\n\n" + content

	if flagBool(flags, "dry-run", false) {
		data, err := os.ReadFile(sym.File)
		if err != nil {
			return nil, err
		}
		newData := make([]byte, 0, len(data)+len(insertion))
		newData = append(newData, data[:sym.EndByte]...)
		newData = append(newData, []byte(insertion)...)
		newData = append(newData, data[sym.EndByte:]...)
		diff := edit.UnifiedDiff(output.Rel(sym.File), data, newData)
		return map[string]any{"file": output.Rel(sym.File), "diff": diff, "dry_run": true}, nil
	}

	cr, err := commitEdits(ctx, db, []resolvedEdit{{
		File: sym.File, StartByte: sym.EndByte, EndByte: sym.EndByte,
		Replacement: insertion, ExpectHash: hash,
	}})
	if err != nil {
		return output.EditResult{OK: false, File: output.Rel(sym.File), Message: err.Error()}, nil
	}
	return writeResult(sym.File, cr, fmt.Sprintf("inserted after %s", sym.Name)), nil
}

// containerTypes lists normalized symbol types that are valid containers for --inside.
var containerTypes = map[string]bool{
	"class":     true, // Python, JS/TS, Java, Ruby
	"struct":    true, // Go, C, Rust
	"enum":      true, // Rust, TS, Java
	"impl":      true, // Rust
	"interface": true, // Go, Java, TS
	"type":      true, // Go type declarations
	"module":    true, // Ruby
}

// runInsertInside inserts content inside a container symbol (class, struct, impl, etc.)
// just before its closing delimiter. If --after is also set, inserts after that child
// symbol within the container instead.
func runInsertInside(ctx context.Context, db *index.DB, root string, file string, containerName string, flags map[string]any) (any, error) {
	content := writeContent(flags)
	if content == "" {
		return nil, fmt.Errorf("write --inside requires 'content' (or 'new_text' or 'body') in flags")
	}

	// Resolve the container symbol
	resolvedFile, err := db.ResolvePath(file)
	if err != nil {
		return nil, err
	}
	container, err := db.GetSymbol(ctx, resolvedFile, containerName)
	if err != nil {
		return nil, fmt.Errorf("container symbol %q not found (try --after <symbol> or --append instead): %w", containerName, err)
	}

	if !containerTypes[container.Type] {
		return nil, fmt.Errorf("symbol %q is a %s, not a container type (class/struct/impl/module)", containerName, container.Type)
	}

	// Determine language config
	cfg := index.GetLangConfig(container.File)

	// Languages with MethodsOutside (Go): --inside adds fields, not methods.
	// Auto-reroute methods to --after.
	if cfg != nil && cfg.MethodsOutside && (container.Type == "struct" || container.Type == "type") {
		data, readErr := os.ReadFile(container.File)
		if readErr == nil {
			body := string(data[container.StartByte:container.EndByte])
			if strings.Contains(body, "struct {") || strings.Contains(body, "struct{") {
				trimmed := strings.TrimSpace(content)
				if strings.HasPrefix(trimmed, "func ") || strings.HasPrefix(trimmed, "func(") {
					// If --after is also set, honor it for placement
					afterChild := flagString(flags, "after", "")
					insertAfter := container.EndByte
					insertLabel := container.Name
					if afterChild != "" {
						afterSym, afterErr := db.GetSymbol(ctx, container.File, afterChild)
						if afterErr != nil {
							return nil, fmt.Errorf("--after symbol %q not found in %s: %w", afterChild, output.Rel(container.File), afterErr)
						}
						insertAfter = afterSym.EndByte
						insertLabel = afterSym.Name
					}
					insertion := "\n\n" + content
					if flagBool(flags, "dry-run", false) {
						newData := make([]byte, 0, len(data)+len(insertion))
						newData = append(newData, data[:insertAfter]...)
						newData = append(newData, []byte(insertion)...)
						newData = append(newData, data[insertAfter:]...)
						diff := edit.UnifiedDiff(output.Rel(container.File), data, newData)
						return map[string]any{"file": output.Rel(container.File), "diff": diff, "dry_run": true}, nil
					}
					hash := edit.HashBytes(data)
					cr, err := commitEdits(ctx, db, []resolvedEdit{{
						File: container.File, StartByte: insertAfter, EndByte: insertAfter,
						Replacement: insertion, ExpectHash: hash,
					}})
					if err != nil {
						return output.EditResult{OK: false, File: output.Rel(container.File), Message: err.Error()}, nil
					}
					return writeResult(container.File, cr, fmt.Sprintf("inserted after %s (auto-rerouted from --inside: Go struct methods go outside)", insertLabel)), nil
				}
			}
		}
	}

	// Read the file
	data, err := os.ReadFile(container.File)
	if err != nil {
		return nil, err
	}

	// If --after is set, find the child symbol and insert after it
	afterChild := flagString(flags, "after", "")
	if afterChild != "" {
		return insertInsideAfterChild(ctx, db, container, afterChild, content, data)
	}

	// Find insertion point: just before the closing delimiter
	insertByte, baseIndent, err := findContainerInsertionPoint(data, container, cfg)
	if err != nil {
		return nil, err
	}

	// Build the insertion with proper indentation
	content = strings.TrimRight(content, "\n")
	indentedContent := indentContent(content, baseIndent)

	// Determine separator: brace languages insert before closing brace,
	// indent/keyword languages insert after the last line of the body.
	insertion := indentedContent + "\n"
	if cfg != nil && cfg.Container != index.ContainerBrace {
		insertion = "\n" + indentedContent + "\n"
	}

	if flagBool(flags, "dry-run", false) {
		newData := make([]byte, 0, len(data)+len(insertion))
		newData = append(newData, data[:insertByte]...)
		newData = append(newData, []byte(insertion)...)
		newData = append(newData, data[insertByte:]...)
		diff := edit.UnifiedDiff(output.Rel(container.File), data, newData)
		return map[string]any{"file": output.Rel(container.File), "diff": diff, "dry_run": true}, nil
	}

	cr, err := commitEdits(ctx, db, []resolvedEdit{{
		File: container.File, StartByte: uint32(insertByte), EndByte: uint32(insertByte),
		Replacement: insertion, ExpectHash: edit.HashBytes(data),
	}})
	if err != nil {
		return output.EditResult{OK: false, File: output.Rel(container.File), Message: err.Error()}, nil
	}
	return writeResult(container.File, cr, fmt.Sprintf("inserted inside %s", container.Name)), nil
}

// insertInsideAfterChild inserts content after a specific child symbol within a container.
func insertInsideAfterChild(ctx context.Context, db *index.DB, container *index.SymbolInfo, childName string, content string, data []byte) (any, error) {
	// Find the child symbol in the same file
	child, err := db.GetSymbol(ctx, container.File, childName)
	if err != nil {
		return nil, fmt.Errorf("child symbol %q not found in %s: %w", childName, output.Rel(container.File), err)
	}

	// Verify child is inside container
	if child.StartByte < container.StartByte || child.EndByte > container.EndByte {
		return nil, fmt.Errorf("symbol %q is not inside %q", childName, container.Name)
	}

	// Detect indentation from the child symbol's first line
	indent := detectIndentAt(data, child.StartByte)

	indentedContent := indentContent(content, indent)
	insertion := "\n\n" + indentedContent

	cr, err := commitEdits(ctx, db, []resolvedEdit{{
		File: container.File, StartByte: child.EndByte, EndByte: child.EndByte,
		Replacement: insertion, ExpectHash: edit.HashBytes(data),
	}})
	if err != nil {
		return output.EditResult{OK: false, File: output.Rel(container.File), Message: err.Error()}, nil
	}
	return writeResult(container.File, cr, fmt.Sprintf("inserted inside %s after %s", container.Name, childName)), nil
}

// findContainerInsertionPoint finds the byte offset just before the closing delimiter
// of a container symbol, and returns the base indentation for content inside it.
func findContainerInsertionPoint(data []byte, container *index.SymbolInfo, cfg *index.LangConfig) (int, string, error) {
	body := data[container.StartByte:container.EndByte]

	style := index.ContainerBrace
	if cfg != nil {
		style = cfg.Container
	}

	switch style {
	case index.ContainerIndent:
		// Indentation-based (Python). Insert at end of container body.
		containerIndent := detectIndentAt(data, container.StartByte)
		indent := containerIndent + "    " // default: 4 spaces deeper
		lines := strings.Split(string(body), "\n")
		for _, line := range lines[1:] { // skip the class/def header line
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "@") {
				indent = extractLeadingWhitespace(line)
				break
			}
		}
		insertByte := int(container.EndByte)
		if insertByte < len(data) && insertByte > 0 && data[insertByte-1] != '\n' {
			for insertByte < len(data) && data[insertByte] != '\n' {
				insertByte++
			}
			if insertByte < len(data) {
				insertByte++ // past the newline
			}
		}
		return insertByte, indent, nil

	case index.ContainerKeyword:
		// Keyword-delimited (Ruby, Lua). Find the last occurrence of the closing keyword.
		closeToken := "end"
		if cfg != nil && cfg.ContainerClose != "" {
			closeToken = cfg.ContainerClose
		}
		endIdx := strings.LastIndex(string(body), closeToken)
		if endIdx < 0 {
			return 0, "", fmt.Errorf("cannot find %q in container %s", closeToken, container.Name)
		}
		insertByte := int(container.StartByte) + endIdx
		containerIndent := detectIndentAt(data, container.StartByte)
		indent := containerIndent + "  "
		return insertByte, indent, nil

	default:
		// Brace-delimited languages: find the last '}'
		lastBrace := strings.LastIndex(string(body), "}")
		if lastBrace < 0 {
			return 0, "", fmt.Errorf("cannot find closing '}' in container %s", container.Name)
		}
		insertByte := int(container.StartByte) + lastBrace
		containerIndent := detectIndentAt(data, container.StartByte)
		indent := containerIndent + "\t"
		// Use spaces if the file uses spaces
		if len(body) > 0 {
			for _, line := range strings.Split(string(body), "\n") {
				if len(line) > 0 && line[0] == ' ' {
					existingIndent := extractLeadingWhitespace(line)
					if len(existingIndent) > len(containerIndent) {
						indent = existingIndent
						break
					}
				}
				if len(line) > 0 && line[0] == '\t' {
					break // tabs confirmed
				}
			}
		}
		return insertByte, indent, nil
	}
}

// detectIndentAt returns the leading whitespace of the line containing the given byte offset.
func detectIndentAt(data []byte, byteOffset uint32) string {
	lineStart := int(byteOffset)
	for lineStart > 0 && data[lineStart-1] != '\n' {
		lineStart--
	}
	return extractLeadingWhitespace(string(data[lineStart:byteOffset]))
}

// extractLeadingWhitespace returns the leading tabs/spaces from a string.
func extractLeadingWhitespace(s string) string {
	for i, c := range s {
		if c != ' ' && c != '\t' {
			return s[:i]
		}
	}
	return s
}

// indentContent applies a base indentation to every line of content.
// It first strips the common leading whitespace from the input so that
// pre-indented content doesn't get double-indented.
func indentContent(content string, indent string) string {
	lines := strings.Split(content, "\n")

	// Find minimum common leading whitespace across non-empty lines
	minIndent := ""
	first := true
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		ws := extractLeadingWhitespace(line)
		if first || len(ws) < len(minIndent) {
			minIndent = ws
			first = false
		}
	}

	// Strip common indent, then apply target indent
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			lines[i] = indent + strings.TrimPrefix(line, minIndent)
		}
	}
	return strings.Join(lines, "\n")
}
