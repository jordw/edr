package dispatch

import (
	"context"
	"fmt"
	"strings"

	"github.com/jordw/edr/internal/index"
)

// NotFoundError is a structured error returned when old_text doesn't match.
// It implements error for Go error chains and is detected by asNotFoundError
// in the batch handler to produce structured JSON output.
type NotFoundError struct {
	ErrorType  string         `json:"error"`
	File       string         `json:"file"`
	OldText    string         `json:"old_text"`
	Hint       string         `json:"hint"`
	NearMatch  *nearMatchInfo `json:"near_match,omitempty"`
	NextAction string         `json:"next_action,omitempty"`
	EditsAgo   int            `json:"edits_ago,omitempty"`
}

type nearMatchInfo struct {
	Line       int    `json:"line"`
	Kind       string `json:"kind"` // "whitespace", "indentation", "partial"
	ActualText string `json:"actual_text,omitempty"`
}

func (e *NotFoundError) Error() string {
	msg := fmt.Sprintf("old_text not found in %s", e.File)
	if e.NearMatch != nil {
		msg += fmt.Sprintf(" (%s near line %d)", e.NearMatch.Kind, e.NearMatch.Line)
	}
	return msg
}

// notFoundError builds a NotFoundError with diagnostic hints.
func notFoundError(content, relFile, matchText string) *NotFoundError {
	nfe := &NotFoundError{
		ErrorType: "not_found",
		File:      relFile,
		OldText:   matchText,
		Hint:      "file may have changed since last read — re-read before editing",
	}

	// Truncate old_text in the struct for JSON output
	if len(nfe.OldText) > 200 {
		nfe.OldText = nfe.OldText[:200] + "..."
	}

	// 1. Check whitespace-normalized match (tabs vs spaces, trailing spaces, etc.)
	normContent := normalizeWhitespace(content)
	normMatch := normalizeWhitespace(matchText)
	if idx := strings.Index(normContent, normMatch); idx >= 0 {
		line := 1 + strings.Count(content[:findOriginalOffset(content, normContent, idx)], "\n")
		nfe.Hint = "old_text matches after normalizing whitespace — retry with --fuzzy to apply, or pass exact content via --old-text @path"
		nfe.NearMatch = &nearMatchInfo{Line: line, Kind: "whitespace"}
		nfe.NextAction = fmt.Sprintf("retry with --fuzzy, or re-read %s and copy exact whitespace", relFile)
		return nfe
	}

	// 2. Check if old_text matches after trimming leading/trailing whitespace from each line
	trimmedMatch := trimLines(matchText)
	trimmedContent := trimLines(content)
	if idx := strings.Index(trimmedContent, trimmedMatch); idx >= 0 {
		origOff := findOriginalOffset(content, trimmedContent, idx)
		line := 1 + strings.Count(content[:origOff], "\n")
		nfe.Hint = "old_text matches after trimming indentation — retry with --fuzzy to apply, or check leading whitespace on each line"
		nfe.NearMatch = &nearMatchInfo{Line: line, Kind: "indentation"}
		nfe.NextAction = fmt.Sprintf("retry with --fuzzy, or re-read %s and copy exact indentation", relFile)
		return nfe
	}

	// 3. Find best partial match — first line of old_text
	firstLine := matchText
	if nl := strings.Index(matchText, "\n"); nl >= 0 {
		firstLine = matchText[:nl]
	}
	firstLine = strings.TrimSpace(firstLine)
	if firstLine != "" && len(firstLine) > 5 {
		if idx := strings.Index(content, firstLine); idx >= 0 {
			line := 1 + strings.Count(content[:idx], "\n")
			lineStart := strings.LastIndex(content[:idx], "\n") + 1
			lineEnd := strings.Index(content[idx:], "\n")
			if lineEnd < 0 {
				lineEnd = len(content) - idx
			}
			actualLine := content[lineStart : idx+lineEnd]
			if len(actualLine) > 120 {
				actualLine = actualLine[:120] + "..."
			}
			nfe.Hint = "first line of old_text found but full match failed — content may have diverged"
			nfe.NearMatch = &nearMatchInfo{Line: line, Kind: "partial", ActualText: actualLine}
			nfe.NextAction = fmt.Sprintf("re-read %s to get current content, then retry with updated old_text", relFile)
			return nfe
		}
	}

	// 4. Fuzzy similarity: find the file segment most similar to matchText's
	// first non-trivial line, by character bigram overlap. Catches typos like
	// "reocrding" → "recording" that branch 3 misses (no exact substring).
	// Short queries (< 30 chars) compare against tokens; longer queries compare
	// against whole lines, so we don't penalize length mismatch in either case.
	if firstLine != "" && len(firstLine) >= 5 {
		bestLine, bestText, bestScore := -1, "", 0.0
		if !strings.ContainsAny(firstLine, " \t") {
			// Single-token query (no whitespace) — compare against file tokens to
			// avoid length-mismatch dragging the score down.
			pos := 0
			for pos < len(content) {
				// Skip whitespace
				for pos < len(content) && (content[pos] == ' ' || content[pos] == '\t' || content[pos] == '\n') {
					pos++
				}
				start := pos
				for pos < len(content) && content[pos] != ' ' && content[pos] != '\t' && content[pos] != '\n' {
					pos++
				}
				if pos-start < 3 {
					continue
				}
				token := content[start:pos]
				score := bigramSimilarity(firstLine, token)
				if score > bestScore {
					bestScore = score
					bestLine = 1 + strings.Count(content[:start], "\n")
					ls := strings.LastIndex(content[:start], "\n") + 1
					le := start + strings.Index(content[start:], "\n")
					if le < start {
						le = len(content)
					}
					bestText = content[ls:le]
				}
			}
		} else {
			// Line-level: compare against each non-trivial line.
			offset := 0
			for _, line := range strings.Split(content, "\n") {
				trimmed := strings.TrimSpace(line)
				if len(trimmed) >= 3 {
					score := bigramSimilarity(firstLine, trimmed)
					if score > bestScore {
						bestScore = score
						bestLine = 1 + strings.Count(content[:offset], "\n")
						bestText = line
					}
				}
				offset += len(line) + 1
			}
		}
		if bestScore >= 0.6 {
			if len(bestText) > 120 {
				bestText = bestText[:120] + "..."
			}
			nfe.Hint = fmt.Sprintf("near-match (%.0f%% similar) — your old_text differs from the file at line %d; check for typos", bestScore*100, bestLine)
			nfe.NearMatch = &nearMatchInfo{Line: bestLine, Kind: "fuzzy", ActualText: bestText}
			nfe.NextAction = fmt.Sprintf("compare your old_text to line %d of %s and fix the difference (often a typo or stale copy)", bestLine, relFile)
			return nfe
		}
	}

	nfe.NextAction = fmt.Sprintf("re-read %s to get current content", relFile)
	return nfe
}

// bigramSimilarity returns a 0..1 score based on character-bigram overlap
// between a and b (Sørensen–Dice coefficient on bigram multisets). Cheap to
// compute and selective enough to catch typos without false positives.
func bigramSimilarity(a, b string) float64 {
	if len(a) < 2 || len(b) < 2 {
		return 0
	}
	bgA := make(map[string]int, len(a))
	for i := 0; i < len(a)-1; i++ {
		bgA[a[i:i+2]]++
	}
	bgB := make(map[string]int, len(b))
	for i := 0; i < len(b)-1; i++ {
		bgB[b[i:i+2]]++
	}
	common := 0
	for bg, ca := range bgA {
		if cb, ok := bgB[bg]; ok {
			if ca < cb {
				common += ca
			} else {
				common += cb
			}
		}
	}
	return 2.0 * float64(common) / float64(len(a)+len(b)-2)
}

// ambiguousMatchError builds an error with line numbers for all match locations.
func ambiguousMatchError(ctx context.Context, db index.SymbolStore, file, content, relFile, matchText string, locs [][]int) error {
	lines := make([]int, 0, len(locs))
	for _, loc := range locs {
		line := 1 + strings.Count(content[:loc[0]], "\n")
		lines = append(lines, line)
	}
	lineStrs := make([]string, len(lines))
	for i, l := range lines {
		lineStrs[i] = fmt.Sprintf("%d", l)
	}

	// Map each match to its enclosing symbol so we can suggest --in <symbol>
	// when matches span multiple symbols. Prefer the smallest-spanning symbol
	// that contains the line; ignore container types (struct/class/etc.) so
	// we land on the function or method an agent can scope by name.
	containerHint := ""
	if db != nil {
		syms, err := db.GetSymbolsByFile(ctx, file)
		if err == nil && len(syms) > 0 {
			seen := map[string]struct{}{}
			var names []string
			for _, l := range lines {
				var best *index.SymbolInfo
				for i := range syms {
					s := &syms[i]
					if s.Name == "" {
						continue
					}
					if int(s.StartLine) <= l && l <= int(s.EndLine) {
						if best == nil || (s.EndLine-s.StartLine) < (best.EndLine-best.StartLine) {
							best = s
						}
					}
				}
				if best == nil {
					continue
				}
				if _, ok := seen[best.Name]; ok {
					continue
				}
				seen[best.Name] = struct{}{}
				names = append(names, best.Name)
			}
			if len(names) >= 2 {
				containerHint = fmt.Sprintf("; matches span symbols %v — scope with --in <symbol> to pick one", names)
			} else if len(names) == 1 {
				containerHint = fmt.Sprintf("; all matches are inside %s — add surrounding context to disambiguate", names[0])
			}
		}
	}

	return fmt.Errorf("ambiguous: old_text %q matched %d locations in %s (lines %s); provide more surrounding context to make it unique, or use all: true to replace all%s",
		matchText, len(locs), relFile, strings.Join(lineStrs, ", "), containerHint)
}
