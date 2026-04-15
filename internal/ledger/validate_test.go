package ledger

import (
	"strings"
	"testing"
)

func baseLedger() *Ledger {
	s := Site{
		File:       "foo.c",
		ByteRange:  [2]int{10, 14},
		Line:       5,
		Tier:       TierDefinite,
		Role:       RoleDef,
		ReasonCode: ReasonResolvedDef,
	}
	s.SiteKey = ComputeSiteKey(s.File, s.ByteRange[0], s.ByteRange[1], s.Tier, []byte("init"))
	return &Ledger{
		Version: SchemaVersion,
		Command: CommandRename,
		Target:  Target{Name: "init", File: "foo.c", Line: 5, Kind: "function"},
		Scope:   ScopeSameFile,
		Sites:   []Site{s},
		Counts:  map[Tier]int{TierDefinite: 1},
		Total:   1,
		Rename: &RenamePayload{
			From:  "init",
			To:    "init_v2",
			Edits: map[string]Edit{s.SiteKey: {OldBytes: "init", Replacement: "init_v2"}},
		},
	}
}

func TestValidate_OK(t *testing.T) {
	if err := Validate(baseLedger()); err != nil {
		t.Fatalf("valid ledger failed: %v", err)
	}
}

func TestValidate_VersionMismatch(t *testing.T) {
	l := baseLedger()
	l.Version = "ledger/0"
	if err := Validate(l); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version error, got %v", err)
	}
}

func TestValidate_RoleNotAllowedForTier(t *testing.T) {
	l := baseLedger()
	l.Sites[0].Role = RoleComment // comment not allowed for TierDefinite
	if err := Validate(l); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected role error, got %v", err)
	}
}

func TestValidate_ReasonNotAllowedForTier(t *testing.T) {
	l := baseLedger()
	l.Sites[0].ReasonCode = ReasonScopeShadow // shadow not allowed for TierDefinite
	if err := Validate(l); err == nil || !strings.Contains(err.Error(), "reason_code") {
		t.Fatalf("expected reason error, got %v", err)
	}
}

func TestValidate_OrphanEdit(t *testing.T) {
	l := baseLedger()
	l.Rename.Edits["ghost"] = Edit{OldBytes: "x", Replacement: "y"}
	if err := Validate(l); err == nil || !strings.Contains(err.Error(), "orphan edit") {
		t.Fatalf("expected orphan edit error, got %v", err)
	}
}

func TestValidate_OrphanSite(t *testing.T) {
	l := baseLedger()
	delete(l.Rename.Edits, l.Sites[0].SiteKey)
	if err := Validate(l); err == nil {
		t.Fatalf("expected orphan site error, got nil")
	}
}

func TestValidate_CountsMismatch(t *testing.T) {
	l := baseLedger()
	l.Counts[TierDefinite] = 99
	if err := Validate(l); err == nil || !strings.Contains(err.Error(), "counts") {
		t.Fatalf("expected counts mismatch, got %v", err)
	}
}

func TestValidate_TotalMismatch(t *testing.T) {
	l := baseLedger()
	l.Total = 42
	if err := Validate(l); err == nil || !strings.Contains(err.Error(), "total") {
		t.Fatalf("expected total mismatch, got %v", err)
	}
}

func TestValidate_DuplicateSiteKey(t *testing.T) {
	l := baseLedger()
	l.Sites = append(l.Sites, l.Sites[0])
	l.Counts[TierDefinite] = 2
	l.Total = 2
	if err := Validate(l); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}
