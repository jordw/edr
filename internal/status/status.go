package status

// Report is a uniform per-stack storage-health snapshot. Individual
// stacks populate the fields they know; zero values are fine for
// fields that do not apply.
//
// This is intentionally minimal: it is the shared shape that idx, scope,
// and session each populate from their own on-disk state. Consumers such
// as "edr next" and "edr index --status" read uniform Reports and shape
// them into their output formats.
type Report struct {
	// Name identifies the stack: "index", "scope", "session".
	Name string

	// Exists is true when the on-disk store is present.
	Exists bool

	// Files is the number of files covered by this stack
	// (indexed, scoped, or tracked).
	Files int

	// Bytes is the on-disk size of the store, in bytes.
	Bytes int64

	// Stale is true when the store is out of sync with its source of
	// truth. For idx this is the git index mtime + dirty flag. For
	// scope it is a file-count mismatch against the working tree. For
	// session it is not applicable and left false.
	Stale bool

	// Coverage is the fraction of repo files covered, in [0,1].
	// Zero when coverage is not computable for the stack.
	Coverage float64

	// Extra holds per-stack extras that do not fit the common fields:
	// trigram count for idx, checkpoint count for session, etc. Nil is
	// equivalent to empty.
	Extra map[string]any
}

// Reporter is the interface each stack implements to produce its
// Report. Implementations must be cheap: "edr next" fires on every
// command, so reporters do header-only reads (no full decodes).
type Reporter interface {
	Status() Report
}

// Aggregate collects reports from N reporters. Nil reporters are
// skipped, not reported as zero values. Order is preserved.
func Aggregate(reporters ...Reporter) []Report {
	out := make([]Report, 0, len(reporters))
	for _, r := range reporters {
		if r == nil {
			continue
		}
		out = append(out, r.Status())
	}
	return out
}
