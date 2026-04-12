package cmd

import (
	"context"
	"testing"

	"github.com/jordw/edr/internal/dispatch"
	"github.com/jordw/edr/internal/idx"
	"github.com/jordw/edr/internal/index"
)

func TestCallersViaDispatch(t *testing.T) {
	root := "/Users/jordw/Documents/GitHub/edr"
	db := index.NewOnDemand(root)
	defer db.Close()

	dispatch.Dispatch(t.Context(), db, "index", nil, nil)
	idx.InvalidateSymbolCache()

	// Now try focus with expand
	ctx := context.Background()
	result, err := dispatch.Dispatch(ctx, db, "focus", []string{"BuildImportGraph"}, map[string]any{"expand": "both"})
	if err != nil {
		t.Fatal(err)
	}
	if m, ok := result.(map[string]any); ok {
		t.Logf("keys: %v", mapKeys(m))
		if callers, ok := m["callers"]; ok {
			t.Logf("callers: %v", callers)
		} else {
			t.Error("no callers key in result")
		}
		if deps, ok := m["deps"]; ok {
			t.Logf("deps: %v", deps)
		} else {
			t.Error("no deps key in result")
		}
	} else {
		t.Errorf("unexpected result type: %T", result)
	}
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m { keys = append(keys, k) }
	return keys
}
