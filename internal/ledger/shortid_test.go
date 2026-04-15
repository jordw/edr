package ledger

import "testing"

func TestAssignShortIDs(t *testing.T) {
	l := &Ledger{
		Sites: []Site{
			// Intentionally out-of-order input; AssignShortIDs sorts by file+byte.
			{File: "b.c", ByteRange: [2]int{10, 14}, Tier: TierDefinite},
			{File: "a.c", ByteRange: [2]int{20, 24}, Tier: TierShadowed},
			{File: "a.c", ByteRange: [2]int{10, 14}, Tier: TierDefinite},
			{File: "a.c", ByteRange: [2]int{30, 34}, Tier: TierLexicalNoise},
		},
	}
	l.AssignShortIDs()

	// After sort: a.c:10, a.c:20, a.c:30, b.c:10
	// In tier-order assignment: definite first (a.c:10, b.c:10 → d1, d2),
	// then shadowed (a.c:20 → s1), then lexical-noise (a.c:30 → n1).
	want := []struct {
		file  string
		short string
	}{
		{"a.c", "d1"},
		{"a.c", "s1"},
		{"a.c", "n1"},
		{"b.c", "d2"},
	}
	for i, w := range want {
		if l.Sites[i].File != w.file {
			t.Errorf("site[%d] file=%q want %q", i, l.Sites[i].File, w.file)
		}
		if l.Sites[i].ShortID != w.short {
			t.Errorf("site[%d] short=%q want %q", i, l.Sites[i].ShortID, w.short)
		}
	}
}

func TestAssignShortIDs_Deterministic(t *testing.T) {
	make := func() *Ledger {
		return &Ledger{
			Sites: []Site{
				{File: "a.c", ByteRange: [2]int{100, 104}, Tier: TierDefinite},
				{File: "a.c", ByteRange: [2]int{50, 54}, Tier: TierDefinite},
			},
		}
	}
	l1, l2 := make(), make()
	l1.AssignShortIDs()
	l2.AssignShortIDs()
	for i := range l1.Sites {
		if l1.Sites[i].ShortID != l2.Sites[i].ShortID {
			t.Errorf("non-deterministic: run1=%q run2=%q", l1.Sites[i].ShortID, l2.Sites[i].ShortID)
		}
	}
}
