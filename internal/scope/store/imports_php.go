package store

import (
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/scope"
)

// resolveImportsPHP is Phase 1 of the cross-file import graph for PHP.
// After per-file parsing, it rewrites Ref.Binding so that refs to local
// KindImport decls (stamped by `use Foo\Bar;`) point at the actual
// exported Decl in the source file — when that source file is
// repo-internal.
//
// Resolution model (v1):
//
//   - Each PHP file declares zero or one namespace via `namespace Foo\Bar;`.
//     The php builder stamps the full backslash-qualified path on a
//     KindNamespace decl's Signature so this pass can recover it. Files
//     with no namespace clause are treated as "global namespace" (path "").
//
//   - Each file-scope class / interface / trait / enum / function / const
//     is publicly visible (PHP's implicit-public semantics) and indexed
//     by its FQN: "<ns>\\<Name>" when the file has a namespace, else
//     "<Name>". The php builder sets Decl.Exported=true on these.
//
//   - Each KindImport decl carries Signature = "[<prefix>:]<modulePath>\x00<origName>".
//     FQN = "<modulePath>\\<origName>" (modulePath stripped of any
//     leading `\`). If that FQN matches an exported decl's FQN, the ref
//     to the import is rewritten to that target, Reason="import_export".
//
// Scope (v1 limitations):
//
//   - `use function …` / `use const …` imports are v1.5: the resolver
//     sees the "function:" / "const:" prefix and currently skips them,
//     since the builder does not yet differentiate function-namespace
//     from value-namespace exports. Refs to such imports stay bound to
//     the local Import decl (honest "external" answer).
//
//   - Block-form files with multiple `namespace A { … } namespace B { … }`
//     are handled as "first namespace wins" — in practice this is rare
//     enough that v1 accepts the limitation. Statement-form files (one
//     `namespace Foo;` at the top) are the common shape and work
//     correctly.
//
//   - Refs to external namespaces (e.g. `use Vendor\Library\X;` where no
//     file in the repo declares that namespace) stay bound to the local
//     Import decl.
func resolveImportsPHP(parsed []parsedFile) {
	if len(parsed) == 0 {
		return
	}

	// Filter to PHP files.
	byRel := make(map[string]*scope.Result, len(parsed))
	for _, p := range parsed {
		if !isPHPLike(p.rel) {
			continue
		}
		byRel[p.rel] = p.result
	}
	if len(byRel) == 0 {
		return
	}

	// Determine each file's namespace path. Use the first KindNamespace
	// decl with a non-empty Signature (block-form with multiple
	// namespaces is a v1 limitation — first wins).
	fileNS := make(map[string]string, len(byRel))
	for rel, r := range byRel {
		fileNS[rel] = firstPHPNamespace(r)
	}

	// Build FQN → (sourceRel, targetDeclID) for all exported top-level
	// decls across all PHP files. Entries prefer the value-namespace
	// shadow when a decl was dual-emitted in both NSValue and NSType;
	// the within-file merge has already unified their DeclIDs, so either
	// hit is correct — but we deduplicate by keeping the first.
	type fqnTarget struct {
		sourceRel string
		declID    scope.DeclID
	}
	fqnIndex := make(map[string]fqnTarget)
	for rel, r := range byRel {
		ns := fileNS[rel]
		for i := range r.Decls {
			d := &r.Decls[i]
			if !d.Exported {
				continue
			}
			if d.Kind == scope.KindImport || d.Kind == scope.KindNamespace {
				continue
			}
			fqn := composeFQN(ns, d.Name)
			if _, exists := fqnIndex[fqn]; !exists {
				fqnIndex[fqn] = fqnTarget{sourceRel: rel, declID: d.ID}
			}
		}
	}
	if len(fqnIndex) == 0 {
		return
	}

	// Per-file: resolve each KindImport decl's target, keyed by decl.ID.
	// Only populated for imports whose target is repo-internal.
	targets := make(map[scope.DeclID]scope.DeclID)

	for _, p := range parsed {
		if !isPHPLike(p.rel) {
			continue
		}
		for i := range p.result.Decls {
			d := &p.result.Decls[i]
			if d.Kind != scope.KindImport {
				continue
			}
			if d.Signature == "" {
				continue
			}
			sig := d.Signature
			// Skip `use function` / `use const` — v1.5.
			if strings.HasPrefix(sig, "function:") || strings.HasPrefix(sig, "const:") {
				continue
			}
			modulePath, orig := parseImportSignature(sig)
			if orig == "" {
				continue
			}
			// Strip a single leading `\` (absolute-form `use \Foo\Bar;`).
			modulePath = strings.TrimPrefix(modulePath, "\\")
			fqn := composeFQN(modulePath, orig)
			if tgt, ok := fqnIndex[fqn]; ok {
				targets[d.ID] = tgt.declID
			}
		}
	}

	if len(targets) == 0 {
		return
	}

	// Rewrite refs whose Binding.Decl is a local Import decl with a
	// resolved target.
	for _, p := range parsed {
		if !isPHPLike(p.rel) {
			continue
		}
		// Quick-exit: does this file have any Import decls with targets?
		hasAny := false
		for i := range p.result.Decls {
			if p.result.Decls[i].Kind != scope.KindImport {
				continue
			}
			if _, ok := targets[p.result.Decls[i].ID]; ok {
				hasAny = true
				break
			}
		}
		if !hasAny {
			continue
		}
		for i := range p.result.Refs {
			ref := &p.result.Refs[i]
			if ref.Binding.Kind != scope.BindResolved {
				continue
			}
			tgtID, ok := targets[ref.Binding.Decl]
			if !ok {
				continue
			}
			ref.Binding.Decl = tgtID
			ref.Binding.Reason = "import_export"
		}
	}
}

// firstPHPNamespace returns the file's namespace path, recovered from
// the first KindNamespace decl with a non-empty Signature. An empty
// string means the file has no `namespace` clause (global namespace).
func firstPHPNamespace(r *scope.Result) string {
	for i := range r.Decls {
		d := &r.Decls[i]
		if d.Kind == scope.KindNamespace && d.Signature != "" {
			return d.Signature
		}
	}
	return ""
}

// composeFQN joins a namespace path and a decl name with a backslash.
// Empty namespace yields just the name (global-namespace FQN).
func composeFQN(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "\\" + name
}

// isPHPLike reports whether a file's extension is a PHP extension.
func isPHPLike(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".php", ".phtml", ".phps":
		return true
	}
	return false
}
