package edit

import (
	"fmt"
	"os"
	"strings"

	"github.com/jordw/edr/internal/output"
)

// --- Canonical LCS-based unified diff engine ---

type lcsMatch struct {
	oldIdx int
	newIdx int
}

type diffLine struct {
	prefix byte
	text   string
}

func lcsLines(old, new []string) []lcsMatch {
	m, n := len(old), len(new)
	if m == 0 || n == 0 {
		return nil
	}
	if m*n > 1000000 {
		return lcsSimple(old, new)
	}

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if old[i-1] == new[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] >= dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	var result []lcsMatch
	i, j := m, n
	for i > 0 && j > 0 {
		if old[i-1] == new[j-1] {
			result = append(result, lcsMatch{i - 1, j - 1})
			i--
			j--
		} else if dp[i-1][j] >= dp[i][j-1] {
			i--
		} else {
			j--
		}
	}
	for l, r := 0, len(result)-1; l < r; l, r = l+1, r-1 {
		result[l], result[r] = result[r], result[l]
	}
	return result
}

func lcsSimple(old, new []string) []lcsMatch {
	var result []lcsMatch
	m, n := len(old), len(new)

	prefix := 0
	for prefix < m && prefix < n && old[prefix] == new[prefix] {
		result = append(result, lcsMatch{prefix, prefix})
		prefix++
	}

	suffix := 0
	for suffix < m-prefix && suffix < n-prefix && old[m-1-suffix] == new[n-1-suffix] {
		suffix++
	}

	if prefix < m-suffix && prefix < n-suffix {
		middle := prefix
		oi, ni := prefix, prefix
		for oi < m-suffix && ni < n-suffix {
			if old[oi] == new[ni] {
				result = append(result, lcsMatch{oi, ni})
				oi++
				ni++
			} else {
				_ = middle
				ni++
				if ni >= n-suffix {
					oi++
					ni = prefix
				}
			}
		}
	}

	for i := 0; i < suffix; i++ {
		result = append(result, lcsMatch{m - suffix + i, n - suffix + i})
	}
	return result
}

func buildHunks(old, new []string, lcs []lcsMatch, contextLines int) []string {
	var allLines []diffLine
	type linePos struct {
		oldIdx int
		newIdx int
	}
	var positions []linePos

	oi, ni := 0, 0
	for _, m := range lcs {
		for oi < m.oldIdx {
			allLines = append(allLines, diffLine{'-', old[oi]})
			positions = append(positions, linePos{oi, ni})
			oi++
		}
		for ni < m.newIdx {
			allLines = append(allLines, diffLine{'+', new[ni]})
			positions = append(positions, linePos{oi, ni})
			ni++
		}
		allLines = append(allLines, diffLine{' ', old[oi]})
		positions = append(positions, linePos{oi, ni})
		oi++
		ni++
	}
	for oi < len(old) {
		allLines = append(allLines, diffLine{'-', old[oi]})
		positions = append(positions, linePos{oi, ni})
		oi++
	}
	for ni < len(new) {
		allLines = append(allLines, diffLine{'+', new[ni]})
		positions = append(positions, linePos{oi, ni})
		ni++
	}

	if len(allLines) == 0 {
		return nil
	}

	hasChanges := false
	for _, dl := range allLines {
		if dl.prefix != ' ' {
			hasChanges = true
			break
		}
	}
	if !hasChanges {
		return nil
	}

	var hunks []string
	inHunk := false
	hunkStart := 0
	lastChange := -contextLines - 1

	for i, dl := range allLines {
		if dl.prefix != ' ' {
			if !inHunk {
				hunkStart = i - contextLines
				if hunkStart < 0 {
					hunkStart = 0
				}
				inHunk = true
			}
			lastChange = i
		} else if inHunk && i-lastChange > contextLines {
			hunkEnd := lastChange + contextLines + 1
			if hunkEnd > len(allLines) {
				hunkEnd = len(allLines)
			}
			hunks = append(hunks, formatHunk(allLines[hunkStart:hunkEnd], positions[hunkStart].oldIdx, positions[hunkStart].newIdx))
			inHunk = false
		}
	}
	if inHunk {
		hunkEnd := lastChange + contextLines + 1
		if hunkEnd > len(allLines) {
			hunkEnd = len(allLines)
		}
		hunks = append(hunks, formatHunk(allLines[hunkStart:hunkEnd], positions[hunkStart].oldIdx, positions[hunkStart].newIdx))
	}

	return hunks
}

func formatHunk(lines []diffLine, oldStart, newStart int) string {
	var b strings.Builder
	oldCount, newCount := 0, 0
	for _, dl := range lines {
		if dl.prefix != '+' {
			oldCount++
		}
		if dl.prefix != '-' {
			newCount++
		}
	}
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", oldStart+1, oldCount, newStart+1, newCount)
	for _, dl := range lines {
		fmt.Fprintf(&b, "%c%s\n", dl.prefix, dl.text)
	}
	return b.String()
}

// UnifiedDiff produces a multi-hunk unified diff from old and new byte slices.
// This is the canonical diff function used by all callers.
func UnifiedDiff(label string, old, new []byte) string {
	oldLines := splitLines(string(old))
	newLines := splitLines(string(new))

	lcs := lcsLines(oldLines, newLines)
	hunks := buildHunks(oldLines, newLines, lcs, 3)
	if len(hunks) == 0 {
		return ""
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n", label)
	fmt.Fprintf(&b, "+++ b/%s\n", label)
	for _, h := range hunks {
		b.WriteString(h)
	}
	return b.String()
}

// --- Public API (unchanged signatures) ---

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

	result := UnifiedDiff(stripToRelative(path), data, after)
	if result == "" {
		return "(no changes)\n", nil
	}
	return result, nil
}

// DiffPreviewContent generates a unified diff from old and new content bytes.
// Unlike DiffPreview, it does not read from disk — useful for --all replacements
// where the full content is already transformed in memory.
func DiffPreviewContent(path string, old, new []byte) (string, error) {
	result := UnifiedDiff(stripToRelative(path), old, new)
	if result == "" {
		return "(no changes)\n", nil
	}
	return result, nil
}

// --- Helpers ---

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

// stripToRelative converts an absolute path to a repo-relative path for diff headers.
func stripToRelative(path string) string {
	return output.Rel(path)
}
