package ledger

import (
	"fmt"
	"path"
	"strings"
)

// BuildNextActions populates l.NextActions per the documented generation rules.
// Actions are emitted in order: apply, refine, expand, rescope.
// rename ledgers may emit all four; find-refs ledgers emit only expand + rescope.
func (l *Ledger) BuildNextActions(baseCmd []string) {
	var actions []Action

	isRename := l.Command == CommandRename

	if isRename && l.Counts[TierDefinite] > 0 {
		actions = append(actions, Action{
			Label:   fmt.Sprintf("apply safe (%d sites)", l.Counts[TierDefinite]),
			Kind:    "apply",
			Cmd:     withFlag(baseCmd, "--apply-tiers", "definite"),
			Applies: l.Counts[TierDefinite],
		})
	}

	if isRename && (l.Counts[TierAmbiguousDispatch] > 0 || l.Counts[TierAmbiguousImport] > 0) {
		if prefix := commonPathPrefix(l.Sites, TierAmbiguousDispatch, TierAmbiguousImport); prefix != "" {
			actions = append(actions, Action{
				Label: "narrow to a subdir",
				Kind:  "refine",
				Cmd:   withFlag(baseCmd, "--filter", "file="+prefix+"/**"),
			})
		}
	}

	if l.Render != nil {
		for _, t := range TierOrder {
			if l.Render.Truncated[t] > 0 {
				actions = append(actions, Action{
					Label: fmt.Sprintf("show all %s (%d more)", t, l.Render.Truncated[t]),
					Kind:  "expand",
					Cmd:   withFlag(baseCmd, "--expand", string(t)),
				})
			}
		}
	}

	if l.Scope == ScopeSameFile && l.Total == 0 {
		actions = append(actions, Action{
			Label: "retry with cross-file scope",
			Kind:  "rescope",
			Cmd:   withFlag(baseCmd, "--cross-file"),
		})
	}

	l.NextActions = actions
}

// withFlag appends flag args to a base argv without mutating it.
func withFlag(base []string, args ...string) []string {
	out := make([]string, 0, len(base)+len(args))
	out = append(out, base...)
	out = append(out, args...)
	return out
}

// commonPathPrefix returns the longest common directory prefix (up to one
// level above the deepest shared segment) across sites in the given tiers.
// Returns "" if sites span more than one top-level directory.
func commonPathPrefix(sites []Site, tiers ...Tier) string {
	want := make(map[Tier]struct{}, len(tiers))
	for _, t := range tiers {
		want[t] = struct{}{}
	}
	var first string
	found := false
	for _, s := range sites {
		if _, ok := want[s.Tier]; !ok {
			continue
		}
		if !found {
			first = path.Dir(s.File)
			found = true
			continue
		}
		first = commonDirPrefix(first, path.Dir(s.File))
		if first == "" || first == "." {
			return ""
		}
	}
	if !found || first == "." {
		return ""
	}
	return first
}

func commonDirPrefix(a, b string) string {
	aSegs := strings.Split(a, "/")
	bSegs := strings.Split(b, "/")
	n := len(aSegs)
	if len(bSegs) < n {
		n = len(bSegs)
	}
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if aSegs[i] != bSegs[i] {
			break
		}
		out = append(out, aSegs[i])
	}
	return strings.Join(out, "/")
}
