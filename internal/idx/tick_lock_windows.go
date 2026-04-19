//go:build windows

package idx

import "os"

// Windows doesn't have an unqualified equivalent of advisory flock
// and edr's parallel-agent use case is unix-only today. The stub
// always succeeds so concurrent ticks fall back to pre-lock
// behavior (last-writer-wins) rather than failing outright. When
// Windows demand materializes, swap in a LockFileEx-backed
// implementation — the callers already handle a tryLockFile failure
// by skipping the tick.
func tryLockFile(f *os.File) error {
	return nil
}

func unlockFile(f *os.File) {}
