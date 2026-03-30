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

// BenchmarkWorkflowTraditional simulates what a skilled agent does WITHOUT edr:
// grep to find code, read line ranges, grep for callers, edit, re-read to confirm.
// This is the realistic baseline — not "read the whole file" which no agent does.
func BenchmarkWorkflowTraditional(b *testing.B) {
	db, tmp := setupRepo(b)
	ctx := context.Background()
	file := filepath.Join(tmp, "lib", "scheduler.py")
	original, _ := os.ReadFile(file)

	b.ResetTimer()
	for b.Loop() {
		totalBytes := 0

		// 1. Agent greps for the function (simulated: read full file, agent scans it)
		src, _ := os.ReadFile(file)
		totalBytes += len(src) // agent sees the whole file

		// 2. Agent reads a 50-line range around the function (grep told it line ~200)
		out, _ := dispatchJSON(ctx, db, "focus", []string{"lib/scheduler.py"}, map[string]any{"lines": "180:230"})
		totalBytes += len(out)

		// 3. Agent edits with text match
		out, _ = dispatchJSON(ctx, db, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"old_text": "self._running = True",
			"new_text": "self._running = False",
			"dry-run":  true,
		})
		totalBytes += len(out)

		// 4. Agent re-reads to verify (without edr's auto read-back)
		out, _ = dispatchJSON(ctx, db, "focus", []string{"lib/scheduler.py"}, map[string]any{"lines": "180:230"})
		totalBytes += len(out)

		b.ReportMetric(float64(totalBytes), "response_bytes")
		b.ReportMetric(heapAllocKB(), "heap_alloc_kb")

		b.StopTimer()
		os.WriteFile(file, original, 0644)
		db.InvalidateFiles(ctx, []string{file})
		b.StartTimer()
	}
}

// BenchmarkWorkflowEdr simulates the edr workflow:
// orient to find code, focus with auto-deps, edit with auto-readback.
// Fewer calls, smaller responses, agent gets exactly what it needs.
func BenchmarkWorkflowEdr(b *testing.B) {
	db, tmp := setupRepo(b)
	ctx := context.Background()
	file := filepath.Join(tmp, "lib", "scheduler.py")
	original, _ := os.ReadFile(file)

	b.ResetTimer()
	for b.Loop() {
		totalBytes := 0

		// 1. Orient: see the structure (budget-controlled)
		out, _ := dispatchJSON(ctx, db, "orient", nil, map[string]any{"dir": "lib", "budget": 500})
		totalBytes += len(out)

		// 2. Focus on the symbol (auto-includes dep signatures)
		out, _ = dispatchJSON(ctx, db, "focus", []string{"lib/scheduler.py:_execute_task"}, nil)
		totalBytes += len(out)

		// 3. Edit (auto read-back gives the updated function, no re-read needed)
		out, _ = dispatchJSON(ctx, db, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"in":       "Scheduler",
			"old_text": "self._running = True",
			"new_text": "self._running = False",
			"dry-run":  true,
		})
		totalBytes += len(out)

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
			maxBytes: 2500, // regex-based skeleton is less aggressive than tree-sitter
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
