package cmdspec

import "testing"

func TestRegistryCompleteness(t *testing.T) {
	// Every command must have a non-empty name and description.
	for _, s := range Registry {
		if s.Name == "" {
			t.Error("spec with empty name")
		}
		if s.Desc == "" {
			t.Errorf("%s: empty description", s.Name)
		}
	}
}

func TestByNameRoundTrip(t *testing.T) {
	for _, s := range Registry {
		got := ByName(s.Name)
		if got != s {
			t.Errorf("ByName(%q) returned different spec", s.Name)
		}
	}
	if ByName("nonexistent") != nil {
		t.Error("ByName should return nil for unknown command")
	}
}

func TestClassificationConsistency(t *testing.T) {
	// Every CatRead command should pass IsRead.
	for _, s := range Registry {
		switch s.Category {
		case CatRead:
			if !IsRead(s.Name) {
				t.Errorf("%s: CatRead but IsRead=false", s.Name)
			}
		case CatWrite:
			if !IsWrite(s.Name) {
				t.Errorf("%s: CatWrite but IsWrite=false", s.Name)
			}
			if !ModifiesState(s.Name) {
				t.Errorf("%s: CatWrite but ModifiesState=false", s.Name)
			}
		case CatGlobalMutate:
			if !IsGlobalMutating(s.Name) {
				t.Errorf("%s: CatGlobalMutate but IsGlobalMutating=false", s.Name)
			}
			if !ModifiesState(s.Name) {
				t.Errorf("%s: CatGlobalMutate but ModifiesState=false", s.Name)
			}
		}
	}
}

func TestBatchKeysNonEmpty(t *testing.T) {
	for name, fn := range map[string]func() map[string]bool{
		"DoBatchKeys":     DoBatchKeys,
		"ReadBatchKeys":   ReadBatchKeys,
		"QueryBatchKeys":  QueryBatchKeys,
		"EditBatchKeys":   EditBatchKeys,
		"WriteBatchKeys":  WriteBatchKeys,
		"RenameBatchKeys": RenameBatchKeys,
	} {
		keys := fn()
		if len(keys) == 0 {
			t.Errorf("%s returned empty set", name)
		}
	}
}

func TestSessionBehaviorFlags(t *testing.T) {
	// Verify specific session behavior expectations.
	tests := []struct {
		name      string
		diffEdit  bool
		deltaRead bool
		bodyTrack bool
	}{
		{"read", false, true, true},
		{"edit", true, false, false},
		{"explore", false, true, true},
		{"search", false, false, true},
		{"map", false, false, false},
		{"write", false, false, false},
	}
	for _, tt := range tests {
		if IsDiffEdit(tt.name) != tt.diffEdit {
			t.Errorf("%s: IsDiffEdit=%v, want %v", tt.name, IsDiffEdit(tt.name), tt.diffEdit)
		}
		if IsDeltaRead(tt.name) != tt.deltaRead {
			t.Errorf("%s: IsDeltaRead=%v, want %v", tt.name, IsDeltaRead(tt.name), tt.deltaRead)
		}
		if IsBodyTrack(tt.name) != tt.bodyTrack {
			t.Errorf("%s: IsBodyTrack=%v, want %v", tt.name, IsBodyTrack(tt.name), tt.bodyTrack)
		}
	}
}

func TestNamesMatchesRegistry(t *testing.T) {
	names := Names()
	if len(names) != len(Registry) {
		t.Errorf("Names() returned %d, Registry has %d", len(names), len(Registry))
	}
}

func TestToolDescsMatchesRegistry(t *testing.T) {
	descs := ToolDescs()
	for _, s := range Registry {
		if descs[s.Name] != s.Desc {
			t.Errorf("%s: ToolDescs mismatch", s.Name)
		}
	}
}
