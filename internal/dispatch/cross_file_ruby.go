package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
	scopestore "github.com/jordw/edr/internal/scope/store"
)

// rubyCrossFileSpans is the Ruby branch of
// scopeAwareCrossFileSpans. Uses canonical-DeclID matching via the
// namespace abstraction; falls through (returns empty map) when no
// cross-file matches are found, so the dispatch switch can defer
// to the generic ref-filtering path for cases this resolver
// doesn't model yet.
func rubyCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, []string, bool) {
	if !strings.HasSuffix(strings.ToLower(sym.File), ".rb") {
		return nil, nil, false
	}
	out := map[string][]span{}
	resolver := namespace.NewRubyResolver(db.Root())
	canonical := resolver.CanonicalPath(sym.File)
	targetRes := resolver.Result(sym.File)
	if canonical == "" || targetRes == nil {
		return nil, nil, false
	}

	var target *scope.Decl
	for i := range targetRes.Decls {
		d := &targetRes.Decls[i]
		if d.Name != sym.Name {
			continue
		}
		if d.Span.StartByte >= sym.StartByte && d.Span.EndByte <= sym.EndByte {
			target = d
			break
		}
	}
	if target == nil {
		return out, nil, true
	}

	if sym.Receiver != "" {
		var extraCandidates []string
		if refs, err := db.FindSemanticReferences(ctx, sym.Receiver, sym.File); err == nil {
			seen := map[string]struct{}{sym.File: {}}
			for _, r := range refs {
				if _, ok := seen[r.File]; ok {
					continue
				}
				seen[r.File] = struct{}{}
				extraCandidates = append(extraCandidates, r.File)
			}
		}
		EmitOverrideSpans(out, rubyResolverDeps{r: resolver}, sym, extraCandidates...)
	}

	candidates := map[string]bool{}
	if refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File); err == nil {
		for _, r := range refs {
			if r.File != sym.File {
				candidates[r.File] = true
			}
		}
	}
	for _, sib := range resolver.SamePackageFiles(sym.File) {
		candidates[sib] = true
	}

	isMethod := sym.Receiver != ""
	// Rails-style autoloading: callers don't `require_relative` the
	// def file, so the import-graph filter above admits nothing. For
	// method renames we have a discriminator (the receiver type), so
	// it's safe to widen candidates to every .rb file in the repo
	// and rely on per-ref disambiguation below (acceptableTypes +
	// dotBaseIdentBefore) to filter FPs. Top-level renames stay on
	// the import-graph path — they have no receiver discriminator,
	// so widening would rewrite same-named bare calls indiscriminately.
	if isMethod {
		for _, p := range allRubyFiles(db.Root(), db.EdrDir()) {
			if p != sym.File {
				candidates[p] = true
			}
		}
	}
	acceptableTypes := map[string]bool{}
	if sym.Receiver != "" {
		acceptableTypes[sym.Receiver] = true
		for _, t := range namespace.RbRelatedTypes(resolver.Source(sym.File), sym.Receiver) {
			acceptableTypes[t] = true
		}
	}

	// Other-class detection: walk every candidate (and the def file) for
	// method decls of the same name whose enclosing class is NOT in
	// acceptableTypes. Drives two behaviors:
	//   - looseReceiver=true (no other classes) — admit obj.method calls
	//     even when the receiver type can't be inferred. Without this the
	//     instance-method form `@var.method` is silently skipped because
	//     Ruby has no static type info for ivars/locals.
	//   - looseReceiver=false (collisions exist) — keep the strict
	//     receiver-type filter and emit a warning naming the other classes.
	//     The user has to verify obj.method rewrites manually.
	otherClasses := rbDefiningOtherClasses(resolver, sym, candidates, acceptableTypes)
	looseReceiver := isMethod && len(otherClasses) == 0
	knownClassNames := map[string]bool{}
	for _, c := range otherClasses {
		knownClassNames[c] = true
	}

	pop := namespace.RubyPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		// Methods don't live at file scope so their names aren't in
		// the namespace. Admit every candidate when renaming a
		// method and rely on per-ref disambiguation below.
		if !isMethod && !ns.Matches(sym.Name, target.ID) {
			continue
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}
		src := resolver.Source(cand)
		var varTypes map[string]string
		if isMethod {
			varTypes = buildVarTypes(candRes, src)
		}

		// File-scope decls whose ID matches the target (the
		// canonical-path merge brings prototypes / siblings here).
		for _, d := range candRes.Decls {
			if d.ID != target.ID {
				continue
			}
			if d.Kind == scope.KindImport {
				continue
			}
			out[cand] = append(out[cand], span{
				start: d.Span.StartByte,
				end:   d.Span.EndByte,
			})
		}

		for _, ref := range candRes.Refs {
			if ref.Name != sym.Name {
				continue
			}
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					if local.ID != target.ID {
						continue
					}
				}
			}
			// Property-access handling. For method renames we accept
			// `obj.method` when obj's type matches acceptableTypes.
			// Span stays identifier-only.
			if ref.Binding.Reason == "property_access" && ref.Span.StartByte > 0 && len(src) > 0 {
				prev := src[ref.Span.StartByte-1]
				isDot := prev == '.'
				isOther := (ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == '-' && prev == '>') ||
					(ref.Span.StartByte >= 2 && src[ref.Span.StartByte-2] == ':' && prev == ':')
				if isOther {
					continue
				}
				if isDot {
					if !isMethod {
						continue
					}
					baseIdent := dotBaseIdentBefore(src, ref.Span.StartByte)
					if baseIdent == "" {
						continue
					}
					if looseReceiver {
						// No other class defines this method — admit obj.method
						// regardless of receiver type, but still skip explicit
						// class-qualified calls to a different class (e.g.,
						// SomeOtherClass.method on a Session#method rename).
						if knownClassNames[baseIdent] && !acceptableTypes[baseIdent] {
							continue
						}
					} else {
						if !acceptableTypes[varTypes[baseIdent]] && !acceptableTypes[baseIdent] {
							continue
						}
					}
				}
			}
			out[cand] = append(out[cand], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
			})
		}
	}

	var warnings []string
	if isMethod && len(otherClasses) > 0 {
		sort.Strings(otherClasses)
		warnings = append(warnings, fmt.Sprintf(
			"method %q is also defined on class(es) [%s]; Ruby has no static type info for `obj.%s` call sites, so only calls where the receiver is provably %s (or related) were rewritten — review obj.%s call sites manually before committing",
			sym.Name, strings.Join(otherClasses, ", "), sym.Name, sym.Receiver, sym.Name))
	}
	return out, warnings, true
}

// rbDefiningOtherClasses returns the names of classes (other than
// sym.Receiver and its related hierarchy) that define a method named
// sym.Name. Used to decide whether obj.method calls can be rewritten
// loosely (no other class has this method, so any obj.method must
// be a call to ours) or must be filtered strictly (multiple classes
// define this method, so unresolved-receiver calls are ambiguous).
func rbDefiningOtherClasses(resolver *namespace.RubyResolver, sym *index.SymbolInfo, candidates map[string]bool, acceptable map[string]bool) []string {
	if sym.Receiver == "" {
		return nil
	}
	seen := map[string]bool{}
	visit := func(file string) {
		res := resolver.Result(file)
		src := resolver.Source(file)
		if res == nil || len(src) == 0 {
			return
		}
		hier := namespace.RbFindClassHierarchy(src)
		for _, d := range res.Decls {
			if d.Name != sym.Name {
				continue
			}
			if d.Kind != scope.KindMethod && d.Kind != scope.KindFunction {
				continue
			}
			for _, h := range hier {
				if h.BodyStart <= d.Span.StartByte && d.Span.EndByte <= h.BodyEnd {
					if !acceptable[h.Name] {
						seen[h.Name] = true
					}
					break
				}
			}
		}
	}
	visit(sym.File)
	for f := range candidates {
		visit(f)
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

func ruby_ext_matches(file string, exts []string) bool {
	e := strings.ToLower(file)
	for _, ext := range exts {
		if strings.HasSuffix(e, ext) {
			return true
		}
	}
	return false
}

// allRubyFiles enumerates every .rb file under root. Prefers the
// persisted scope index (cheap, parsed) and falls back to a walk
// when the index isn't built.
func allRubyFiles(root, edrDir string) []string {
	if idx, _ := scopestore.Load(edrDir); idx != nil {
		results := idx.AllResults()
		out := make([]string, 0, len(results))
		for rel := range results {
			if !strings.HasSuffix(strings.ToLower(rel), ".rb") {
				continue
			}
			out = append(out, filepath.Join(root, rel))
		}
		if len(out) > 0 {
			return out
		}
	}
	skipDirs := map[string]bool{".git": true, ".edr": true, "vendor": true, ".bundle": true, "coverage": true, "tmp": true, "log": true, "node_modules": true}
	var out []string
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(path), ".rb") {
			out = append(out, path)
		}
		return nil
	})
	return out
}


// rubyResolverDeps adapts RubyResolver to the dispatch HierarchyDeps
// interface used by EmitOverrideSpans.
type rubyResolverDeps struct {
	r *namespace.RubyResolver
}

func (d rubyResolverDeps) Result(file string) *scope.Result { return d.r.Result(file) }
func (d rubyResolverDeps) SamePackageFiles(file string) []string {
	return d.r.SamePackageFiles(file)
}
func (d rubyResolverDeps) FilesForImport(spec, importingFile string) []string {
	return d.r.FilesForImport(spec, importingFile)
}

// ImportSpec returns the bare path string from a Ruby require/
// require_relative decl. The Ruby builder stamps Signature with the
// path as the module portion; the imported binding name is unused
// here since require returns Object#require's result, not a name.
func (d rubyResolverDeps) ImportSpec(decl *scope.Decl) string {
	module, _ := SplitImportSignature(decl)
	return module
}
