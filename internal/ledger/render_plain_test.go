package ledger

import (
	"strings"
	"testing"
)

func renderableRenameLedger() *Ledger {
	sites := []Site{
		{
			File:       "tools/foo.c",
			ByteRange:  [2]int{100, 104},
			Line:       5,
			Tier:       TierDefinite,
			Role:       RoleDef,
			ReasonCode: ReasonResolvedDef,
			Container: []ContainerStep{
				{Kind: "function", Name: "launch"},
			},
			Snippet: Snippet{Lines: []string{"void init(void) {"}},
		},
		{
			File:       "tools/foo.c",
			ByteRange:  [2]int{200, 204},
			Line:       40,
			Tier:       TierDefinite,
			Role:       RoleCall,
			ReasonCode: ReasonResolvedCall,
			Snippet:    Snippet{Lines: []string{"\tinit();"}},
		},
		{
			File:       "tools/bar.c",
			ByteRange:  [2]int{50, 54},
			Line:       12,
			Tier:       TierShadowed,
			Role:       RoleRef,
			ReasonCode: ReasonScopeShadow,
			Reason:     "local `int init = 0` declared at :10",
		},
	}
	// Fill SiteKeys.
	for i := range sites {
		sites[i].SiteKey = ComputeSiteKey(sites[i].File, sites[i].ByteRange[0], sites[i].ByteRange[1], sites[i].Tier, []byte("init"))
	}
	edits := map[string]Edit{}
	for _, s := range sites {
		if s.Tier == TierDefinite {
			edits[s.SiteKey] = Edit{OldBytes: "init", Replacement: "init_v2"}
		}
	}
	l := &Ledger{
		Version: SchemaVersion,
		Command: CommandRename,
		Target:  Target{Name: "init", File: "tools/foo.c", Line: 5, Kind: "function"},
		Scope:   ScopeCrossFile,
		Sites:   sites,
		Counts:  map[Tier]int{TierDefinite: 2, TierShadowed: 1},
		Total:   3,
		Rename: &RenamePayload{
			From:  "init",
			To:    "init_v2",
			Edits: edits,
		},
	}
	l.AssignShortIDs()
	l.BuildNextActions([]string{"rename", "tools/foo.c:init", "--to", "init_v2"})
	return l
}

func TestRenderPlain_Basic(t *testing.T) {
	l := renderableRenameLedger()
	out := RenderPlain(l, RenderPlainOpts{})

	mustContain(t, out, "Target: init @ tools/foo.c:5 (function)")
	mustContain(t, out, "Scope:  cross-file")
	mustContain(t, out, "Rename: init → init_v2")
	mustContain(t, out, "definite")
	mustContain(t, out, "shadowed")
	mustContain(t, out, "[d1]")
	mustContain(t, out, "[d2]")
	mustContain(t, out, "[s1]")
	mustContain(t, out, "- void init(void) {")
	mustContain(t, out, "+ void init_v2(void) {")
	mustContain(t, out, "next:")
	mustContain(t, out, "apply safe (2 sites)")
	mustContain(t, out, "reason: scope-shadow")
}

func TestRenderPlain_ValidatesCleanly(t *testing.T) {
	l := renderableRenameLedger()
	_ = RenderPlain(l, RenderPlainOpts{})
	if err := Validate(l); err != nil {
		t.Fatalf("post-render validation: %v", err)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected output to contain %q\n--- full output ---\n%s", needle, haystack)
	}
}
