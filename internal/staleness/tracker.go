package staleness

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Tracker accumulates dirty-file markers on disk. Writes use
// O_APPEND with a single WriteString so concurrent Mark calls from
// parallel processes don't interleave within an individual write.
// POSIX only guarantees this strictly for pipes (PIPE_BUF); for
// regular files the guarantee is de facto on Linux (tmpfs, ext4,
// xfs) and best-effort on macOS/APFS — paths are well under the
// threshold in practice, but the 8-goroutine concurrent-Mark test
// is treated as load-bearing, not redundant.
//
// This matters because edr is routinely run by multiple agents in
// parallel against the same repo, and the previous
// read-merge-rewrite dirty list would silently drop markers under
// contention.
//
// Dirty returns the deduplicated set. Clear truncates the file. The
// tracker state is a single file at edrDir/<name>.dirty.
type Tracker struct {
	path string
	mu   sync.Mutex // serializes opens within a single process
}

// OpenTracker returns a Tracker backed by edrDir/<name>.dirty. The
// file is created lazily on first Mark. name typically matches the
// consumer (e.g. "trigram", "scope"). edrDir is created if it
// doesn't exist.
func OpenTracker(edrDir, name string) *Tracker {
	return &Tracker{path: filepath.Join(edrDir, name+".dirty")}
}

// Path returns the absolute path of the backing dirty file. Primarily
// for tests.
func (t *Tracker) Path() string { return t.path }

// Mark appends the given repo-relative paths to the dirty set. Empty
// and whitespace-only paths are dropped. Mark is safe to call
// concurrently from multiple goroutines and multiple processes.
func (t *Tracker) Mark(paths ...string) {
	if len(paths) == 0 {
		return
	}
	var sb strings.Builder
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		sb.WriteString(p)
		sb.WriteByte('\n')
	}
	if sb.Len() == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(t.path), 0o700); err != nil {
		return
	}
	// O_APPEND with a single syscall is atomic below PIPE_BUF.
	// Retries on EINTR are handled by the Go stdlib.
	f, err := os.OpenFile(t.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		// Bounded retry — a concurrent Clear/rename can race the open.
		for i := 0; i < 3 && err != nil; i++ {
			time.Sleep(time.Duration(i+1) * time.Millisecond)
			f, err = os.OpenFile(t.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		}
		if err != nil {
			return
		}
	}
	defer f.Close()
	_, _ = f.WriteString(sb.String())
}

// Dirty returns the deduplicated, sorted list of marked paths. Legacy
// boolean markers ("1") and empty lines are ignored.
func (t *Tracker) Dirty() []string {
	// Hold the lock through the read so an in-process Mark cannot
	// append a partial line while the scanner is consuming the file.
	// Cross-process Mark writes are still atomic-enough per Tracker's
	// package comment; same-process races are fully prevented here.
	t.mu.Lock()
	defer t.mu.Unlock()
	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	seen := make(map[string]struct{}, 64)
	s := bufio.NewScanner(f)
	// Ample buffer: lines are paths.
	s.Buffer(make([]byte, 0, 8192), 1<<20)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || line == "1" {
			continue
		}
		seen[line] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Has reports whether rel is present in the dirty set. O(n) in the
// number of lines; callers that need bulk lookup should call Dirty
// once and build their own set.
func (t *Tracker) Has(rel string) bool {
	for _, p := range t.Dirty() {
		if p == rel {
			return true
		}
	}
	return false
}

// IsDirty reports whether there are any dirty markers. Cheap: just a
// stat.
func (t *Tracker) IsDirty() bool {
	info, err := os.Stat(t.path)
	return err == nil && info.Size() > 0
}

// Clear removes the dirty marker file. After a full index rebuild,
// callers should call Clear to reset the dirty set. A Mark concurrent
// with Clear may win or lose the race; in the agent use case that's
// fine (the next tick re-detects via staleness.Check).
func (t *Tracker) Clear() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = os.Remove(t.path)
}
