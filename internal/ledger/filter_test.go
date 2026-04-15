package ledger

import "testing"

func TestParsePredicate(t *testing.T) {
	cases := []struct {
		in      string
		want    Predicate
		wantErr bool
	}{
		{"file=tools/**", Predicate{Field: "file", Op: "glob", Value: "tools/**"}, false},
		{"container=Foo/bar", Predicate{Field: "container", Op: "eq", Value: "Foo/bar"}, false},
		{"container~=handle", Predicate{Field: "container", Op: "suffix", Value: "handle"}, false},
		{"unknown=x", Predicate{}, true},
		{"file", Predicate{}, true},
	}
	for _, c := range cases {
		got, err := ParsePredicate(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q: wantErr=%v got err=%v", c.in, c.wantErr, err)
			continue
		}
		if err == nil && got != c.want {
			t.Errorf("%q: got %+v want %+v", c.in, got, c.want)
		}
	}
}

func TestPredicate_Matches(t *testing.T) {
	siteA := Site{
		File: "tools/perf/bench/numa.c",
		Container: []ContainerStep{
			{Kind: "function", Name: "launch"},
		},
	}
	cases := []struct {
		p    Predicate
		site Site
		want bool
	}{
		{Predicate{Field: "file", Op: "glob", Value: "tools/**"}, siteA, true},
		{Predicate{Field: "file", Op: "glob", Value: "kernel/**"}, siteA, false},
		{Predicate{Field: "file", Op: "glob", Value: "tools/perf/*.c"}, siteA, false}, // single-star, won't match nested
		{Predicate{Field: "file", Op: "glob", Value: "tools/**/numa.c"}, siteA, true},
		{Predicate{Field: "container", Op: "eq", Value: "function:launch"}, siteA, true},
		{Predicate{Field: "container", Op: "eq", Value: "function:reset"}, siteA, false},
		{Predicate{Field: "container", Op: "suffix", Value: ":launch"}, siteA, true},
		{Predicate{Field: "file", Op: "glob", Value: "kernel/**,tools/**"}, siteA, true}, // OR list
	}
	for i, c := range cases {
		got := c.p.Matches(&c.site)
		if got != c.want {
			t.Errorf("case %d %+v: got %v want %v", i, c.p, got, c.want)
		}
	}
}

func TestApplyFilter(t *testing.T) {
	l := &Ledger{
		Version: SchemaVersion,
		Command: CommandFindRefs,
		Sites: []Site{
			{File: "tools/a.c", Tier: TierDefinite, SiteKey: "k1"},
			{File: "tools/b.c", Tier: TierDefinite, SiteKey: "k2"},
			{File: "kernel/c.c", Tier: TierShadowed, SiteKey: "k3"},
		},
		Counts: map[Tier]int{TierDefinite: 2, TierShadowed: 1},
		Total:  3,
		Filter: &Filter{
			Predicates: []Predicate{{Field: "file", Op: "glob", Value: "tools/**"}},
		},
	}
	removed := l.ApplyFilter()
	if removed != 1 {
		t.Errorf("removed=%d want 1", removed)
	}
	if l.Total != 2 {
		t.Errorf("total=%d want 2", l.Total)
	}
	if l.ExcludedCounts[TierShadowed] != 1 {
		t.Errorf("excluded shadowed=%d want 1", l.ExcludedCounts[TierShadowed])
	}
	if l.Filter.PreFilterTotal != 3 {
		t.Errorf("pre_filter_total=%d want 3", l.Filter.PreFilterTotal)
	}
}
