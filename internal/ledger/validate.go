package ledger

import "fmt"

// Validate checks that a Ledger satisfies the documented invariants.
//
//   - Version must match SchemaVersion.
//   - Every Site must have a non-empty SiteKey, a recognized Tier + Role,
//     and a Role allowed by its Tier per the Tier × Role matrix.
//   - ReasonCode must be allowed by the Tier per the Tier × ReasonCode matrix.
//   - For Command=="rename", Rename.Edits keys must equal the SiteKey set of
//     Sites exactly (bijection, no orphans on either side).
//   - Counts must equal per-tier Site counts. Total must equal len(Sites).
func Validate(l *Ledger) error {
	if l == nil {
		return fmt.Errorf("ledger: nil")
	}
	if l.Version != SchemaVersion {
		return fmt.Errorf("ledger: version mismatch: got %q want %q", l.Version, SchemaVersion)
	}

	siteKeys := make(map[string]struct{}, len(l.Sites))
	tierCounts := make(map[Tier]int)
	for i, s := range l.Sites {
		if s.SiteKey == "" {
			return fmt.Errorf("ledger: site[%d] missing SiteKey", i)
		}
		if _, dup := siteKeys[s.SiteKey]; dup {
			return fmt.Errorf("ledger: duplicate SiteKey %q at site[%d]", s.SiteKey, i)
		}
		siteKeys[s.SiteKey] = struct{}{}
		if !roleAllowedForTier(s.Tier, s.Role) {
			return fmt.Errorf("ledger: site[%d] role %q not allowed for tier %q", i, s.Role, s.Tier)
		}
		if !reasonAllowedForTier(s.Tier, s.ReasonCode) {
			return fmt.Errorf("ledger: site[%d] reason_code %q not allowed for tier %q", i, s.ReasonCode, s.Tier)
		}
		tierCounts[s.Tier]++
	}

	for t, n := range tierCounts {
		if l.Counts[t] != n {
			return fmt.Errorf("ledger: counts[%q]=%d, sites have %d", t, l.Counts[t], n)
		}
	}
	for t, n := range l.Counts {
		if n != tierCounts[t] {
			return fmt.Errorf("ledger: counts[%q]=%d, sites have %d", t, n, tierCounts[t])
		}
	}
	if l.Total != len(l.Sites) {
		return fmt.Errorf("ledger: total=%d, len(sites)=%d", l.Total, len(l.Sites))
	}

	if l.Command == CommandRename {
		if l.Rename == nil {
			return fmt.Errorf("ledger: rename command missing Rename payload")
		}
		if l.Rename.Edits == nil {
			return fmt.Errorf("ledger: rename command missing Edits")
		}
		editableKeys := make(map[string]struct{})
		for i := range l.Sites {
			if IsEditableTier(l.Sites[i].Tier) {
				editableKeys[l.Sites[i].SiteKey] = struct{}{}
			}
		}
		for k := range l.Rename.Edits {
			if _, ok := editableKeys[k]; !ok {
				// Either a key for a non-editable site, or a site that doesn't exist.
				if _, isSite := siteKeys[k]; isSite {
					return fmt.Errorf("ledger: edit for non-editable site %q (its tier cannot carry edits)", k)
				}
				return fmt.Errorf("ledger: orphan edit key %q has no matching site", k)
			}
		}
		for k := range editableKeys {
			if _, ok := l.Rename.Edits[k]; !ok {
				return fmt.Errorf("ledger: editable site %q has no matching edit", k)
			}
		}
	}

	return nil
}

// IsEditableTier reports whether sites in this tier carry an Edit entry in
// a rename ledger. Shadowed and lexical-noise sites are classified but
// intentionally not edited.
func IsEditableTier(t Tier) bool {
	switch t {
	case TierDefinite, TierAmbiguousDispatch, TierAmbiguousImport:
		return true
	}
	return false
}

// roleAllowedForTier encodes the Tier × Role validation matrix.
func roleAllowedForTier(t Tier, r Role) bool {
	switch t {
	case TierDefinite:
		return r == RoleDef || r == RoleDecl || r == RoleCall || r == RoleRef
	case TierAmbiguousDispatch:
		return r == RoleCall || r == RoleRef
	case TierAmbiguousImport:
		return r == RoleCall || r == RoleRef || r == RoleDecl
	case TierShadowed:
		return r == RoleRef || r == RoleCall
	case TierLexicalNoise:
		return r == RoleComment || r == RoleString || r == RoleField || r == RoleUnrelatedDecl
	}
	return false
}

// reasonAllowedForTier encodes the Tier × ReasonCode validation matrix.
func reasonAllowedForTier(t Tier, rc ReasonCode) bool {
	switch t {
	case TierDefinite:
		return rc == ReasonResolvedDef || rc == ReasonResolvedDecl ||
			rc == ReasonResolvedCall || rc == ReasonResolvedRef
	case TierAmbiguousDispatch:
		return rc == ReasonUnresolvedDispatch
	case TierAmbiguousImport:
		return rc == ReasonUnresolvedImport
	case TierShadowed:
		return rc == ReasonScopeShadow
	case TierLexicalNoise:
		return rc == ReasonInsideComment || rc == ReasonInsideString ||
			rc == ReasonStructFieldKey || rc == ReasonUnrelatedDeclSameName
	}
	return false
}
