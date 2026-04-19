package status

import (
	"testing"
)

type fakeReporter struct {
	r Report
}

func (f fakeReporter) Status() Report { return f.r }

func TestAggregate_Empty(t *testing.T) {
	got := Aggregate()
	if len(got) != 0 {
		t.Fatalf("empty input: got %d reports, want 0", len(got))
	}
}

func TestAggregate_SkipsNil(t *testing.T) {
	a := fakeReporter{r: Report{Name: "a", Exists: true}}
	b := fakeReporter{r: Report{Name: "b"}}
	got := Aggregate(a, nil, b, nil)
	if len(got) != 2 {
		t.Fatalf("nil skipping: got %d reports, want 2", len(got))
	}
	if got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("order: got %q,%q, want a,b", got[0].Name, got[1].Name)
	}
}

func TestAggregate_PreservesZeroValues(t *testing.T) {
	// A stack reporting zeros (store missing) should not be elided —
	// only a literally nil Reporter is skipped.
	z := fakeReporter{r: Report{Name: "empty"}}
	got := Aggregate(z)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Exists || got[0].Files != 0 || got[0].Bytes != 0 {
		t.Errorf("zero values mutated: %+v", got[0])
	}
}

func TestAggregate_OrderPreserved(t *testing.T) {
	reps := []Reporter{
		fakeReporter{r: Report{Name: "index"}},
		fakeReporter{r: Report{Name: "scope"}},
		fakeReporter{r: Report{Name: "session"}},
	}
	got := Aggregate(reps...)
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	want := []string{"index", "scope", "session"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("index %d: got %q, want %q", i, got[i].Name, w)
		}
	}
}
