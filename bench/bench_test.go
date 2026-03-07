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

// setupRepo creates a temp copy of bench/testdata indexed and ready for queries.
func setupRepo(tb testing.TB) (*index.DB, string) {
	tb.Helper()

	wd, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	srcDir := filepath.Join(wd, "testdata")
	if _, err := os.Stat(srcDir); err != nil {
		tb.Fatalf("testdata not found at %s", srcDir)
	}

	tmp := tb.TempDir()
	err = filepath.Walk(srcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcDir, path)
		dst := filepath.Join(tmp, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, data, info.Mode())
	})
	if err != nil {
		tb.Fatal(err)
	}

	db, err := index.OpenDB(tmp)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if _, _, err := index.IndexRepo(ctx, db); err != nil {
		tb.Fatal(err)
	}
	return db, tmp
}

// dispatchJSON calls Dispatch and returns the JSON output.
func dispatchJSON(ctx context.Context, db *index.DB, cmd string, args []string, flags map[string]any) ([]byte, error) {
	result, err := dispatch.Dispatch(ctx, db, cmd, args, flags)
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

// benchDispatch runs a dispatch command, reporting both wall time (automatic)
// and response_bytes (custom metric). Agents care about response bytes;
// server operators care about wall time. Both are visible in output.
func benchDispatch(b *testing.B, db *index.DB, cmd string, args []string, flags map[string]any) {
	b.Helper()
	ctx := context.Background()
	b.ResetTimer()
	for b.Loop() {
		out, err := dispatchJSON(ctx, db, cmd, args, flags)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(len(out)), "response_bytes")
	}
}

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

// ---------------------------------------------------------------------------
// Explore / Refs benchmarks
// ---------------------------------------------------------------------------

func BenchmarkExplore(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "explore", []string{"lib/scheduler.py", "_execute_task"}, map[string]any{
		"body":    true,
		"callers": true,
		"deps":    true,
	})
}

func BenchmarkRefs(b *testing.B) {
	db, _ := setupRepo(b)
	benchDispatch(b, db, "refs", []string{"_execute_task"}, nil)
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

		b.StopTimer()
		os.WriteFile(file, original, 0644)
		index.IndexFile(ctx, db, file)
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

		b.StopTimer()
		os.WriteFile(file, original, 0644)
		index.IndexFile(ctx, db, file)
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
		name    string
		cmd     string
		args    []string
		flags   map[string]any
		maxBytes int // response must be <= this
	}{
		{
			name:     "signatures < full symbol",
			cmd:      "read",
			args:     []string{"lib/scheduler.py:Scheduler"},
			flags:    map[string]any{"signatures": true},
			maxBytes: 2000, // full symbol is ~7500B
		},
		{
			name:     "depth2 < full method",
			cmd:      "read",
			args:     []string{"lib/scheduler.py", "_execute_task"},
			flags:    map[string]any{"depth": 2},
			maxBytes: 2000, // full method is ~2100B
		},
		{
			name:     "search with budget",
			cmd:      "search",
			args:     []string{"execute"},
			flags:    map[string]any{"body": true, "budget": 500},
			maxBytes: 3000,
		},
		{
			name:     "map with budget",
			cmd:      "map",
			args:     nil,
			flags:    map[string]any{"budget": 500},
			maxBytes: 3000,
		},
		{
			name:     "multi-file read with budget",
			cmd:      "read",
			args:     []string{"lib/scheduler.py", "lib/TaskProcessor.java", "internal/worker.go"},
			flags:    map[string]any{"budget": 500},
			maxBytes: 3500,
		},
		{
			name:     "edit dry-run",
			cmd:      "edit",
			args:     []string{"lib/scheduler.py"},
			flags:    map[string]any{"old_text": "self._running = True", "new_text": "self._running = False", "dry-run": true},
			maxBytes: 1000,
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

	containers := []string{
		"lib/scheduler.py:Scheduler",
		"lib/scheduler.py:DependencyGraph",
		"lib/TaskProcessor.java:TaskProcessor",
		"internal/worker.go:WorkerPool",
		"lib/config.rb:PluginRegistry",
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
}
