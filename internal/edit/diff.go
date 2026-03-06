package edit

import (
	"fmt"
	"os"
	"strings"
)

// DiffPreview generates a unified diff showing what replacing the byte range
// [startByte, endByte) with replacement would look like, without modifying the
// file. Returns the diff string.
func DiffPreview(path string, startByte, endByte uint32, replacement string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("diffpreview: read: %w", err)
	}

	if int(startByte) > len(data) || int(endByte) > len(data) || startByte > endByte {
		return "", fmt.Errorf("diffpreview: invalid byte range [%d, %d) for file of length %d", startByte, endByte, len(data))
	}

	// Build the "after" version
	after := make([]byte, 0, int(startByte)+len(replacement)+len(data)-int(endByte))
	after = append(after, data[:startByte]...)
	after = append(after, []byte(replacement)...)
	after = append(after, data[endByte:]...)

	oldLines := splitLines(string(data))
	newLines := splitLines(string(after))

	// Convert byte offsets to line numbers (0-indexed, exclusive end).
	// For end offsets we use the last byte of the range so that mid-line
	// boundaries correctly include the affected line.
	oldStartLine := byteOffsetToLine(data, startByte)
	oldEndLine := oldStartLine
	if endByte > startByte {
		oldEndLine = byteOffsetToLine(data, endByte-1) + 1
	}
	replEnd := uint32(int(startByte) + len(replacement))
	newEndLine := oldStartLine
	if len(replacement) > 0 {
		newEndLine = byteOffsetToLine(after, replEnd-1) + 1
	}

	// Context lines
	const contextLines = 3

	// Calculate the hunk boundaries (0-indexed)
	ctxStart := oldStartLine - contextLines
	if ctxStart < 0 {
		ctxStart = 0
	}
	oldCtxEnd := oldEndLine + contextLines
	if oldCtxEnd > len(oldLines) {
		oldCtxEnd = len(oldLines)
	}
	newCtxEnd := newEndLine + contextLines
	if newCtxEnd > len(newLines) {
		newCtxEnd = len(newLines)
	}

	var b strings.Builder

	// File headers
	fmt.Fprintf(&b, "--- a/%s\n", stripToRelative(path))
	fmt.Fprintf(&b, "+++ b/%s\n", stripToRelative(path))

	// Build hunk content
	var oldHunkLen, newHunkLen int
	type diffLine struct {
		prefix byte
		text   string
	}
	var lines []diffLine

	// Context before
	for i := ctxStart; i < oldStartLine; i++ {
		lines = append(lines, diffLine{' ', oldLines[i]})
	}
	// Removed lines
	for i := oldStartLine; i < oldEndLine; i++ {
		lines = append(lines, diffLine{'-', oldLines[i]})
	}
	// Added lines
	for i := oldStartLine; i < newEndLine; i++ {
		lines = append(lines, diffLine{'+', newLines[i]})
	}
	// Context after (use old lines offset appropriately)
	contextAfterStart := oldEndLine
	contextAfterEnd := oldCtxEnd
	for i := contextAfterStart; i < contextAfterEnd; i++ {
		lines = append(lines, diffLine{' ', oldLines[i]})
	}

	// Count old and new lines in the hunk
	oldCount := 0
	newCount := 0
	for _, l := range lines {
		switch l.prefix {
		case ' ':
			oldCount++
			newCount++
		case '-':
			oldCount++
		case '+':
			newCount++
		}
	}
	oldHunkLen = oldCount
	newHunkLen = newCount

	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", ctxStart+1, oldHunkLen, ctxStart+1, newHunkLen)

	for _, l := range lines {
		fmt.Fprintf(&b, "%c%s\n", l.prefix, l.text)
	}

	return b.String(), nil
}

// splitLines splits text into lines, stripping the trailing newline so the
// result length equals the number of lines in the file.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	// strings.Split on a trailing newline produces an empty final element
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// byteOffsetToLine returns the 0-indexed line number for the given byte offset.
func byteOffsetToLine(data []byte, offset uint32) int {
	line := 0
	for i := 0; i < int(offset) && i < len(data); i++ {
		if data[i] == '\n' {
			line++
		}
	}
	return line
}

// countLinesInRange counts lines in the byte range [start, end) of data.
func countLinesInRange(data []byte, start, end uint32) int {
	lines := 0
	for i := int(start); i < int(end) && i < len(data); i++ {
		if data[i] == '\n' {
			lines++
		}
	}
	return lines
}

// stripToRelative tries to produce a short path by removing common prefixes.
// If the path is absolute, it just returns the base-ish portion.
func stripToRelative(path string) string {
	// Just return the path as-is; callers can provide relative paths if desired.
	return path
}
