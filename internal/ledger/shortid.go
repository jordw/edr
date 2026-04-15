package ledger

import (
	"fmt"
	"sort"
)

// AssignShortIDs populates each site's ShortID in canonical tier+position
// order. Must be called after Sites is finalized (post-filter) and before
// rendering. Idempotent.
func (l *Ledger) AssignShortIDs() {
	// Canonical sort: file bytes, then byte_start, then byte_end.
	sort.Slice(l.Sites, func(i, j int) bool {
		a, b := l.Sites[i], l.Sites[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.ByteRange[0] != b.ByteRange[0] {
			return a.ByteRange[0] < b.ByteRange[0]
		}
		return a.ByteRange[1] < b.ByteRange[1]
	})

	counters := make(map[Tier]int)
	for _, t := range TierOrder {
		counters[t] = 0
	}
	// Two-pass: assign in tier order for predictable ShortID letters.
	// Sites are already sorted by file+byte, so within a tier the order is
	// deterministic; we just need to number them correctly.
	//
	// We walk TierOrder as the outer loop and assign IDs for each tier in the
	// sorted site order.
	for _, tier := range TierOrder {
		n := 0
		for i := range l.Sites {
			if l.Sites[i].Tier != tier {
				continue
			}
			n++
			l.Sites[i].ShortID = fmt.Sprintf("%s%d", shortIDLetter(tier), n)
		}
		counters[tier] = n
	}
}
