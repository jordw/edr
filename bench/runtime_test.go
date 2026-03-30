// Runtime benchmarks measuring wall time, allocations, and response size
// for individual edr commands. These are pure latency/throughput benchmarks.
//
// Run with: go test ./bench/ -bench . -benchmem
package bench_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
)

// ---------------------------------------------------------------------------
// Index benchmarks
// ---------------------------------------------------------------------------

func BenchmarkParseRepo(b *testing.B) {
	wd, _ := os.Getwd()
	srcDir := filepath.Join(wd, "testdata")

	b.ResetTimer()
	for b.Loop() {
		tmp := b.TempDir()
		filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(srcDir, path)
			dst := filepath.Join(tmp, rel)
			if info.IsDir() {
				return os.MkdirAll(dst, 0755)
			}
			data, _ := os.ReadFile(path)
			return os.WriteFile(dst, data, info.Mode())
		})
		db := index.NewOnDemand(tmp)
		ctx := context.Background()
		// Force a full parse of all files
		db.AllSymbols(ctx)
		db.Close()
	}
}

func BenchmarkParseFile(b *testing.B) {
	db, tmp := setupRepo(b)
	file := filepath.Join(tmp, "lib", "scheduler.py")
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		db.InvalidateFiles(ctx, []string{file})
		db.GetSymbolsByFile(ctx, file)
	}
}

// ---------------------------------------------------------------------------
// Read benchmarks — wall time + response_bytes
//
// ReadSignatures is slower than ReadSymbol because it does extra tree-sitter
// work to extract method stubs. The win is response bytes, not server speed.
// ---------------------------------------------------------------------------

func BenchmarkReadFile(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "read", []string{"lib/scheduler.py"}, nil)
}

func BenchmarkReadSymbol(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "read", []string{"lib/scheduler.py:Scheduler"}, nil)
}

func BenchmarkReadSignatures(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "read", []string{"lib/scheduler.py:Scheduler"}, map[string]any{"signatures": true})
}

func BenchmarkReadDepth2(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "read", []string{"lib/scheduler.py", "_execute_task"}, map[string]any{"depth": 2})
}

func BenchmarkReadMultiFile(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "read", []string{
		"lib/scheduler.py",
		"lib/TaskProcessor.java",
		"internal/worker.go",
	}, map[string]any{"budget": 500})
}

// ---------------------------------------------------------------------------
// Read benchmarks (continued) — line ranges
// ---------------------------------------------------------------------------

func BenchmarkReadLines(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "read", []string{"lib/scheduler.py"}, map[string]any{"lines": "1:30"})
}

// ---------------------------------------------------------------------------
// Map benchmarks
// ---------------------------------------------------------------------------

func BenchmarkMapRepo(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "map", nil, map[string]any{"budget": 500})
}

func BenchmarkMapFile(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "map", []string{"lib/scheduler.py"}, nil)
}

func BenchmarkMapFileFiltered(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "map", []string{"lib/scheduler.py"}, map[string]any{"type": "function"})
}

func BenchmarkMapDir(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "map", nil, map[string]any{"dir": "lib", "budget": 500})
}

func BenchmarkMapGrep(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "map", nil, map[string]any{"grep": "task", "budget": 500})
}

func BenchmarkMapLang(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "map", nil, map[string]any{"lang": "python", "budget": 500})
}

// ---------------------------------------------------------------------------
// Edit benchmarks
// ---------------------------------------------------------------------------

func BenchmarkEditDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "edit", []string{"lib/scheduler.py", "_execute_task"}, map[string]any{
		"new_text": "def _execute_task(self, task):\n    pass\n",
		"dry-run":  true,
	})
}

func BenchmarkEditMatchDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "edit", []string{"lib/scheduler.py"}, map[string]any{
		"old_text": "self._running = True",
		"new_text": "self._running = False",
		"dry-run":  true,
	})
}

func BenchmarkEditMoveDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "edit", []string{"internal/queue.go", "Close"}, map[string]any{
		"move_after": "NewTaskQueue",
		"dry-run":    true,
	})
}

func BenchmarkEditFuzzyDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	// Fuzzy match: extra whitespace in old_text that doesn't match literally
	benchDispatch(b, db, "edit", []string{"lib/scheduler.py"}, map[string]any{
		"old_text": "self._running  =  True",
		"new_text": "self._running = False",
		"fuzzy":    true,
		"dry-run":  true,
	})
}

func BenchmarkEditInSymbol(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "edit", []string{"lib/scheduler.py"}, map[string]any{
		"in":       "Scheduler",
		"old_text": "self._running = True",
		"new_text": "self._running = False",
		"dry-run":  true,
	})
}

func BenchmarkEditDeleteDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "edit", []string{"lib/scheduler.py:ScheduleType"}, map[string]any{
		"delete":  true,
		"dry-run": true,
	})
}

// ---------------------------------------------------------------------------
// DispatchMulti (batch) benchmark
// ---------------------------------------------------------------------------

func BenchmarkDispatchMulti(b *testing.B) {
	db, _ := setupRepo(b)
	ctx := context.Background()
	cmds := []dispatch.MultiCmd{
		{Cmd: "focus", Args: []string{"lib/scheduler.py:Scheduler"}, Flags: map[string]any{"signatures": true}},
		{Cmd: "focus", Args: []string{"lib/scheduler.py:_execute_task"}, Flags: nil},
		{Cmd: "orient", Args: nil, Flags: map[string]any{"budget": 300}},
	}

	b.ResetTimer()
	for b.Loop() {
		results := dispatch.DispatchMulti(ctx, db, cmds)
		totalBytes := 0
		for _, r := range results {
			data, _ := json.Marshal(r)
			totalBytes += len(data)
		}
		b.ReportMetric(float64(totalBytes), "response_bytes")
	}
}

// ---------------------------------------------------------------------------
// Write benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWriteAfterDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "write", []string{"lib/scheduler.py"}, map[string]any{
		"after":   "_execute_task",
		"content": "def drain(self): pass",
		"dry_run": true,
	})
}

func BenchmarkWriteAppendDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "write", []string{"lib/scheduler.py"}, map[string]any{
		"append":  true,
		"content": "# appended line",
		"dry_run": true,
	})
}

func BenchmarkWriteInside(b *testing.B) {
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
		out, err := dispatchJSON(ctx, db, "write", []string{"lib/scheduler.py"}, map[string]any{
			"inside":  "Scheduler",
			"content": newMethod,
		})
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(len(out)), "response_bytes")

		b.StopTimer()
		os.WriteFile(file, original, 0644)
		db.InvalidateFiles(ctx, []string{file})
		b.StartTimer()
	}
}

// ---------------------------------------------------------------------------
// Verify benchmark
// ---------------------------------------------------------------------------

func BenchmarkVerify(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "verify", nil, nil)
}

// ---------------------------------------------------------------------------
// Agent workflow benchmark — multi-step edit cycle without session
// ---------------------------------------------------------------------------

// BenchmarkAgentEditCycle simulates a realistic agent edit workflow:
// orient → focus (with auto-deps) → edit (with auto-readback).
// This is the end-to-end time for one complete edit cycle.
func BenchmarkAgentEditCycle(b *testing.B) {
	db, tmp := setupRepo(b)
	ctx := context.Background()
	file := filepath.Join(tmp, "lib", "scheduler.py")
	original, _ := os.ReadFile(file)

	b.ResetTimer()
	for b.Loop() {
		totalBytes := 0
		// 1. Orient: understand the codebase
		out, _ := dispatchJSON(ctx, db, "orient", nil, map[string]any{"budget": 500})
		totalBytes += len(out)
		// 2. Focus on target (auto-includes dep signatures)
		out, _ = dispatchJSON(ctx, db, "focus", []string{"lib/scheduler.py:_execute_task"}, nil)
		totalBytes += len(out)
		// 3. Edit with auto-readback (dry-run to keep benchmark repeatable)
		out, _ = dispatchJSON(ctx, db, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"in":       "Scheduler",
			"old_text": "self._running = True",
			"new_text": "self._running = False",
			"dry-run":  true,
		})
		totalBytes += len(out)

		b.ReportMetric(float64(totalBytes), "response_bytes")

		b.StopTimer()
		os.WriteFile(file, original, 0644)
		db.InvalidateFiles(ctx, []string{file})
		b.StartTimer()
	}
}
