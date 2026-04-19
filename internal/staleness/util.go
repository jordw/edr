package staleness

import "time"

// nowUnixNano returns the current wall time in nanoseconds. Factored
// out so tests can stub it if needed (not currently used).
func nowUnixNano() int64 { return time.Now().UnixNano() }
