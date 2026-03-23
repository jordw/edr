// Context-efficiency tests comparing workflow strategies and enforcing
// response-size regression gates. These validate that edr's progressive
// disclosure features (signatures, depth, budget) produce meaningfully
// smaller responses than naive full-file reads.
//
// Workflow benchmarks: go test ./bench/ -bench BenchmarkWorkflow -benchmem
// Regression gates:    go test ./bench/ -run 'Test(ResponseSize|Signatures)' -v
package bench_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

)

// ---------------------------------------------------------------------------
// End-to-end workflow benchmarks
//
// Progressive is intentionally slower (more tree-sitter work per call) but
// returns far fewer response bytes. The tradeoff: ~15% more server time for
// ~75% fewer tokens consumed by the agent.
// ---------------------------------------------------------------------------

func BenchmarkWorkflowTraditional(b *testing.B) {
	db, tmp := setupRepo(b)
	ctx := context.Background()
	file := filepath.Join(tmp, "lib", "scheduler.py")
	original, _ := os.ReadFile(file)
	newMethod := `def drain(self, timeout: float = 5.0) -> int:
    """Drain remaining tasks."""
    return 0
`

	b.ResetTimer()
	for b.Loop() {
		out1, _ := dispatchJSON(ctx, db, "read", []string{"lib/scheduler.py:Scheduler"}, nil)
		out2, _ := dispatchJSON(ctx, db, "read", []string{"lib/scheduler.py", "_execute_task"}, nil)
		out3, _ := dispatchJSON(ctx, db, "write", []string{"lib/scheduler.py"}, map[string]any{
			"after":   "_execute_task",
			"content": newMethod,
		})
		totalBytes := len(out1) + len(out2) + len(out3)
		b.ReportMetric(float64(totalBytes), "response_bytes")
		b.ReportMetric(heapAllocKB(), "heap_alloc_kb")

		b.StopTimer()
		os.WriteFile(file, original, 0644)
		db.InvalidateFiles(ctx, []string{file})
		b.StartTimer()
	}
}

func BenchmarkWorkflowProgressive(b *testing.B) {
	db, tmp := setupRepo(b)
	ctx := context.Background()
	file := filepath.Join(tmp, "lib", "scheduler.py")
	original, _ := os.ReadFile(file)
	newMethod := `def drain(self, timeout: float = 5.0) -> int:
    """Drain remaining tasks."""
    return 0
`

	b.ResetTimer()
	for b.Loop() {
		out1, _ := dispatchJSON(ctx, db, "read", []string{"lib/scheduler.py:Scheduler"}, map[string]any{"signatures": true})
		out2, _ := dispatchJSON(ctx, db, "read", []string{"lib/scheduler.py", "_execute_task"}, map[string]any{"depth": 2})
		out3, _ := dispatchJSON(ctx, db, "write", []string{"lib/scheduler.py"}, map[string]any{
			"inside":  "Scheduler",
			"content": newMethod,
		})
		totalBytes := len(out1) + len(out2) + len(out3)
		b.ReportMetric(float64(totalBytes), "response_bytes")
		b.ReportMetric(heapAllocKB(), "heap_alloc_kb")

		b.StopTimer()
		os.WriteFile(file, original, 0644)
		db.InvalidateFiles(ctx, []string{file})
		b.StartTimer()
	}
}

// ---------------------------------------------------------------------------
// Regression test: assert response byte sizes stay within expected bounds.
// These are not benchmarks — they fail if response sizes regress.
// ---------------------------------------------------------------------------

func TestResponseSizeRegression(t *testing.T) {
	db, _ := setupRepo(t)
	ctx := context.Background()

	tests := []struct {
		name     string
		cmd      string
		args     []string
		flags    map[string]any
		maxBytes int // response must be <= this
	}{
		// --- Read gates ---
		{
			name:     "signatures < full symbol",
			cmd:      "read",
			args:     []string{"lib/scheduler.py:Scheduler"},
			flags:    map[string]any{"signatures": true},
			maxBytes: 1500, // actual ~1167B, full symbol is ~7500B
		},
		{
			name:     "depth2 < full method",
			cmd:      "read",
			args:     []string{"lib/scheduler.py", "_execute_task"},
			flags:    map[string]any{"depth": 2},
			maxBytes: 800, // actual ~555B
		},
		{
			name:     "multi-file read with budget",
			cmd:      "read",
			args:     []string{"lib/scheduler.py", "lib/TaskProcessor.java", "internal/worker.go"},
			flags:    map[string]any{"budget": 500},
			maxBytes: 5000, // actual ~4103B
		},
		{
			name:     "read line range",
			cmd:      "read",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"lines": "1:30"},
			maxBytes: 1200, // actual ~884B
		},
		// --- Search gates ---
		{
			name:     "search with budget",
			cmd:      "search",
			args:     []string{"execute"},
			flags:    map[string]any{"body": true, "budget": 500},
			maxBytes: 3000, // actual ~2222B
		},
		{
			name:     "find files",
			cmd:      "search",
			args:     []string{"**/*.py"},
			flags:    nil,
			maxBytes: 1500, // actual ~66B
		},
		{
			name:     "search in symbol",
			cmd:      "search",
			args:     []string{"running"},
			flags:    map[string]any{"text": true, "in": "lib/scheduler.py:Scheduler"},
			maxBytes: 800, // actual ~573B
		},
		// --- Map gates ---
		{
			name:     "map with budget",
			cmd:      "map",
			args:     nil,
			flags:    map[string]any{"budget": 500},
			maxBytes: 6000, // actual ~5136B
		},
		{
			name:     "map file with type filter",
			cmd:      "map",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"type": "function"},
			maxBytes: 5500, // actual ~4818B
		},
		{
			name:     "map dir filter",
			cmd:      "map",
			args:     nil,
			flags:    map[string]any{"dir": "lib", "budget": 500},
			maxBytes: 7000, // actual ~5238B
		},
		{
			name:     "map grep filter",
			cmd:      "map",
			args:     nil,
			flags:    map[string]any{"grep": "task", "budget": 500},
			maxBytes: 5500, // actual ~3992B
		},
		{
			name:     "map lang filter",
			cmd:      "map",
			args:     nil,
			flags:    map[string]any{"lang": "python", "budget": 500},
			maxBytes: 4500, // actual ~3172B
		},
		// --- Edit gates ---
		{
			name:     "edit dry-run",
			cmd:      "edit",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"old_text": "self._running = True", "new_text": "self._running = False", "dry-run": true},
			maxBytes: 1000, // actual ~794B
		},
		{
			name:     "edit fuzzy dry-run",
			cmd:      "edit",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"old_text": "self._running  =  True", "new_text": "self._running = False", "fuzzy": true, "dry-run": true},
			maxBytes: 700, // actual ~457B
		},
		{
			name:     "edit in symbol dry-run",
			cmd:      "edit",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"in": "Scheduler", "old_text": "self._running = True", "new_text": "self._running = False", "dry-run": true},
			maxBytes: 700, // actual ~457B
		},
		{
			name:     "edit delete dry-run",
			cmd:      "edit",
			args:     []string{"lib/scheduler.py:ScheduleType"},
			flags:    map[string]any{"delete": true, "dry-run": true},
			maxBytes: 600, // actual ~349B
		},
		// --- Write gates ---
		{
			name:     "write after dry-run",
			cmd:      "write",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"after": "_execute_task", "content": "def drain(self): pass", "dry_run": true},
			maxBytes: 700, // actual ~504B
		},
		{
			name:     "write append dry-run",
			cmd:      "write",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"append": true, "content": "# appended", "dry_run": true},
			maxBytes: 500, // actual ~280B
		},
		// --- Refs gates ---
		{
			name:     "refs chain",
			cmd:      "refs",
			args:     []string{"Scheduler"},
			flags:    map[string]any{"chain": "_execute_task"},
			maxBytes: 300, // actual ~102B
		},
		{
			name:     "refs impact",
			cmd:      "refs",
			args:     []string{"_execute_task"},
			flags:    map[string]any{"impact": true},
			maxBytes: 500, // actual ~262B
		},
		{
			name:     "explore gather",
			cmd:      "refs",
			args:     []string{"_execute_task"},
			flags:    map[string]any{"gather": true, "body": true, "budget": 1500},
			maxBytes: 8000, // actual ~405B but gather with body can grow
		},
		// --- Rename gates ---
		{
			name:     "rename dry-run",
			cmd:      "rename",
			args:     []string{"HandlerFunc", "TaskHandlerFunc"},
			flags:    map[string]any{"dry_run": true},
			maxBytes: 3500, // actual ~2816B
		},
		// --- Verify gates ---
		{
			name:     "verify",
			cmd:      "verify",
			args:     nil,
			flags:    nil,
			maxBytes: 300, // actual ~92B
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := dispatchJSON(ctx, db, tt.cmd, tt.args, tt.flags)
			if err != nil {
				t.Fatalf("dispatch error: %v", err)
			}
			if len(out) > tt.maxBytes {
				t.Errorf("response too large: %dB > %dB limit\nfirst 200 bytes: %s",
					len(out), tt.maxBytes, string(out[:min(200, len(out))]))
			}
		})
	}
}

// TestSignaturesSmaller verifies --signatures always returns fewer bytes
// than a full symbol read for the same container.
func TestSignaturesSmaller(t *testing.T) {
	db, _ := setupRepo(t)
	ctx := context.Background()

	// Containers where signatures should be strictly smaller than full body.
	// Go structs may be larger since signatures now include receiver methods
	// (method sigs vs just struct fields), which is intentional — the signatures
	// are still much smaller than reading all method implementations.
	containers := []string{
		"lib/scheduler.py:Scheduler",
		"lib/scheduler.py:DependencyGraph",
		"lib/TaskProcessor.java:TaskProcessor",
		"lib/config.rb:PluginRegistry",
	}
	// Go struct: signatures include receiver methods, so may exceed struct body size.
	// We verify they're generated correctly and log the size for reference.
	goContainers := []string{
		"internal/worker.go:WorkerPool",
	}

	for _, spec := range containers {
		t.Run(spec, func(t *testing.T) {
			full, err := dispatchJSON(ctx, db, "read", []string{spec}, nil)
			if err != nil {
				t.Fatalf("full read: %v", err)
			}
			sigs, err := dispatchJSON(ctx, db, "read", []string{spec}, map[string]any{"signatures": true})
			if err != nil {
				t.Fatalf("signatures read: %v", err)
			}
			if len(sigs) >= len(full) {
				t.Errorf("signatures (%dB) should be smaller than full (%dB)", len(sigs), len(full))
			}
			savings := 100 - (len(sigs)*100)/len(full)
			t.Logf("%s: full=%dB sigs=%dB savings=%d%%", spec, len(full), len(sigs), savings)
		})
	}
	for _, spec := range goContainers {
		t.Run(spec, func(t *testing.T) {
			_, err := dispatchJSON(ctx, db, "read", []string{spec}, nil)
			if err != nil {
				t.Fatalf("full read: %v", err)
			}
			sigs, err := dispatchJSON(ctx, db, "read", []string{spec}, map[string]any{"signatures": true})
			if err != nil {
				t.Fatalf("signatures read: %v", err)
			}
			// Go struct signatures include receiver methods — just verify they're non-empty
			if len(sigs) == 0 {
				t.Errorf("signatures should not be empty")
			}
			t.Logf("%s: sigs=%dB", spec, len(sigs))
		})
	}
}
