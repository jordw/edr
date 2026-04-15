package ledger

import "testing"

func TestSiteKey_Deterministic(t *testing.T) {
	k1 := ComputeSiteKey("tools/foo.c", 100, 104, TierDefinite, []byte("init"))
	k2 := ComputeSiteKey("tools/foo.c", 100, 104, TierDefinite, []byte("init"))
	if k1 != k2 {
		t.Fatalf("same inputs, different keys: %s vs %s", k1, k2)
	}
	if len(k1) != 64 {
		t.Fatalf("expected 64-char hex, got %d chars: %s", len(k1), k1)
	}
}

func TestSiteKey_DifferentiatesEveryInput(t *testing.T) {
	base := ComputeSiteKey("tools/foo.c", 100, 104, TierDefinite, []byte("init"))
	cases := []struct {
		name string
		got  string
	}{
		{"different file", ComputeSiteKey("tools/bar.c", 100, 104, TierDefinite, []byte("init"))},
		{"different start", ComputeSiteKey("tools/foo.c", 101, 104, TierDefinite, []byte("init"))},
		{"different end", ComputeSiteKey("tools/foo.c", 100, 105, TierDefinite, []byte("init"))},
		{"different tier", ComputeSiteKey("tools/foo.c", 100, 104, TierShadowed, []byte("init"))},
		{"different bytes", ComputeSiteKey("tools/foo.c", 100, 104, TierDefinite, []byte("ini2"))},
	}
	for _, c := range cases {
		if c.got == base {
			t.Errorf("%s: expected different key, got same (%s)", c.name, c.got)
		}
	}
}

// TestSiteKey_CanonicalFormat pins the exact output for a known input so any
// future change to the serialization format breaks this test loudly.
func TestSiteKey_CanonicalFormat(t *testing.T) {
	got := ComputeSiteKey("a.go", 0, 4, TierDefinite, []byte("test"))
	want := "f5496a6760cab675772e8dd344110b609aeb053028381a2332f28ca9e1047090"
	if got != want {
		t.Fatalf("canonical SiteKey changed\n got: %s\nwant: %s\n(if intentional, bump SiteKeyVersion)", got, want)
	}
}
