package dispatch

import (
	"strings"
	"testing"

	"github.com/jordw/edr/internal/index"
)

func TestRankCandidates_PeripheralPenalty(t *testing.T) {
	root := "/repo"
	candidates := []index.SymbolInfo{
		{Name: "open", Type: "function", File: "/repo/drivers/tty/serial.c", StartLine: 50, EndLine: 80},
		{Name: "open", Type: "function", File: "/repo/fs/open.c", StartLine: 100, EndLine: 250},
		{Name: "open", Type: "function", File: "/repo/plugins/auth/handler.go", StartLine: 10, EndLine: 20},
	}
	ranked := rankCandidates(candidates, "open", root)
	if len(ranked) < 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ranked))
	}
	// fs/open.c should rank first: shallow + large span, no penalties
	if ranked[0].Rel != "fs/open.c" {
		t.Errorf("expected fs/open.c first, got %s", ranked[0].Rel)
	}
	// Both peripheral paths should be penalized below fs/open.c
	for _, r := range ranked[1:] {
		if r.Score >= ranked[0].Score {
			t.Errorf("%s (score %d) should rank below %s (score %d)", r.Rel, r.Score, ranked[0].Rel, ranked[0].Score)
		}
	}
}

func TestRankCandidates_DepthGradient(t *testing.T) {
	root := "/repo"
	candidates := []index.SymbolInfo{
		{Name: "init", Type: "function", File: "/repo/init.c", StartLine: 1, EndLine: 50},
		{Name: "init", Type: "function", File: "/repo/a/b/init.c", StartLine: 1, EndLine: 50},
		{Name: "init", Type: "function", File: "/repo/a/b/c/d/e/init.c", StartLine: 1, EndLine: 50},
	}
	ranked := rankCandidates(candidates, "init", root)
	if len(ranked) < 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ranked))
	}
	// Shallowest should rank first
	if ranked[0].Rel != "init.c" {
		t.Errorf("expected init.c first, got %s", ranked[0].Rel)
	}
	// Deepest should rank last
	if ranked[2].Rel != "a/b/c/d/e/init.c" {
		t.Errorf("expected a/b/c/d/e/init.c last, got %s", ranked[2].Rel)
	}
}

func TestRankCandidates_SpanGradient(t *testing.T) {
	root := "/repo"
	candidates := []index.SymbolInfo{
		{Name: "Config", Type: "struct", File: "/repo/src/config.go", StartLine: 10, EndLine: 12},  // 2-line stub
		{Name: "Config", Type: "struct", File: "/repo/lib/config.go", StartLine: 10, EndLine: 50},  // 40-line struct
		{Name: "Config", Type: "struct", File: "/repo/pkg/config.go", StartLine: 10, EndLine: 200}, // 190-line struct
	}
	ranked := rankCandidates(candidates, "Config", root)
	if len(ranked) < 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ranked))
	}
	// Largest span should rank first
	if ranked[0].Rel != "pkg/config.go" {
		t.Errorf("expected pkg/config.go first, got %s (score %d)", ranked[0].Rel, ranked[0].Score)
	}
}

func TestIsPeripheralPath(t *testing.T) {
	yes := []string{"drivers/tty/serial.c", "plugins/auth/main.go", "extensions/foo.rs", "contrib/bar.py", "addons/baz.js", "adapters/db.go", "connectors/api.ts", "integrations/slack.py"}
	no := []string{"src/main.go", "lib/config.go", "include/header.h", "kernel/sched/core.c", "cmd/root.go", "modules/auth.js"}

	for _, p := range yes {
		if !isPeripheralPath(p) {
			t.Errorf("expected peripheral: %s", p)
		}
	}
	for _, p := range no {
		if isPeripheralPath(p) {
			t.Errorf("should not be peripheral: %s", p)
		}
	}
}

func TestRankCandidates_ToolsPenalty(t *testing.T) {
	root := "/repo"
	candidates := []index.SymbolInfo{
		{Name: "open", Type: "method", File: "/repo/tools/lib/python/feat/parse.py", StartLine: 63, EndLine: 80},
		{Name: "open", Type: "function", File: "/repo/fs/open.c", StartLine: 100, EndLine: 200},
		{Name: "open", Type: "function", File: "/repo/drivers/tty/serial.c", StartLine: 50, EndLine: 80},
	}
	ranked := rankCandidates(candidates, "open", root)
	if len(ranked) < 3 {
		t.Fatalf("expected 3 candidates, got %d", len(ranked))
	}
	// tools/ should NOT be #1 — fs/open.c should beat it
	if ranked[0].Rel == "tools/lib/python/feat/parse.py" {
		t.Errorf("tools/ path should not rank first; got %s at #1", ranked[0].Rel)
	}
	// Find the tools candidate and verify it's penalized
	for _, r := range ranked {
		if r.Rel == "tools/lib/python/feat/parse.py" {
			if r.Score >= ranked[0].Score {
				t.Errorf("tools/ path (score %d) should rank below #1 %s (score %d)", r.Score, ranked[0].Rel, ranked[0].Score)
			}
			return
		}
	}
	t.Error("tools/ candidate not found in results")
}

func TestRankCandidates_MinorityLanguage(t *testing.T) {
	root := "/repo"
	// 8 C files + 2 Rust files — Rust should be penalized
	candidates := []index.SymbolInfo{
		{Name: "probe", Type: "function", File: "/repo/rust/kernel/uaccess.rs", StartLine: 316, EndLine: 330},
		{Name: "probe", Type: "function", File: "/repo/rust/kernel/page.rs", StartLine: 310, EndLine: 325},
		{Name: "probe", Type: "function", File: "/repo/drivers/a.c", StartLine: 50, EndLine: 100},
		{Name: "probe", Type: "function", File: "/repo/drivers/b.c", StartLine: 50, EndLine: 100},
		{Name: "probe", Type: "function", File: "/repo/drivers/c.c", StartLine: 50, EndLine: 100},
		{Name: "probe", Type: "function", File: "/repo/drivers/d.c", StartLine: 50, EndLine: 100},
		{Name: "probe", Type: "function", File: "/repo/drivers/e.c", StartLine: 50, EndLine: 100},
		{Name: "probe", Type: "function", File: "/repo/drivers/f.c", StartLine: 50, EndLine: 100},
		{Name: "probe", Type: "function", File: "/repo/drivers/g.c", StartLine: 50, EndLine: 100},
		{Name: "probe", Type: "function", File: "/repo/drivers/h.c", StartLine: 50, EndLine: 100},
	}
	ranked := rankCandidates(candidates, "probe", root)
	// Find Rust candidates and verify they're penalized vs C
	for _, r := range ranked {
		if strings.HasSuffix(r.Rel, ".rs") {
			// Rust candidates should have the minority penalty applied
			for _, c := range ranked {
				if strings.HasSuffix(c.Rel, ".c") && c.Score > r.Score {
					return // found a C file ranking above Rust — pass
				}
			}
			t.Error("no .c file ranks above .rs files despite .c being >70% of candidates")
			return
		}
	}
}

func TestRankCandidates_PeripheralMajority(t *testing.T) {
	root := "/repo"
	// When >50% are from drivers/, the penalty should be reduced
	candidates := []index.SymbolInfo{
		{Name: "probe", Type: "function", File: "/repo/core/probe.c", StartLine: 10, EndLine: 50},
		{Name: "probe", Type: "function", File: "/repo/drivers/a/probe.c", StartLine: 10, EndLine: 50},
		{Name: "probe", Type: "function", File: "/repo/drivers/b/probe.c", StartLine: 10, EndLine: 50},
		{Name: "probe", Type: "function", File: "/repo/drivers/c/probe.c", StartLine: 10, EndLine: 50},
	}
	ranked := rankCandidates(candidates, "probe", root)
	if len(ranked) < 4 {
		t.Fatalf("expected 4 candidates, got %d", len(ranked))
	}
	// core/probe.c should still be #1 (core path boost)
	if ranked[0].Rel != "core/probe.c" {
		t.Errorf("expected core/probe.c first, got %s", ranked[0].Rel)
	}
	// But drivers/ candidates should not be crushed — their effective
	// penalty is only -5 (base -15 + majority recovery +10)
	driverScore := 0
	for _, r := range ranked {
		if strings.HasPrefix(r.Rel, "drivers/") {
			driverScore = r.Score
			break
		}
	}
	gap := ranked[0].Score - driverScore
	if gap > 25 {
		t.Errorf("peripheral majority recovery should limit the gap; core=%d driver=%d gap=%d", ranked[0].Score, driverScore, gap)
	}
}

func TestIsToolsPath(t *testing.T) {
	yes := []string{"tools/lib/main.py", "tool/gen.go", "util/helpers.js", "utils/format.ts", "hack/verify.sh", "misc/debug.c"}
	no := []string{"src/tools/util.ts", "lib/util.go", "cmd/tool.go"}

	for _, p := range yes {
		if !isToolsPath(p) {
			t.Errorf("expected tools path: %s", p)
		}
	}
	for _, p := range no {
		if isToolsPath(p) {
			t.Errorf("should not be tools path: %s", p)
		}
	}
}

func TestIsCoreInfraPath(t *testing.T) {
	yes := []string{"kernel/sched/core.c", "core/main.go", "internal/dispatch/handler.go", "pkg/api/server.go", "src/main.ts", "lib/config.go", "fs/open.c", "net/socket.c"}
	no := []string{"drivers/tty/serial.c", "tools/perf/main.c", "test/unit_test.go", "vendor/lib.go"}

	for _, p := range yes {
		if !isCoreInfraPath(p) {
			t.Errorf("expected core infra path: %s", p)
		}
	}
	for _, p := range no {
		if isCoreInfraPath(p) {
			t.Errorf("should not be core infra path: %s", p)
		}
	}
}

func TestIsScriptsPath(t *testing.T) {
	yes := []string{"scripts/build.sh", "build/lib/main.ts", "ci/pipeline.yml", "hack/verify.sh", "deploy/k8s.yaml", "script/bootstrap"}
	no := []string{"src/scripts/util.ts", "lib/build.go", "cmd/deploy.go"}

	for _, p := range yes {
		if !isScriptsPath(p) {
			t.Errorf("expected scripts path: %s", p)
		}
	}
	for _, p := range no {
		if isScriptsPath(p) {
			t.Errorf("should not be scripts path: %s", p)
		}
	}
}
