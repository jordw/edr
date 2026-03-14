// Shared test infrastructure for all benchmark tracks.
package bench_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
)

// heapAllocKB returns current heap allocation in kilobytes.
// Unlike peak RSS (which is process-wide and only grows), this reflects
// the heap state at the point of measurement and is meaningful per-benchmark.
func heapAllocKB() float64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / 1024
}

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
