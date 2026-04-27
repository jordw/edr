package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/output"
	"github.com/jordw/edr/internal/session"
)

// addDispatchFailedOp creates a failed op on the envelope, matching batch behavior.
// Per-op errors go on the op; only index-level errors go in envelope errors[].
func addDispatchFailedOp(env *output.Envelope, opID, opType string, err error, sess *session.Session) {
	// Surface structured not-found errors with diagnostic hints
	var nfe *dispatch.NotFoundError
	if errors.As(err, &nfe) {
		// Attribute staleness to the agent's own prior edits when applicable.
		if sess != nil && nfe.File != "" {
			if n := sess.EditsSinceRead(nfe.File); n > 0 {
				nfe.EditsAgo = n
				stale := fmt.Sprintf("your view is stale: %d edit(s) to %s since last read — run `edr focus %s` first", n, nfe.File, nfe.File)
				if nfe.Hint == "" {
					nfe.Hint = stale
				} else {
					nfe.Hint = stale + "; " + nfe.Hint
				}
			}
		}
		env.AddFailedOpResult(opID, opType, "not_found", nfe)
		return
	}

	// Surface ambiguous symbol errors with candidates
	var ambErr *index.AmbiguousSymbolError
	if errors.As(err, &ambErr) {
		candidates := make([]map[string]any, len(ambErr.Candidates))
		for i, c := range ambErr.Candidates {
			rel := c.File
			if ambErr.Root != "" {
				rel = output.Rel(c.File)
			}
			candidates[i] = map[string]any{
				"file": rel,
				"line": c.StartLine,
				"type": c.Type,
			}
		}
		env.AddFailedOpResult(opID, opType, "ambiguous_symbol", map[string]any{
			"error":      ambErr.Error(),
			"symbol":     ambErr.Name,
			"candidates": candidates,
			"hint":       "use file:symbol to disambiguate",
		})
		return
	}

	// Classify remaining op-level errors with specific codes
	code := classifyError(err)
	env.AddFailedOpWithCode(opID, opType, code, err.Error())
}

// classifyError maps dispatch errors to structured error codes.
func classifyError(err error) string {
	var nfe *dispatch.NotFoundError
	if errors.As(err, &nfe) {
		return "not_found"
	}
	var ambErr *index.AmbiguousSymbolError
	if errors.As(err, &ambErr) {
		return "ambiguous_symbol"
	}
	return classifyErrorMsg(err.Error())
}

// classifyErrorMsg classifies an error message string into a structured code.
func classifyErrorMsg(msg string) string {
	switch {
	case strings.Contains(msg, "not found"):
		return "not_found"
	case strings.Contains(msg, "is ambiguous"):
		return "ambiguous_symbol"
	case strings.Contains(msg, "ambiguous"):
		return "ambiguous_match"
	case strings.Contains(msg, "no such file"):
		return "file_not_found"
	case strings.Contains(msg, "outside repo root"):
		return "outside_repo"
	case strings.Contains(msg, "hash mismatch"):
		return "hash_mismatch"
	case strings.Contains(msg, "mutually exclusive"):
		return "invalid_mode"
	default:
		return "command_error"
	}
}
