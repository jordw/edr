package bench_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/session"
	"github.com/jordw/edr/internal/trace"
)

// handleDoJSON simulates a handleDo call: dispatches through the session
// post-processing pipeline and records trace events. Returns JSON output bytes.
func handleDoJSON(t testing.TB, ctx context.Context, db *index.DB, sess *session.Session, tc *trace.Collector,
	cmd string, args []string, flags map[string]any) []byte {
	t.Helper()

	cb := tc.BeginCall()
	numReads, numQueries, numEdits := 0, 0, 0
	if cmd == "read" {
		numReads = 1
	} else if cmd == "search" || cmd == "map" || cmd == "explore" || cmd == "refs" || cmd == "find" {
		numQueries = 1
	} else if cmd == "edit" || cmd == "write" {
		numEdits = 1
	}
	hasVerify := cmd == "verify"
	cb.SetRequest(numReads, numQueries, numEdits, 0, 0, hasVerify, false, nil)

	sess.ResetStats()

	if session.EditCommands[cmd] || cmd == "init" {
		sess.InvalidateForEdit(cmd, args)
	}

	result, err := dispatch.Dispatch(ctx, db, cmd, args, flags)
	if err != nil {
		t.Fatalf("dispatch %s %v: %v", cmd, args, err)
	}

	data, _ := json.Marshal(result)
	text := sess.PostProcess(cmd, args, flags, result, string(data))

	cb.AddQueryEvent(cmd, true, len(text))
	dr, bd, se := sess.GetStats()
	cb.SetSessionStats(dr, bd, se)
	cb.Finish(len(text), false, 0)

	return []byte(text)
}

// ---------------------------------------------------------------------------
// TestSessionMultiLang: Simulates a realistic agent session that touches
// every language in testdata. Exercises reads, searches, edits, writes,
// session optimizations (deltas, body dedup, slim edits), and traces.
// ---------------------------------------------------------------------------

func TestSessionMultiLang(t *testing.T) {
	db, tmp := setupRepo(t)
	ctx := context.Background()

	tc := trace.NewCollector(filepath.Join(tmp, ".edr"), "bench-test-1.0")
	if tc == nil {
		t.Fatal("failed to create trace collector")
	}
	defer tc.Close()

	sess := session.New()

	// Phase 1: Orientation — map the repo, find files, read signatures
	// This is how an agent starts: get the lay of the land.
	t.Run("orient/map_repo", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, tc, "map", nil, map[string]any{"budget": 500})
		assertJSONHas(t, out, "map")
		assertJSONHas(t, out, "symbols")
	})

	t.Run("orient/find_all_langs", func(t *testing.T) {
		for _, pat := range []string{"**/*.go", "**/*.py", "**/*.rs", "**/*.c", "**/*.java", "**/*.rb", "**/*.js", "**/*.tsx"} {
			out := handleDoJSON(t, ctx, db, sess, tc, "find", []string{pat}, nil)
			if len(out) < 10 {
				t.Errorf("find %s returned too few bytes: %d", pat, len(out))
			}
		}
	})

	// Phase 2: Multi-language reads — signatures, full, depth
	// Read containers across Go, Python, Rust, Java, JS, Ruby, C
	containers := []struct {
		spec string
		lang string
	}{
		{"lib/scheduler.py:Scheduler", "Python"},
		{"lib/scheduler.py:DependencyGraph", "Python"},
		{"internal/queue.go:TaskQueue", "Go"},
		{"internal/worker.go:WorkerPool", "Go"},
		{"lib/task_queue.rs:TaskQueue", "Rust"},
		{"lib/task_queue.rs:WorkerPool", "Rust"},
		{"lib/TaskProcessor.java:TaskProcessor", "Java"},
		{"lib/config.rb:PluginRegistry", "Ruby"},
		{"web/api.js:TaskAPIClient", "JS"},
		{"web/api.js:RateLimiter", "JS"},
		{"web/components/Dashboard.tsx:Dashboard", "TSX"},
		{"include/queue.h:task_queue", "C-header"},
	}

	t.Run("read/signatures_multi_lang", func(t *testing.T) {
		for _, c := range containers {
			out := handleDoJSON(t, ctx, db, sess, tc, "read", []string{c.spec}, map[string]any{"signatures": true})
			if len(out) == 0 {
				t.Errorf("[%s] %s: empty response", c.lang, c.spec)
			}
		}
	})

	// Read full symbols from each language
	fullReads := []struct {
		spec string
		lang string
	}{
		{"lib/scheduler.py:_execute_task", "Python"},
		{"internal/queue.go:Enqueue", "Go"},
		{"lib/task_queue.rs:enqueue", "Rust"},
		{"lib/TaskProcessor.java:processWithRetry", "Java"},
		{"lib/config.rb:delay_for", "Ruby"},
		{"web/api.js:validateTaskPayload", "JS"},
		{"web/components/TaskList.tsx:TaskList", "TSX"},
		{"lib/queue.c:tq_enqueue", "C"},
	}

	t.Run("read/full_symbols_multi_lang", func(t *testing.T) {
		for _, r := range fullReads {
			out := handleDoJSON(t, ctx, db, sess, tc, "read", []string{r.spec}, nil)
			if len(out) == 0 {
				t.Errorf("[%s] %s: empty response", r.lang, r.spec)
			}
		}
	})

	// Phase 3: Delta reads — re-read the same symbols, expect deltas/unchanged
	t.Run("read/delta_unchanged", func(t *testing.T) {
		for _, r := range fullReads {
			out := handleDoJSON(t, ctx, db, sess, tc, "read", []string{r.spec}, nil)
			var m map[string]any
			json.Unmarshal(out, &m)
			if _, ok := m["unchanged"]; !ok {
				if _, ok := m["delta"]; !ok {
					t.Errorf("[%s] %s: expected delta or unchanged, got %d bytes", r.lang, r.spec, len(out))
				}
			}
		}
	})

	// Phase 4: Search across the codebase
	t.Run("search/symbol_cross_lang", func(t *testing.T) {
		// "enqueue" exists in Go, Rust, C, JS
		out := handleDoJSON(t, ctx, db, sess, tc, "search", []string{"enqueue"}, map[string]any{"body": true, "budget": 500})
		assertJSONHas(t, out, "matches")
	})

	t.Run("search/text_cross_lang", func(t *testing.T) {
		// "retry" is a concept in every language's code
		out := handleDoJSON(t, ctx, db, sess, tc, "search", []string{"retry"}, map[string]any{"text": true, "budget": 300})
		assertJSONHas(t, out, "matches")
	})

	t.Run("search/body_dedup", func(t *testing.T) {
		// Second search for "enqueue" with body should trigger body dedup
		out := handleDoJSON(t, ctx, db, sess, tc, "search", []string{"enqueue"}, map[string]any{"body": true, "budget": 500})
		var m map[string]any
		json.Unmarshal(out, &m)
		if skipped, ok := m["skipped_bodies"]; ok {
			t.Logf("body dedup skipped: %v", skipped)
		}
	})

	// Phase 5: Explore and refs — semantic analysis
	t.Run("explore/python_gather", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, tc, "explore", []string{"_execute_task"}, map[string]any{
			"gather": true, "body": true, "budget": 1500,
		})
		assertJSONHas(t, out, "target")
	})

	t.Run("explore/go_callers_deps", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, tc, "explore", []string{"internal/queue.go", "Dequeue"}, map[string]any{
			"callers": true, "deps": true, "body": true,
		})
		// explore returns {symbol, body, callers, deps}
		assertJSONHas(t, out, "symbol")
	})

	t.Run("refs/cross_file", func(t *testing.T) {
		// Scope to Go's TaskQueue to avoid ambiguity with Rust/Ruby
		out := handleDoJSON(t, ctx, db, sess, tc, "refs", []string{"internal/queue.go", "TaskQueue"}, nil)
		if len(out) < 20 {
			t.Errorf("refs TaskQueue: too short response: %d bytes", len(out))
		}
	})

	t.Run("refs/chain", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, tc, "refs", []string{"Scheduler"}, map[string]any{"chain": "_execute_task"})
		if len(out) < 10 {
			t.Errorf("refs chain: too short response: %d bytes", len(out))
		}
	})

	// Phase 6: Edits across multiple languages (dry-run to keep testdata clean)
	edits := []struct {
		file    string
		oldText string
		newText string
		lang    string
	}{
		{"lib/scheduler.py", "self._running = False\n        self._max_workers = max_workers", "self._running = False  # trace test\n        self._max_workers = max_workers", "Python"},
		{"internal/queue.go", "// Sort by priority (higher first), then by creation time\n\tq.sortLocked()\n\tq.cond.Signal()", "// Sort by priority (higher first), then by creation time\n\tq.sortLocked()\n\tq.cond.Broadcast()", "Go"},
		{"lib/queue.c", "insert_sorted(q, task);\n\n    pthread_cond_signal(&q->cond);", "insert_sorted(q, task);\n\n    pthread_cond_broadcast(&q->cond);", "C"},
		{"web/api.js", "\"Content-Type\": \"application/json\", ...opts.headers", "\"Content-Type\": \"application/json\", \"X-Trace\": \"1\", ...opts.headers", "JS"},
	}

	t.Run("edit/dry_run_multi_lang", func(t *testing.T) {
		for _, e := range edits {
			out := handleDoJSON(t, ctx, db, sess, tc, "edit", []string{e.file}, map[string]any{
				"old_text": e.oldText,
				"new_text": e.newText,
				"dry-run":  true,
			})
			var m map[string]any
			if err := json.Unmarshal(out, &m); err != nil {
				t.Errorf("[%s] %s: invalid JSON: %v", e.lang, e.file, err)
				continue
			}
			// dry-run edits return {ok, diff, ...} or {ok, file, hash, lines_changed, diff_available}
			if ok, exists := m["ok"].(bool); exists && !ok {
				t.Errorf("[%s] %s: edit failed: %s", e.lang, e.file, string(out[:min(200, len(out))]))
			}
			// If diff is present, the edit succeeded
			if _, hasDiff := m["diff"]; !hasDiff {
				if _, hasDA := m["diff_available"]; !hasDA {
					if ok, exists := m["ok"].(bool); !exists || !ok {
						t.Errorf("[%s] %s: no diff or ok in response: %v", e.lang, e.file, mapKeys(m))
					}
				}
			}
		}
	})

	// Phase 7: Real edits + revert — exercises edit events and revert detection
	pyFile := filepath.Join(tmp, "lib", "scheduler.py")
	pyOriginal, _ := os.ReadFile(pyFile)

	t.Run("edit/apply_and_revert", func(t *testing.T) {
		// Apply edit — use unique context to avoid ambiguity
		out := handleDoJSON(t, ctx, db, sess, tc, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"old_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = True",
			"new_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = False  # BENCH EDIT",
		})
		var m map[string]any
		json.Unmarshal(out, &m)
		if ok, exists := m["ok"].(bool); exists && !ok {
			t.Fatalf("apply edit not ok: %s", string(out[:min(200, len(out))]))
		}

		// Revert edit
		out = handleDoJSON(t, ctx, db, sess, tc, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"old_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = False  # BENCH EDIT",
			"new_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = True",
		})
		json.Unmarshal(out, &m)
		if ok, exists := m["ok"].(bool); exists && !ok {
			t.Fatalf("revert edit not ok: %s", string(out[:min(200, len(out))]))
		}

		// Restore file for subsequent tests
		os.WriteFile(pyFile, pyOriginal, 0644)
		index.IndexFile(ctx, db, pyFile)
	})

	// Phase 8: Write inside container
	goFile := filepath.Join(tmp, "internal", "queue.go")
	goOriginal, _ := os.ReadFile(goFile)

	t.Run("write/inside_go_struct", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, tc, "write", []string{"internal/queue.go"}, map[string]any{
			"inside":  "TaskQueue",
			"content": "draining bool",
		})
		if len(out) == 0 {
			t.Fatal("write inside returned empty")
		}

		// Restore
		os.WriteFile(goFile, goOriginal, 0644)
		index.IndexFile(ctx, db, goFile)
	})

	// Phase 9: DispatchMulti — batch reads from multiple languages
	t.Run("batch/multi_lang_reads", func(t *testing.T) {
		cmds := []dispatch.MultiCmd{
			{Cmd: "read", Args: []string{"lib/scheduler.py:Scheduler"}, Flags: map[string]any{"signatures": true}},
			{Cmd: "read", Args: []string{"lib/task_queue.rs:TaskQueue"}, Flags: map[string]any{"signatures": true}},
			{Cmd: "read", Args: []string{"lib/TaskProcessor.java:TaskProcessor"}, Flags: map[string]any{"signatures": true}},
			{Cmd: "read", Args: []string{"internal/queue.go:TaskQueue"}, Flags: map[string]any{"signatures": true}},
			{Cmd: "read", Args: []string{"web/api.js:TaskAPIClient"}, Flags: map[string]any{"signatures": true}},
			{Cmd: "read", Args: []string{"lib/config.rb:PluginRegistry"}, Flags: map[string]any{"signatures": true}},
		}
		results := dispatch.DispatchMulti(ctx, db, cmds)
		for i, r := range results {
			if !r.OK {
				t.Errorf("batch read %d (%s): %s", i, cmds[i].Args[0], r.Error)
			}
		}
	})

	// Phase 10: Depth-2 reads across languages
	t.Run("read/depth2_multi_lang", func(t *testing.T) {
		specs := []struct {
			file   string
			symbol string
			lang   string
		}{
			{"lib/scheduler.py", "_execute_task", "Python"},
			{"internal/queue.go", "Enqueue", "Go"},
			{"lib/task_queue.rs", "enqueue", "Rust"},
			{"lib/TaskProcessor.java", "processWithRetry", "Java"},
		}
		for _, s := range specs {
			out := handleDoJSON(t, ctx, db, sess, tc, "read", []string{s.file, s.symbol}, map[string]any{"depth": 2})
			if len(out) == 0 {
				t.Errorf("[%s] depth-2 %s:%s: empty", s.lang, s.file, s.symbol)
			}
		}
	})

	// Close collector to flush all events, then bench the session
	tc.Close()

	t.Run("bench_session", func(t *testing.T) {
		traceDB, err := trace.OpenTraceDB(filepath.Join(tmp, ".edr"))
		if err != nil {
			t.Fatal(err)
		}
		defer traceDB.Close()

		result, err := trace.BenchSession(traceDB, "")
		if err != nil {
			t.Fatal(err)
		}

		data, _ := json.MarshalIndent(result, "", "  ")
		t.Logf("Session bench result:\n%s", string(data))

		// Validate the bench result has reasonable values
		if result.TotalCalls < 30 {
			t.Errorf("expected at least 30 calls, got %d", result.TotalCalls)
		}
		if result.TotalReads < 20 {
			t.Errorf("expected at least 20 reads, got %d", result.TotalReads)
		}
		if result.TotalQueries < 5 {
			t.Errorf("expected at least 5 queries, got %d", result.TotalQueries)
		}
		if result.DeltaReads < 1 {
			t.Errorf("expected at least 1 delta read, got %d", result.DeltaReads)
		}
		if result.TotalTokensEst < 100 {
			t.Errorf("expected at least 100 estimated tokens, got %d", result.TotalTokensEst)
		}
		if result.ReadEfficiency == nil {
			t.Error("expected read_efficiency to be computed")
		} else {
			t.Logf("Read efficiency: %.1f%%", *result.ReadEfficiency*100)
		}
		if result.TokensPerCall == nil {
			t.Error("expected tokens_per_call to be computed")
		} else {
			t.Logf("Tokens/call: %.0f", *result.TokensPerCall)
		}
		if result.OptimizationRate == nil {
			t.Error("expected optimization_rate to be computed")
		} else {
			t.Logf("Optimization rate: %.1f%%", *result.OptimizationRate*100)
		}
		if result.AvgCallDurationMs == nil {
			t.Error("expected avg_call_duration_ms to be computed")
		}
	})
}

// ---------------------------------------------------------------------------
// BenchmarkSessionWorkflow: Performance benchmark for a multi-language
// session workflow. Measures total bytes and time for a complete session.
// ---------------------------------------------------------------------------

func BenchmarkSessionWorkflow(b *testing.B) {
	db, tmp := setupRepo(b)
	ctx := context.Background()
	pyFile := filepath.Join(tmp, "lib", "scheduler.py")
	pyOriginal, _ := os.ReadFile(pyFile)

	b.ResetTimer()
	for b.Loop() {
		sess := session.New()

		totalBytes := 0

		// Orient
		out, _ := dispatchJSON(ctx, db, "map", nil, map[string]any{"budget": 500})
		totalBytes += len(out)

		// Read signatures from 6 languages
		for _, spec := range []string{
			"lib/scheduler.py:Scheduler",
			"lib/task_queue.rs:TaskQueue",
			"lib/TaskProcessor.java:TaskProcessor",
			"internal/queue.go:TaskQueue",
			"web/api.js:TaskAPIClient",
			"lib/config.rb:PluginRegistry",
		} {
			result, _ := dispatch.Dispatch(ctx, db, "read", []string{spec}, map[string]any{"signatures": true})
			data, _ := json.Marshal(result)
			text := sess.PostProcess("read", []string{spec}, map[string]any{"signatures": true}, result, string(data))
			totalBytes += len(text)
		}

		// Read full methods
		for _, spec := range []string{
			"lib/scheduler.py:_execute_task",
			"internal/queue.go:Enqueue",
			"lib/task_queue.rs:enqueue",
			"lib/TaskProcessor.java:processWithRetry",
		} {
			result, _ := dispatch.Dispatch(ctx, db, "read", []string{spec}, nil)
			data, _ := json.Marshal(result)
			text := sess.PostProcess("read", []string{spec}, nil, result, string(data))
			totalBytes += len(text)
		}

		// Re-read (should be delta/unchanged)
		for _, spec := range []string{
			"lib/scheduler.py:_execute_task",
			"internal/queue.go:Enqueue",
		} {
			result, _ := dispatch.Dispatch(ctx, db, "read", []string{spec}, nil)
			data, _ := json.Marshal(result)
			text := sess.PostProcess("read", []string{spec}, nil, result, string(data))
			totalBytes += len(text)
		}

		// Search
		result, _ := dispatch.Dispatch(ctx, db, "search", []string{"enqueue"}, map[string]any{"body": true, "budget": 500})
		data, _ := json.Marshal(result)
		text := sess.PostProcess("search", []string{"enqueue"}, map[string]any{"body": true, "budget": 500}, result, string(data))
		totalBytes += len(text)

		// Explore
		result, _ = dispatch.Dispatch(ctx, db, "explore", []string{"_execute_task"}, map[string]any{"gather": true, "body": true, "budget": 1500})
		data, _ = json.Marshal(result)
		text = sess.PostProcess("explore", []string{"_execute_task"}, map[string]any{"gather": true, "body": true, "budget": 1500}, result, string(data))
		totalBytes += len(text)

		// Edit + revert
		result, _ = dispatch.Dispatch(ctx, db, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"old_text": "self._running = True", "new_text": "self._running = False",
		})
		data, _ = json.Marshal(result)
		text = sess.PostProcess("edit", []string{"lib/scheduler.py"}, nil, result, string(data))
		totalBytes += len(text)

		b.StopTimer()
		os.WriteFile(pyFile, pyOriginal, 0644)
		index.IndexFile(ctx, db, pyFile)
		b.StartTimer()

		b.ReportMetric(float64(totalBytes), "response_bytes")
	}
}

// assertJSONHas checks that the JSON output contains a given key.
func assertJSONHas(t testing.TB, data []byte, key string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Errorf("invalid JSON: %v (first 100 bytes: %s)", err, string(data[:min(100, len(data))]))
		return
	}
	if _, ok := m[key]; !ok {
		t.Errorf("expected key %q in response, got keys: %v", key, mapKeys(m))
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
