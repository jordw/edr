package bench_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jordw/edr/internal/cmdspec"
	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/session"
)

// handleDoJSON simulates a handleDo call: dispatches through the session
// post-processing pipeline. Returns JSON output bytes.
func handleDoJSON(t testing.TB, ctx context.Context, db index.SymbolStore, sess *session.Session,
	cmd string, args []string, flags map[string]any) []byte {
	t.Helper()

	if cmdspec.ModifiesState(cmd) {
		sess.InvalidateForEdit(cmd, args)
	}

	result, err := dispatch.Dispatch(ctx, db, cmd, args, flags)
	if err != nil {
		t.Fatalf("dispatch %s %v: %v", cmd, args, err)
	}

	data, _ := json.Marshal(result)
	text := sess.PostProcess(cmd, args, flags, result, string(data))

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

	sess := session.New()

	// Phase 1: Orientation — map the repo, find files, read signatures
	// This is how an agent starts: get the lay of the land.
	t.Run("orient/map_repo", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "map", nil, map[string]any{"budget": 500})
		assertJSONHas(t, out, "content")
		assertJSONHas(t, out, "symbols")
	})

	t.Run("orient/find_all_langs", func(t *testing.T) {
		for _, pat := range []string{"**/*.go", "**/*.py", "**/*.rs", "**/*.c", "**/*.java", "**/*.rb", "**/*.js", "**/*.tsx"} {
			out := handleDoJSON(t, ctx, db, sess, "search", []string{pat}, nil)
			if len(out) < 10 {
				t.Errorf("search %s returned too few bytes: %d", pat, len(out))
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
		{"web/components/Dashboard.tsx:DashboardProps", "TSX"},
		{"include/queue.h:task_queue", "C-header"},
	}

	t.Run("read/signatures_multi_lang", func(t *testing.T) {
		for _, c := range containers {
			out := handleDoJSON(t, ctx, db, sess, "read", []string{c.spec}, map[string]any{"signatures": true})
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
			out := handleDoJSON(t, ctx, db, sess, "read", []string{r.spec}, nil)
			if len(out) == 0 {
				t.Errorf("[%s] %s: empty response", r.lang, r.spec)
			}
		}
	})

	// Phase 3: Delta reads — re-read the same symbols, expect deltas/unchanged.
	// Symbol reads keep full content but mark session:"unchanged" (avoids --full round-trip).
	// File reads return a deduped stub with "unchanged":true.
	t.Run("read/delta_unchanged", func(t *testing.T) {
		for _, r := range fullReads {
			out := handleDoJSON(t, ctx, db, sess, "read", []string{r.spec}, nil)
			var m map[string]any
			json.Unmarshal(out, &m)
			if _, ok := m["unchanged"]; ok {
				continue // file-level dedup stub
			}
			if _, ok := m["delta"]; ok {
				continue // delta
			}
			if sessVal, _ := m["session"].(string); sessVal == "unchanged" {
				continue // symbol read with session dedup
			}
			t.Errorf("[%s] %s: expected delta, unchanged, or session:unchanged, got %d bytes", r.lang, r.spec, len(out))
		}
	})

	// Phase 4: Search across the codebase
	t.Run("search/symbol_cross_lang", func(t *testing.T) {
		// "enqueue" exists in Go, Rust, C, JS
		out := handleDoJSON(t, ctx, db, sess, "search", []string{"enqueue"}, map[string]any{"body": true, "budget": 500})
		assertJSONHas(t, out, "matches")
	})

	t.Run("search/text_cross_lang", func(t *testing.T) {
		// "retry" is a concept in every language's code
		// Text search defaults to grouped output (key is "files", not "matches")
		out := handleDoJSON(t, ctx, db, sess, "search", []string{"retry"}, map[string]any{"text": true, "budget": 300})
		assertJSONHas(t, out, "files")
	})

	t.Run("search/body_dedup", func(t *testing.T) {
		// Second search for "enqueue" with body should trigger body dedup
		out := handleDoJSON(t, ctx, db, sess, "search", []string{"enqueue"}, map[string]any{"body": true, "budget": 500})
		var m map[string]any
		json.Unmarshal(out, &m)
		if skipped, ok := m["skipped_bodies"]; ok {
			t.Logf("body dedup skipped: %v", skipped)
		}
	})

	// Phase 5: Explore and refs — semantic analysis
	t.Run("explore/python_gather", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "refs", []string{"_execute_task"}, map[string]any{
			"body": true, "budget": 1500,
		})
		assertJSONHas(t, out, "symbol")
	})

	t.Run("explore/go_callers_deps", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "refs", []string{"internal/queue.go", "Dequeue"}, map[string]any{
			"callers": true, "deps": true, "body": true,
		})
		// refs returns {symbol, body, callers, deps}
		assertJSONHas(t, out, "symbol")
	})

	t.Run("refs/cross_file", func(t *testing.T) {
		// Scope to Go's TaskQueue to avoid ambiguity with Rust/Ruby
		out := handleDoJSON(t, ctx, db, sess, "refs", []string{"internal/queue.go", "TaskQueue"}, nil)
		if len(out) < 20 {
			t.Errorf("refs TaskQueue: too short response: %d bytes", len(out))
		}
	})

	t.Run("refs/chain", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "refs", []string{"Scheduler"}, map[string]any{"chain": "_execute_task"})
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
			out := handleDoJSON(t, ctx, db, sess, "edit", []string{e.file}, map[string]any{
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

	// Phase 6b: Edit --fuzzy and --in
	t.Run("edit/fuzzy_dry_run", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"old_text": "self._running  =  True",
			"new_text": "self._running = False",
			"fuzzy":    true,
			"dry-run":  true,
		})
		var m map[string]any
		json.Unmarshal(out, &m)
		if status, _ := m["status"].(string); status != "dry_run" {
			t.Errorf("edit_fuzzy should be dry_run, got %q", status)
		}
	})

	t.Run("edit/in_symbol_dry_run", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"in":       "Scheduler",
			"old_text": "self._running = True",
			"new_text": "self._running = False",
			"dry-run":  true,
		})
		var m map[string]any
		json.Unmarshal(out, &m)
		if status, _ := m["status"].(string); status != "dry_run" {
			t.Errorf("edit_in_symbol should be dry_run, got %q", status)
		}
	})

	// Phase 6c: Refs --impact
	t.Run("refs/impact", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "refs", []string{"_execute_task"}, map[string]any{
			"impact": true,
		})
		if len(out) < 10 {
			t.Errorf("refs impact: too short: %d bytes", len(out))
		}
	})

	// Phase 6d: Verify
	t.Run("verify/auto_detect", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "verify", nil, nil)
		var m map[string]any
		json.Unmarshal(out, &m)
		// verify returns {command, status, duration_ms}
		if _, ok := m["status"]; !ok {
			t.Errorf("verify should return status, got keys: %v", mapKeys(m))
		}
	})

	// Phase 7: Real edits + revert — exercises edit events and revert detection
	pyFile := filepath.Join(tmp, "lib", "scheduler.py")
	pyOriginal, _ := os.ReadFile(pyFile)

	t.Run("edit/apply_and_revert", func(t *testing.T) {
		// Apply edit — use unique context to avoid ambiguity
		out := handleDoJSON(t, ctx, db, sess, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"old_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = True",
			"new_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = False  # BENCH EDIT",
		})
		var m map[string]any
		json.Unmarshal(out, &m)
		if ok, exists := m["ok"].(bool); exists && !ok {
			t.Fatalf("apply edit not ok: %s", string(out[:min(200, len(out))]))
		}

		// Revert edit
		out = handleDoJSON(t, ctx, db, sess, "edit", []string{"lib/scheduler.py"}, map[string]any{
			"old_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = False  # BENCH EDIT",
			"new_text": "\"\"\"Start the scheduler loop.\"\"\"\n        self._running = True",
		})
		json.Unmarshal(out, &m)
		if ok, exists := m["ok"].(bool); exists && !ok {
			t.Fatalf("revert edit not ok: %s", string(out[:min(200, len(out))]))
		}

		// Restore file for subsequent tests
		os.WriteFile(pyFile, pyOriginal, 0644)
		db.InvalidateFiles(ctx, []string{pyFile})
	})

	// Phase 8: Write inside container
	goFile := filepath.Join(tmp, "internal", "queue.go")
	goOriginal, _ := os.ReadFile(goFile)

	t.Run("write/inside_go_struct", func(t *testing.T) {
		out := handleDoJSON(t, ctx, db, sess, "write", []string{"internal/queue.go"}, map[string]any{
			"inside":  "TaskQueue",
			"content": "draining bool",
		})
		if len(out) == 0 {
			t.Fatal("write inside returned empty")
		}

		// Restore
		os.WriteFile(goFile, goOriginal, 0644)
		db.InvalidateFiles(ctx, []string{goFile})
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
			out := handleDoJSON(t, ctx, db, sess, "read", []string{s.file, s.symbol}, map[string]any{"depth": 2})
			if len(out) == 0 {
				t.Errorf("[%s] depth-2 %s:%s: empty", s.lang, s.file, s.symbol)
			}
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
		result, _ = dispatch.Dispatch(ctx, db, "refs", []string{"_execute_task"}, map[string]any{"gather": true, "body": true, "budget": 1500})
		data, _ = json.Marshal(result)
		text = sess.PostProcess("refs", []string{"_execute_task"}, map[string]any{"gather": true, "body": true, "budget": 1500}, result, string(data))
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
		db.InvalidateFiles(ctx, []string{pyFile})
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
