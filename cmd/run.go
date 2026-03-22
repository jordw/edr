package cmd

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const maxRunOutput = 1 << 20 // 1MB cap for stored output

var runCmd = &cobra.Command{
	Use:   "run <command...>",
	Short: "Run a command, show only what changed since last run",
	Long: `Execute a shell command and diff output against the previous run.
First run shows full output. Subsequent runs show a sparse view:
unchanged regions collapse to [N unchanged lines], changed lines
show inline {old → new} markers. Use --full for raw output.`,
	Args:               cobra.MinimumNArgs(1),
	DisableFlagParsing: false,
	RunE: func(cmd *cobra.Command, args []string) error {
		full, _ := cmd.Flags().GetBool("full")
		reset, _ := cmd.Flags().GetBool("reset")

		// Use args directly to preserve argument boundaries.
		// strings.Join + sh -c would double-wrap and break quoting.
		root := getRoot(cmd)
		runDir := filepath.Join(root, ".edr", "run")

		// Cache key uses joined args (stable across runs)
		shellCmd := strings.Join(args, " ")

		// --reset: clear the stored baseline so this run is treated as first run
		if reset {
			key := fmt.Sprintf("%x", sha256.Sum256([]byte(shellCmd)))[:12]
			os.Remove(filepath.Join(runDir, key+".last"))
		}

		c := exec.Command(args[0], args[1:]...)
		c.Dir = root
		out, execErr := c.CombinedOutput()

		if full {
			os.Stdout.Write(out)
			return exitError(execErr)
		}

		output := diffAgainstPrevious(runDir, shellCmd, string(out))
		fmt.Print(output)

		return exitError(execErr)
	},
}

func init() {
	runCmd.Flags().Bool("full", false, "Bypass diff, show full output")
	runCmd.Flags().Bool("reset", false, "Clear baseline before running (treat as first run)")
	rootCmd.AddCommand(runCmd)
}

func exitError(err error) error {
	if err == nil {
		return nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return silentError{code: exitErr.ExitCode()}
	}
	return err
}

// diffAgainstPrevious diffs current output against the stored previous run.
// First run shows full output. Identical output prints "[no changes]".
// Otherwise shows sparse output with inline diffs.
func diffAgainstPrevious(runDir, command, current string) string {
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(command)))[:12]
	lastFile := filepath.Join(runDir, key+".last")

	prev, err := os.ReadFile(lastFile)
	hasPrev := err == nil

	// Store current (truncate from front if over cap)
	store := current
	if len(store) > maxRunOutput {
		store = store[len(store)-maxRunOutput:]
	}
	os.MkdirAll(runDir, 0755)
	os.WriteFile(lastFile, []byte(store), 0644)

	if !hasPrev {
		return current
	}

	prevStr := string(prev)
	if prevStr == current {
		return "[no changes]\n"
	}

	oldLines := strings.Split(strings.TrimRight(prevStr, "\n"), "\n")
	newLines := strings.Split(strings.TrimRight(current, "\n"), "\n")
	diff, _ := sparseDiff(oldLines, newLines)
	return diff
}

// sparseDiff produces a sparse version of the new output where unchanged
// regions are collapsed and changed lines show inline {old → new} markers.
// Uses positional alignment (line N vs line N).
type diffStats struct {
	changed     int
	digitChanged int
	added       int
	removed     int
}

type diffEntry struct {
	kind      string // "same", "modified", "added", "removed"
	newText   string
	oldText   string
	digitOnly bool // true if modification is digits-only
}

func sparseDiff(oldLines, newLines []string) (string, diffStats) {
	var stats diffStats
	// Phase 1: Classify each line using LCS alignment.
	var entries []diffEntry

	lcs := computeLCS(oldLines, newLines)
	oi, ni, li := 0, 0, 0

	for oi < len(oldLines) || ni < len(newLines) {
		if li < len(lcs) && oi < len(oldLines) && ni < len(newLines) &&
			oldLines[oi] == lcs[li] && newLines[ni] == lcs[li] {
			entries = append(entries, diffEntry{kind: "same", newText: newLines[ni]})
			oi++
			ni++
			li++
			continue
		}

		// Collect non-LCS runs from both sides
		oldEnd := oi
		for oldEnd < len(oldLines) && (li >= len(lcs) || oldLines[oldEnd] != lcs[li]) {
			oldEnd++
		}
		newEnd := ni
		for newEnd < len(newLines) && (li >= len(lcs) || newLines[newEnd] != lcs[li]) {
			newEnd++
		}

		// Pair modifications
		for oi < oldEnd && ni < newEnd {
			entries = append(entries, diffEntry{
				kind:      "modified",
				newText:   newLines[ni],
				oldText:   oldLines[oi],
				digitOnly: isDigitOnlyChange(oldLines[oi], newLines[ni]),
			})
			oi++
			ni++
		}
		for oi < oldEnd {
			entries = append(entries, diffEntry{kind: "removed", oldText: oldLines[oi]})
			oi++
		}
		for ni < newEnd {
			entries = append(entries, diffEntry{kind: "added", newText: newLines[ni]})
			ni++
		}
	}

	// Phase 2: Merge small unchanged gaps into adjacent collapsed regions.
	// A "same" run of ≤2 lines between changed lines is too small to be useful
	// context — absorb it so repeated warnings don't leak through individually.
	merged := mergeSmallGaps(entries)

	// Phase 3: Render with collapsing.
	// Merge adjacent "same" and "digit-only modified" into one summary.
	var result strings.Builder
	i := 0
	for i < len(merged) {
		e := merged[i]

		// Collapsible: unchanged or digit-only modified
		if e.kind == "same" || (e.kind == "modified" && e.digitOnly) {
			total := 0
			numChanged := 0
			for i < len(merged) {
				ei := merged[i]
				if ei.kind == "same" {
					total++
					i++
				} else if ei.kind == "modified" && ei.digitOnly {
					total++
					numChanged++
					i++
				} else {
					break
				}
			}
			stats.digitChanged += numChanged
			if numChanged == 0 {
				fmt.Fprintf(&result, "[%d unchanged lines]\n", total)
			} else {
				fmt.Fprintf(&result, "[%d lines, %d with numbers changed]\n", total, numChanged)
			}
			continue
		}

		switch e.kind {
		case "modified":
			stats.changed++
			result.WriteString(inlineDiff(e.oldText, e.newText))
			result.WriteByte('\n')
			i++

		case "added":
			stats.added++
			fmt.Fprintf(&result, "{+ %s}\n", e.newText)
			i++

		case "removed":
			stats.removed++
			fmt.Fprintf(&result, "{- %s}\n", e.oldText)
			i++
		}
	}

	return result.String(), stats
}

// mergeSmallGaps absorbs small runs of "same" lines (≤2) that sit between
// changed lines. This prevents repeated unchanged output (e.g., compiler
// warnings) from leaking through as individual [1 unchanged lines] entries
// when interleaved with changing output.
func mergeSmallGaps(entries []diffEntry) []diffEntry {
	if len(entries) == 0 {
		return entries
	}

	// Identify runs: for each entry, record if it's part of a "same" run
	// that is ≤2 lines and has non-same entries on both sides.
	out := make([]diffEntry, 0, len(entries))
	i := 0
	for i < len(entries) {
		if entries[i].kind != "same" && !(entries[i].kind == "modified" && entries[i].digitOnly) {
			out = append(out, entries[i])
			i++
			continue
		}

		// Count the collapsible run length
		runStart := i
		for i < len(entries) && (entries[i].kind == "same" || (entries[i].kind == "modified" && entries[i].digitOnly)) {
			i++
		}
		runLen := i - runStart

		// If ≤2 lines AND surrounded by changes on both sides, absorb into collapse
		hasBefore := runStart > 0
		hasAfter := i < len(entries)
		if runLen <= 2 && hasBefore && hasAfter {
			// Re-tag as "same" so they merge with the next collapsed region
			for j := runStart; j < i; j++ {
				out = append(out, diffEntry{kind: "same", newText: entries[j].newText})
			}
		} else {
			// Keep as-is
			out = append(out, entries[runStart:i]...)
		}
	}
	return out
}

// isDigitOnlyChange returns true if two strings of equal length differ
// only at byte positions where at least one side has a digit.
// isDigitOnlyChange returns true if two lines have the same non-digit
// skeleton. "test 18 in 0.003s" vs "test 9 in 0.005s" → both strip to
// "test  in .s" → digit-only change, even though lengths differ.
func isDigitOnlyChange(a, b string) bool {
	return a != b && stripDigits(a) == stripDigits(b)
}

func stripDigits(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			b = append(b, s[i])
		}
	}
	return string(b)
}

// computeLCS returns the longest common subsequence of two string slices.
func computeLCS(a, b []string) []string {
	m, n := len(a), len(b)
	if m == 0 || n == 0 {
		return nil
	}

	// For very large outputs, skip LCS (O(m*n) memory)
	if m*n > 10_000_000 {
		return nil
	}

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}
	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			if a[i-1] == b[j-1] {
				dp[i][j] = dp[i-1][j-1] + 1
			} else if dp[i-1][j] > dp[i][j-1] {
				dp[i][j] = dp[i-1][j]
			} else {
				dp[i][j] = dp[i][j-1]
			}
		}
	}

	result := make([]string, 0, dp[m][n])
	i, j := m, n
	for i > 0 && j > 0 {
		if a[i-1] == b[j-1] {
			result = append(result, a[i-1])
			i--
			j--
		} else if dp[i-1][j] > dp[i][j-1] {
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

// inlineDiff produces a line with {old → new} markers for changed segments.
// Unchanged prefix and suffix are kept as-is.
func inlineDiff(old, new string) string {
	// Find common prefix
	pfx := 0
	for pfx < len(old) && pfx < len(new) && old[pfx] == new[pfx] {
		pfx++
	}
	// Find common suffix
	sfx := 0
	for sfx < len(old)-pfx && sfx < len(new)-pfx && old[len(old)-1-sfx] == new[len(new)-1-sfx] {
		sfx++
	}

	oldMid := old[pfx : len(old)-sfx]
	newMid := new[pfx : len(new)-sfx]

	if oldMid == "" && newMid == "" {
		return new
	}

	var b strings.Builder
	b.WriteString(new[:pfx])
	b.WriteString("{")
	b.WriteString(oldMid)
	b.WriteString(" → ")
	b.WriteString(newMid)
	b.WriteString("}")
	b.WriteString(new[len(new)-sfx:])
	return b.String()
}
