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

func BenchmarkIndexRepo(b *testing.B) {
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
		db, err := index.OpenDB(tmp)
		if err != nil {
			b.Fatal(err)
		}
		ctx := context.Background()
		if _, _, err := index.IndexRepo(ctx, db); err != nil {
			b.Fatal(err)
		}
		// Report DB and WAL sizes
		dbPath := filepath.Join(tmp, ".edr", "index.db")
		if info, err := os.Stat(dbPath); err == nil {
			b.ReportMetric(float64(info.Size()/1024), "db_size_kb")
		}
		walPath := dbPath + "-wal"
		if info, err := os.Stat(walPath); err == nil {
			b.ReportMetric(float64(info.Size()/1024), "wal_size_kb")
		}
		db.Close()
	}
}

func BenchmarkIndexFile(b *testing.B) {
	db, tmp := setupRepo(b)
	file := filepath.Join(tmp, "lib", "scheduler.py")
	ctx := context.Background()

	b.ResetTimer()
	for b.Loop() {
		if err := index.IndexFile(ctx, db, file); err != nil {
			b.Fatal(err)
		}
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
// Search benchmarks
// ---------------------------------------------------------------------------

func BenchmarkSearchSymbol(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "search", []string{"execute"}, nil)
}

func BenchmarkSearchSymbolBody(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "search", []string{"execute"}, map[string]any{"body": true, "budget": 500})
}

func BenchmarkSearchText(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "search", []string{"retry"}, map[string]any{"text": true, "budget": 300})
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
	benchDispatch(b, db, "edit", []string{"internal/queue.go"}, map[string]any{
		"move":    "Close",
		"after":   "NewTaskQueue",
		"dry-run": true,
	})
}

// ---------------------------------------------------------------------------
// Explore / Refs benchmarks
// ---------------------------------------------------------------------------

func BenchmarkExplore(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "refs", []string{"lib/scheduler.py", "_execute_task"}, map[string]any{
		"body":    true,
		"callers": true,
		"deps":    true,
	})
}

func BenchmarkExploreGather(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "refs", []string{"_execute_task"}, map[string]any{
		"gather": true,
		"body":   true,
		"budget": 1500,
	})
}

func BenchmarkRefs(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "refs", []string{"_execute_task"}, nil)
}

func BenchmarkRefsChain(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "refs", []string{"Scheduler"}, map[string]any{"chain": "_execute_task"})
}

// ---------------------------------------------------------------------------
// Find benchmarks
// ---------------------------------------------------------------------------

func BenchmarkFind(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "search", []string{"**/*.py"}, nil)
}

// ---------------------------------------------------------------------------
// Rename benchmark
// ---------------------------------------------------------------------------

func BenchmarkRenameDryRun(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "rename", []string{"HandlerFunc", "TaskHandlerFunc"}, map[string]any{
		"dry_run": true,
	})
}

// ---------------------------------------------------------------------------
// DispatchMulti (batch) benchmark
// ---------------------------------------------------------------------------

func BenchmarkDispatchMulti(b *testing.B) {
	db, _ := setupRepo(b)
	ctx := context.Background()
	cmds := []dispatch.MultiCmd{
		{Cmd: "read", Args: []string{"lib/scheduler.py:Scheduler"}, Flags: map[string]any{"signatures": true}},
		{Cmd: "search", Args: []string{"execute"}, Flags: map[string]any{"body": true, "budget": 300}},
		{Cmd: "map", Args: nil, Flags: map[string]any{"budget": 300}},
		{Cmd: "refs", Args: []string{"_execute_task"}, Flags: nil},
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
// Write --inside benchmark
// ---------------------------------------------------------------------------

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
		index.IndexFile(ctx, db, file)
		b.StartTimer()
	}
}
