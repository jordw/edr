package ledger

import (
	"fmt"
	"testing"
)

// TestRenderPlain_RealisticKernelInit builds a ledger mimicking the
// "rename init in the Linux kernel" scenario from the stress test:
// ~12 definite, ~20 shadowed, ~30 lexical-noise, 0 ambiguous.
// Prints the full plain render for visual review.
func TestRenderPlain_RealisticKernelInit(t *testing.T) {
	var sites []Site
	edits := map[string]Edit{}

	// 1 definition site
	add := func(file string, line, start, end int, tier Tier, role Role, rc ReasonCode, container string, snippet string, reason string) {
		s := Site{
			File:       file,
			ByteRange:  [2]int{start, end},
			Line:       line,
			Col:        1,
			Tier:       tier,
			Role:       role,
			ReasonCode: rc,
			Reason:     reason,
			Snippet:    Snippet{Lines: []string{snippet}},
		}
		if container != "" {
			s.Container = []ContainerStep{{Kind: "function", Name: container}}
		}
		s.SiteKey = ComputeSiteKey(file, start, end, tier, []byte("init"))
		sites = append(sites, s)
		if IsEditableTier(tier) {
			edits[s.SiteKey] = Edit{OldBytes: "init", Replacement: "init_v2"}
		}
	}

	// definite: def + call sites in same module
	add("tools/perf/bench/numa.c", 1517, 45000, 45004, TierDefinite, RoleDef, ReasonResolvedDef, "", "static int init(void) {", "")
	add("tools/perf/bench/numa.c", 523, 15200, 15204, TierDefinite, RoleCall, ReasonResolvedCall, "launch", "\tinit();", "")
	add("tools/perf/bench/numa.c", 891, 27100, 27104, TierDefinite, RoleCall, ReasonResolvedCall, "reset", "\tinit();", "")
	add("tools/perf/bench/numa.c", 1102, 33500, 33504, TierDefinite, RoleCall, ReasonResolvedCall, "main", "\tresult = init();", "")
	add("tools/perf/bench/numa.c", 322, 9800, 9804, TierDefinite, RoleDecl, ReasonResolvedDecl, "", "static int init(void);", "")

	// shadowed: other files with local `init` variables
	for i := 0; i < 20; i++ {
		file := fmt.Sprintf("kernel/sched/core%d.c", i)
		line := 100 + i*50
		offset := 5000 + i*200
		add(file, line, offset, offset+4, TierShadowed, RoleRef, ReasonScopeShadow,
			fmt.Sprintf("__schedule_%d", i),
			fmt.Sprintf("\tint init = %d;", i),
			fmt.Sprintf("local `int init = %d` declared at :%d", i, line-3))
	}

	// lexical-noise: struct field initializers, comments, strings
	noiseFiles := []struct {
		file, snippet, reason string
		role                  Role
		rc                    ReasonCode
	}{
		{"fs/nfs/super.c", ".init = nfs_init_fs_context,", "designated initializer .init = ...", RoleField, ReasonStructFieldKey},
		{"fs/ext4/super.c", ".init = ext4_init_fs_context,", "designated initializer .init = ...", RoleField, ReasonStructFieldKey},
		{"drivers/gpu/drm/i915/init.c", "/* init the display */", "block comment containing 'init'", RoleComment, ReasonInsideComment},
		{"drivers/net/ethernet/intel/e1000.c", ".init = e1000_init,", "designated initializer .init = ...", RoleField, ReasonStructFieldKey},
		{"include/linux/module.h", "// module init callback", "line comment mentioning 'init'", RoleComment, ReasonInsideComment},
		{"arch/x86/kernel/setup.c", "\"init=%s\"", "string literal containing 'init'", RoleString, ReasonInsideString},
	}
	for i, nf := range noiseFiles {
		offset := 80000 + i*300
		add(nf.file, 50+i*20, offset, offset+4, TierLexicalNoise, nf.role, nf.rc, "", nf.snippet, nf.reason)
	}
	// Pad more noise to get a larger count
	for i := 0; i < 24; i++ {
		file := fmt.Sprintf("drivers/misc/dev%d.c", i)
		offset := 90000 + i*200
		add(file, 200+i*10, offset, offset+4, TierLexicalNoise, RoleField, ReasonStructFieldKey,
			"", fmt.Sprintf(".init = dev%d_init,", i), "designated initializer .init = ...")
	}

	l := &Ledger{
		Version: SchemaVersion,
		Command: CommandRename,
		Target:  Target{Name: "init", File: "tools/perf/bench/numa.c", Line: 1517, Kind: "function", Signature: "static int init(void)"},
		Scope:   ScopeCrossFile,
		Sites:   sites,
		Rename: &RenamePayload{
			From:  "init",
			To:    "init_v2",
			Edits: edits,
		},
	}
	l.RecomputeCounts()
	l.AssignShortIDs()
	l.BuildNextActions([]string{"edr", "rename", "tools/perf/bench/numa.c:init", "--to", "init_v2"})

	if err := Validate(l); err != nil {
		t.Fatalf("validate: %v", err)
	}

	out := RenderPlain(l, RenderPlainOpts{})

	// Print the full render for visual review.
	t.Logf("\n%s", out)

	// Structural checks.
	mustContain(t, out, "Target: init @ tools/perf/bench/numa.c:1517 (function)")
	mustContain(t, out, "Rename: init → init_v2")
	mustContain(t, out, "definite")
	mustContain(t, out, "[d1]")
	mustContain(t, out, "- static int init(void) {")
	mustContain(t, out, "+ static int init_v2(void) {")
	mustContain(t, out, "shadowed")
	mustContain(t, out, "[s1]")
	mustContain(t, out, "scope-shadow")
	mustContain(t, out, "lexical-noise")
	mustContain(t, out, "[n1]")
	mustContain(t, out, "struct-field-key")
	mustContain(t, out, "apply safe (5 sites)")
	mustContain(t, out, "--expand shadowed")
	mustContain(t, out, "--expand lexical-noise")

	// Validate again after render (render populates Render hints).
	if err := Validate(l); err != nil {
		t.Fatalf("post-render validate: %v", err)
	}
}
