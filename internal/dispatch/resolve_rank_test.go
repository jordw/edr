package dispatch

import (
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
