package ledger

import (
	"bytes"
	"encoding/json"
)

// RenderJSON marshals a Ledger to canonical JSON. Always full-fidelity: all
// sites are included, regardless of plain-render truncation.
func RenderJSON(l *Ledger) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(l); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// RenderJSONHeader returns a single-line JSON object with the summary fields:
// command, target, scope, counts, total, truncated, next_actions, warnings.
// Clients that want the full site list read the body via RenderJSON.
func RenderJSONHeader(l *Ledger) ([]byte, error) {
	var truncated map[Tier]int
	if l.Render != nil {
		truncated = l.Render.Truncated
	}
	h := struct {
		Version     string       `json:"version"`
		Command     Command      `json:"command"`
		Target      Target       `json:"target"`
		Scope       Scope        `json:"scope"`
		Counts      map[Tier]int `json:"counts"`
		Total       int          `json:"total"`
		Truncated   map[Tier]int `json:"truncated,omitempty"`
		NextActions []Action     `json:"next_actions"`
		Warnings    []Warning    `json:"warnings,omitempty"`
	}{
		Version:     l.Version,
		Command:     l.Command,
		Target:      l.Target,
		Scope:       l.Scope,
		Counts:      l.Counts,
		Total:       l.Total,
		Truncated:   truncated,
		NextActions: l.NextActions,
		Warnings:    l.Warnings,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(h); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
