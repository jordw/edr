package output

// wire.go transforms the internal envelope into compact wire-format JSON.
//
// Internal code uses readable field names ("content", "symbol", "op_id").
// The wire format uses short names ("c", "sym", "id") to minimize tokens.
// This is the only place that mapping is defined.

// renameKeys maps internal field names to short wire-format names.
// Fields not in this map pass through unchanged.
var renameKeys = map[string]string{
	// envelope level (handled by marshalEnvelope)
	"command":        "cmd",
	"schema_version": "", // drop

	// op level
	"op_id":          "id",
	"content":        "c",
	"symbol":         "sym",
	"error_code":     "ec",
	"truncated":      "trunc",
	"total_matches":  "n",
	"total_files":    "nf",
	"total_lines":    "nl",
	"matches":        "m",
	"lines_changed":  "lc",
	"lines_added":    "la",
	"lines_removed":  "lr",
	"old_size":       "os",
	"new_size":       "ns",
}

// dropFields are removed entirely from ops — noise that agents don't need.
var dropFields = map[string]bool{
	"size":           true, // read metadata — agent has content
	"mtime":          true, // read metadata
	"kind":           true, // search kind (symbol/text) — not actionable
	"diff_available": true, // redundant with diff presence
	"score":          true, // internal ranking
	"snippet":        true, // context bloat (use --context explicitly)
	"column":         true, // byte offset — agents use line+text
	"count":          true, // per-file match count — redundant with array length
}

// dropIfDefault are removed when they equal their default value.
var dropIfDefault = map[string]any{
	"truncated":   false,
	"signatures":  false,
	"destructive": false,
	"session":     "new", // only "unchanged" is actionable
}

// transformOp applies renames, drops, and default-stripping to an op map.
// Operates in place.
func transformOp(op Op) {
	// Drop noise fields
	for key := range dropFields {
		delete(op, key)
	}

	// Drop default-valued fields
	for key, defVal := range dropIfDefault {
		if v, ok := op[key]; ok && v == defVal {
			delete(op, key)
		}
	}

	// Rename keys
	for oldKey, newKey := range renameKeys {
		if v, ok := op[oldKey]; ok {
			delete(op, oldKey)
			if newKey != "" {
				op[newKey] = v
			}
		}
	}

	// Recurse into nested structures (search results)
	transformNestedMatches(op, "matches") // flat matches
	transformNestedMatches(op, "m")       // already renamed

	// File-grouped search results
	if files, ok := op["files"]; ok {
		if fs, isList := files.([]any); isList {
			for _, f := range fs {
				if fm, isMap := f.(map[string]any); isMap {
					delete(fm, "count")
					transformNestedMatches(fm, "matches")
					transformNestedMatches(fm, "m")
					// Rename matches → m in file groups
					if v, ok := fm["matches"]; ok {
						delete(fm, "matches")
						fm["m"] = v
					}
				}
			}
		}
	}

	// Rename matches → m at top level (after nested transform)
	if v, ok := op["matches"]; ok {
		delete(op, "matches")
		op["m"] = v
	}
}

// transformNestedMatches strips noise from match entries inside an array.
func transformNestedMatches(parent map[string]any, key string) {
	arr, ok := parent[key]
	if !ok {
		return
	}
	ms, isList := arr.([]any)
	if !isList {
		return
	}
	for _, m := range ms {
		if mm, isMap := m.(map[string]any); isMap {
			for field := range dropFields {
				delete(mm, field)
			}
			// Rename symbol → sym inside match entries
			if v, ok := mm["symbol"]; ok {
				delete(mm, "symbol")
				mm["sym"] = v
			}
		}
	}
}
