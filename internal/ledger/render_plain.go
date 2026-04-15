package ledger

import (
	"fmt"
	"strings"
)

// Default sampling caps per tier for plain rendering. Agents reading JSON
// always get the full site list; these apply only to the plain renderer.
const (
	actionTierMaxSamples = 100 // definite / ambiguous-*
	debugTierMaxSamples  = 10  // shadowed / lexical-noise
)

// RenderPlainOpts carries renderer options. ExpandTiers[tier]=true shows
// every site for that tier, overriding the default sampling cap.
type RenderPlainOpts struct {
	ExpandTiers map[Tier]bool
}

// RenderPlain formats a Ledger as a human-readable text block. It also
// populates l.Render (ShownCounts, Truncated) as a side effect so the JSON
// header can reflect what was rendered.
func RenderPlain(l *Ledger, opts RenderPlainOpts) string {
	if l.Render == nil {
		l.Render = &RenderHints{}
	}
	l.Render.ShownCounts = make(map[Tier]int)
	l.Render.Truncated = make(map[Tier]int)

	var b strings.Builder

	writeHeader(&b, l)
	writeCounts(&b, l)
	writeFilterLine(&b, l)
	if l.Command == CommandRename && l.Rename != nil && l.Rename.Applied != nil {
		writeAppliedBlock(&b, l)
		return b.String()
	}
	writeNextActions(&b, l)

	for _, tier := range TierOrder {
		if l.Counts[tier] == 0 {
			continue
		}
		writeTierSection(&b, l, tier, opts)
	}

	return b.String()
}

func writeHeader(b *strings.Builder, l *Ledger) {
	fmt.Fprintf(b, "Target: %s @ %s:%d (%s)\n", l.Target.Name, l.Target.File, l.Target.Line, l.Target.Kind)
	fmt.Fprintf(b, "Scope:  %s\n", l.Scope)
	if l.Command == CommandRename && l.Rename != nil {
		fmt.Fprintf(b, "Rename: %s → %s\n", l.Rename.From, l.Rename.To)
	}
	b.WriteByte('\n')
}

func writeCounts(b *strings.Builder, l *Ledger) {
	widest := 0
	for _, t := range TierOrder {
		if len(string(t)) > widest {
			widest = len(string(t))
		}
	}
	for _, t := range TierOrder {
		n := l.Counts[t]
		pad := strings.Repeat(" ", widest-len(string(t))+1)
		fmt.Fprintf(b, "%s%s%5d", t, pad, n)
		if l.Render != nil && l.Render.Truncated[t] > 0 {
			fmt.Fprintf(b, "  (%d shown; --expand %s)", l.Render.ShownCounts[t], t)
		}
		b.WriteByte('\n')
	}
	b.WriteByte('\n')
}

func writeFilterLine(b *strings.Builder, l *Ledger) {
	if l.Filter == nil || len(l.Filter.Predicates) == 0 {
		return
	}
	var terms []string
	for _, p := range l.Filter.Predicates {
		op := "="
		if p.Op == "suffix" {
			op = "~="
		}
		terms = append(terms, fmt.Sprintf("%s%s%s", p.Field, op, p.Value))
	}
	total := 0
	for _, n := range l.ExcludedCounts {
		total += n
	}
	fmt.Fprintf(b, "Filter: %s  (excluded %d)\n\n", strings.Join(terms, " "), total)
}

func writeNextActions(b *strings.Builder, l *Ledger) {
	if len(l.NextActions) == 0 {
		return
	}
	b.WriteString("next:\n")
	letters := "abcdefghijklmnop"
	for i, a := range l.NextActions {
		letter := "?"
		if i < len(letters) {
			letter = string(letters[i])
		}
		fmt.Fprintf(b, "  [%s] %-36s %s\n", letter, a.Label, strings.Join(a.Cmd, " "))
	}
	b.WriteByte('\n')
}

func writeTierSection(b *strings.Builder, l *Ledger, tier Tier, opts RenderPlainOpts) {
	total := l.Counts[tier]
	max := sampleCapFor(tier)
	if opts.ExpandTiers[tier] {
		max = total
	}
	shown := 0
	label := string(tier)
	header := fmt.Sprintf("─── %s (%d)", label, total)
	if max < total {
		header = fmt.Sprintf("─── %s (sample of %d)", label, total)
	}
	fmt.Fprintf(b, "%s ───\n", header)

	for i := range l.Sites {
		if l.Sites[i].Tier != tier {
			continue
		}
		if shown >= max {
			break
		}
		writeSite(b, l, &l.Sites[i])
		shown++
	}
	l.Render.ShownCounts[tier] = shown
	if total > shown {
		l.Render.Truncated[tier] = total - shown
		fmt.Fprintf(b, "  ... (%d more; --expand %s)\n", total-shown, tier)
	}
	b.WriteByte('\n')
}

func writeSite(b *strings.Builder, l *Ledger, s *Site) {
	container := formatContainer(s.Container)
	containerPart := ""
	if container != "" {
		containerPart = fmt.Sprintf("  [%s]", container)
	}
	shortKey := s.SiteKey
	if len(shortKey) > 12 {
		shortKey = shortKey[:12]
	}
	fmt.Fprintf(b, "  [%s]  sk:%s  %s:%d  %s%s\n",
		s.ShortID, shortKey, s.File, s.Line, s.Role, containerPart)

	// Inline diff for rename sites with edits.
	if l.Command == CommandRename && l.Rename != nil {
		if ed, ok := l.Rename.Edits[s.SiteKey]; ok && showInlineDiff(s.Tier) {
			// Replace OldBytes with Replacement within the site's snippet line.
			// For a clean single-line rename this renders one `-` and one `+`.
			renderInlineDiff(b, s, ed)
		}
	}

	// For debug tiers, surface the reason.
	if !isActionTier(s.Tier) && s.Reason != "" {
		fmt.Fprintf(b, "       reason: %s — %s\n", s.ReasonCode, s.Reason)
	}
}

func renderInlineDiff(b *strings.Builder, s *Site, ed Edit) {
	if len(s.Snippet.Lines) == 0 {
		fmt.Fprintf(b, "       - %s\n       + %s\n", ed.OldBytes, ed.Replacement)
		return
	}
	for _, line := range s.Snippet.Lines {
		if strings.Contains(line, ed.OldBytes) {
			fmt.Fprintf(b, "       - %s\n", line)
			fmt.Fprintf(b, "       + %s\n", strings.Replace(line, ed.OldBytes, ed.Replacement, 1))
			return
		}
	}
	// Fallback if the snippet doesn't include the old bytes literally.
	fmt.Fprintf(b, "       - %s\n       + %s\n", ed.OldBytes, ed.Replacement)
}

func writeAppliedBlock(b *strings.Builder, l *Ledger) {
	r := l.Rename.Applied
	if r == nil {
		return
	}
	fmt.Fprintf(b, "Applied: %d sites in %d file(s)\n", len(r.Keys), len(r.Files))
	for i := range l.Sites {
		if containsStr(r.Keys, l.Sites[i].SiteKey) {
			mark := "✓"
			if !r.OK {
				mark = "!"
			}
			fmt.Fprintf(b, "  [%s]  %s:%d   %s\n", l.Sites[i].ShortID, l.Sites[i].File, l.Sites[i].Line, mark)
		}
	}
	b.WriteByte('\n')
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func sampleCapFor(t Tier) int {
	if isActionTier(t) {
		return actionTierMaxSamples
	}
	return debugTierMaxSamples
}

func isActionTier(t Tier) bool {
	return t == TierDefinite || t == TierAmbiguousDispatch || t == TierAmbiguousImport
}

func showInlineDiff(t Tier) bool {
	return isActionTier(t)
}
