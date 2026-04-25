package dispatch

import (
	"context"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// jvmBaseIdentBefore returns the identifier immediately before a `.`
// at refStart, or "" if no dotted base. Shared by Java + Kotlin.
func jvmBaseIdentBefore(src []byte, refStart uint32) string {
	if int(refStart) <= 0 || int(refStart) > len(src) {
		return ""
	}
	i := int(refStart) - 1
	if src[i] != '.' {
		return ""
	}
	end := i
	i--
	for i >= 0 {
		c := src[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			i--
			continue
		}
		break
	}
	return string(src[i+1 : end])
}

// jvmCrossFileSpans implements the shared body of javaCrossFileSpans
// and kotlinCrossFileSpans. The two languages have identical shape:
// canonical-DeclID match for class/interface/enum targets; class-
// qualified disambiguation (`Class.method`) for method/field targets.
//
// Pass `getResult`, `getSource`, `samePkg`, and `populator` so the
// caller can inject the language-specific resolver. Same-package
// behavior plus FindSemanticReferences-narrowed candidates produce
// the candidate set.
func jvmCrossFileSpans(
	ctx context.Context,
	db index.SymbolStore,
	sym *index.SymbolInfo,
	getResult func(string) *scope.Result,
	getSource func(string) []byte,
	samePkg func(string) []string,
	populator namespace.Populator,
	resolver namespace.Resolver,
) (map[string][]span, bool) {
	out := map[string][]span{}
	targetRes := getResult(sym.File)
	if targetRes == nil {
		return out, true
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
		return out, true
	}

	// For method targets, also locate the enclosing class decl.
	// Its canonical DeclID lets us disambiguate Class.method
	// property accesses across files.
	//
	// Kotlin companion-object methods have sym.Receiver="Companion"
	// but callers write `Lib.method()`, not `Lib.Companion.method()`.
	// Walk the symbol-index parent chain: if the receiver is
	// "Companion", replace it with the enclosing class name so
	// acceptableTypes matches what callers actually type.
	effectiveReceiver := sym.Receiver
	if effectiveReceiver == "Companion" {
		if syms, err := db.GetSymbolsByFile(ctx, sym.File); err == nil {
			// Locate sym among the file's symbols (same name + byte
			// containment), then resolve its grandparent via the
			// ParentIndex chain.
			for i := range syms {
				s := &syms[i]
				if s.Name != sym.Name || s.Type != "method" {
					continue
				}
				if !(s.StartByte == sym.StartByte && s.EndByte == sym.EndByte) {
					continue
				}
				if s.ParentIndex < 0 || s.ParentIndex >= len(syms) {
					break
				}
				companion := syms[s.ParentIndex]
				if companion.Name != "Companion" {
					break
				}
				if companion.ParentIndex < 0 || companion.ParentIndex >= len(syms) {
					break
				}
				grand := syms[companion.ParentIndex]
				if grand.Name != "" {
					effectiveReceiver = grand.Name
				}
				break
			}
		}
	}
	var targetClass *scope.Decl
	if effectiveReceiver != "" {
		for i := range targetRes.Decls {
			d := &targetRes.Decls[i]
			if d.Name == effectiveReceiver && d.Scope == scope.ScopeID(1) {
				targetClass = d
				break
			}
		}
	}
	// Hierarchy extension: when the target is a method on a class
	// that extends/implements something, calls via the supertype
	// (`Service s = new ServiceImpl(); s.run()`) should also
	// rewrite — polymorphic dispatch to our method. Build the set
	// of acceptable class DeclIDs = {targetClass} ∪ its supertypes
	// as resolved through the target file''s namespace. Names not
	// resolvable via namespace (stdlib, unmodeled) are skipped.
	acceptableClassIDs := map[scope.DeclID]bool{}
	if targetClass != nil {
		acceptableClassIDs[targetClass.ID] = true
		targetNS := namespace.Build(sym.File, targetRes, resolver, populator)
		for _, superName := range targetClass.SuperTypes {
			for _, e := range targetNS.Lookup(superName) {
				acceptableClassIDs[e.DeclID] = true
			}
		}
	}

	candidates := map[string]bool{}
	if refs, err := db.FindSemanticReferences(ctx, sym.Name, sym.File); err == nil {
		for _, r := range refs {
			if r.File != sym.File {
				candidates[r.File] = true
			}
		}
	}
	for _, sib := range samePkg(sym.File) {
		candidates[sib] = true
	}

	for cand := range candidates {
		candRes := getResult(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, populator)
		// Match precondition: either the target itself is in this
		// files namespace (class/enum/etc.), OR the enclosing class
		// is (method/field rename via Class.method).
		classMatch := targetClass != nil && ns.Matches(effectiveReceiver, targetClass.ID)
		nameMatch := ns.Matches(sym.Name, target.ID)
		if !classMatch && !nameMatch {
			continue
		}
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}
		src := getSource(cand)
		// Bounded receiver-type hints: pair each KindVar/KindParam/
		// KindField decl with the type identifier immediately
		// preceding it. Lets us resolve `lib.process()` when `lib`
		// was declared `Lib lib = ...`.
		varTypes := buildVarTypes(candRes, src)
		for _, ref := range candRes.Refs {
			if ref.Name != sym.Name {
				continue
			}
			// Shadow guard: ref bound to a NESTED-scope same-name
			// decl in this file → its a local, not our target.
			if ref.Binding.Kind == scope.BindResolved && ref.Binding.Decl != 0 {
				if local, ok := declByID[ref.Binding.Decl]; ok && local.Name == sym.Name {
					var localScopeKind scope.ScopeKind
					if sid := int(local.Scope) - 1; sid >= 0 && sid < len(candRes.Scopes) {
						localScopeKind = candRes.Scopes[sid].Kind
					}
					if local.Kind != scope.KindImport && localScopeKind != scope.ScopeFile {
						continue
					}
				}
			}
			// Property-access disambiguation for method/field rename:
			// `X.method` is only our target if X resolves to our
			// target's class. Two cases handled:
			//   (a) X is the class name itself (`Lib.method()`).
			//   (b) X is a local variable typed as our class
			//       (`Lib lib = ...; lib.method()`). Resolved via
			//       buildVarTypes — bounded receiver-type hints,
			//       per-file scan pairing var decls with their
			//       preceding type identifier.
			// For class targets (no targetClass), namespace.Matches
			// above already disambiguated.
			if ref.Binding.Reason == "property_access" && targetClass != nil && len(src) > 0 {
				baseIdent := jvmBaseIdentBefore(src, ref.Span.StartByte)
				if baseIdent == "" {
					continue
				}
				classCandidate := baseIdent
				// Case (a) check first: base is itself a class name.
				okA := false
				for _, e := range ns.Lookup(baseIdent) {
					if acceptableClassIDs[e.DeclID] {
						okA = true
						break
					}
				}
				if !okA {
					// Case (b): baseIdent is a local var — look up
					// its type, then verify the type resolves to an
					// acceptable class (target class or a supertype).
					if vt := varTypes[baseIdent]; vt != "" {
						classCandidate = vt
					} else {
						continue
					}
					okB := false
					for _, e := range ns.Lookup(classCandidate) {
						if acceptableClassIDs[e.DeclID] {
							okB = true
							break
						}
					}
					if !okB {
						continue
					}
				}
			}
			// Expand the span back through a leading `.` for
			// property-access refs. The rename pipeline's call-site
			// regex is `\.method\b` when sym.Receiver is set; without
			// the leading dot in the span, the regex would not match
			// inside the narrow identifier-only span.
			startByte := ref.Span.StartByte
			if ref.Binding.Reason == "property_access" && startByte > 0 && len(src) > 0 && src[startByte-1] == '.' {
				startByte--
			}
			out[cand] = append(out[cand], span{
				start: startByte,
				end:   ref.Span.EndByte,
				isDef: false,
			})
		}
	}
	return out, true
}

func javaCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	r := namespace.NewJavaResolver(db.Root())
	out, ok := jvmCrossFileSpans(ctx, db, sym, r.Result, r.Source, r.SamePackageFiles, namespace.JavaPopulator(r), r)
	if !ok {
		return out, ok
	}
	// Hierarchy-aware supplement: if the target is a method on a class
	// that implements/extends something, include the supertype's
	// same-name method decl as a rename target. Solves the Phase 8
	// case where renaming ServiceImpl.run must also rewrite Service.run.
	hSpans := javaHierarchySpans(sym, r)
	for f, spans := range hSpans {
		out[f] = append(out[f], spans...)
	}
	return out, true
}

// javaHierarchySpans returns identifier-level spans for every
// same-name method decl in an interface or parent class that sym's
// enclosing class extends/implements. Returns nil for non-method
// targets or when the target class has no supertypes recorded.
//
// The scan is bounded: we look at same-package siblings and files
// reached by the target file''s imports. We do not chase transitive
// supertype chains (renaming a method on a grandchild class would
// update the direct parent but not the grandparent) — the common
// case is immediate interface implementation, which this covers.
func javaHierarchySpans(sym *index.SymbolInfo, r *namespace.JavaResolver) map[string][]span {
	out := map[string][]span{}
	if sym.Receiver == "" {
		return out // not a method; nothing to do
	}
	targetRes := r.Result(sym.File)
	if targetRes == nil {
		return out
	}
	// Find the enclosing class decl in the target file by name.
	var supers []string
	for i := range targetRes.Decls {
		d := &targetRes.Decls[i]
		if d.Name == sym.Receiver && d.Scope == scope.ScopeID(1) {
			supers = d.SuperTypes
			break
		}
	}
	if len(supers) == 0 {
		return out
	}
	// Candidate files to scan: same-package siblings + anything
	// reachable via the target file''s imports.
	var candidates []string
	candidates = append(candidates, r.SamePackageFiles(sym.File)...)
	for _, d := range targetRes.Decls {
		if d.Kind != scope.KindImport {
			continue
		}
		idx := strings.IndexByte(d.Signature, 0)
		var modulePath, originalName string
		if idx >= 0 {
			modulePath = d.Signature[:idx]
			originalName = d.Signature[idx+1:]
		} else {
			modulePath = d.Signature
		}
		var spec string
		if originalName == "*" {
			spec = modulePath + ".*"
		} else if originalName != "" {
			spec = modulePath + "." + originalName
		} else {
			spec = modulePath
		}
		candidates = append(candidates, r.FilesForImport(spec, sym.File)...)
	}
	// For each candidate, look for a class/interface decl whose name
	// is in our supertype list. If the containing class has a
	// same-name method as the target, emit that method''s span.
	seenSpan := map[[3]uint32]bool{}
	for _, cand := range candidates {
		candRes := r.Result(cand)
		if candRes == nil {
			continue
		}
		// Find the supertype class/interface''s decl index (it''s at
		// file scope; scope ID tracks the class body).
		superScopeID := scope.ScopeID(0)
		for i := range candRes.Decls {
			d := &candRes.Decls[i]
			if d.Scope != scope.ScopeID(1) {
				continue
			}
			for _, s := range supers {
				if d.Name == s {
					// Find the scope ID that represents this class''s body.
					// It''s the first scope with FullSpan inside/after
					// d''s FullSpan. Simpler: scan decls whose scope
					// is nested inside this decl''s FullSpan.
					for j := range candRes.Decls {
						m := &candRes.Decls[j]
						if m.Kind == scope.KindMethod && m.Name == sym.Name &&
							m.Span.StartByte >= d.Span.StartByte &&
							m.Span.EndByte <= d.FullSpan.EndByte {
							key := [3]uint32{uint32(len(out)), m.Span.StartByte, m.Span.EndByte}
							if seenSpan[key] {
								continue
							}
							seenSpan[key] = true
							// isDef: true — the supertype''s method is
							// a DECLARATION of the same logical symbol,
							// not a call site. Gets the bare-name regex
							// rather than the  call-site regex.
							out[cand] = append(out[cand], span{
								start: m.Span.StartByte,
								end:   m.Span.EndByte,
								isDef: true,
							})
						}
					}
					_ = superScopeID
				}
			}
		}
	}
	return out
}

func kotlinCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	r := namespace.NewKotlinResolver(db.Root())
	return jvmCrossFileSpans(ctx, db, sym, r.Result, r.Source, r.SamePackageFiles, namespace.KotlinPopulator(r), r)
}

// buildVarTypes returns a map from variable/parameter/field NAME to
// its type identifier name, for every typed declaration in r whose
// preceding source bytes look like `<TypeName> <varName>`. Bounded
// receiver-type hints — pure intraprocedural, single-pass.
//
// Keyed by name (not DeclID) because the consumer looks up the base
// identifier of a property_access ref (`lib.method` → look up "lib").
// Same-name shadows are accepted as best-effort: the latest decl wins.
//
// Handles the common cases:
//   - Java explicit type:   `Lib lib = ...`
//   - Java parameter:       `void use(Lib lib)`
//   - Java field:           `private final Lib lib;`
//   - Kotlin val/var with type annotation: `val lib: Lib = ...` (the
//     `Lib` identifier appears after a `:` rather than before — see
//     buildVarTypesKotlin if needed; first cut covers the Java pattern
//     which Kotlin also accepts in some forms).
//
// Misses the cases that require return-type or constructor inference
// (`var lib = factory.make()`, `val lib = Lib()`). Those need a
// separate pass over assignment RHS expressions; future work.
func buildVarTypes(r *scope.Result, src []byte) map[string]string {
	out := make(map[string]string)
	if r == nil || len(src) == 0 {
		return out
	}
	for _, d := range r.Decls {
		if d.Kind != scope.KindVar && d.Kind != scope.KindParam && d.Kind != scope.KindField && d.Kind != scope.KindLet && d.Kind != scope.KindConst {
			continue
		}
		// Direction A (Java-ish): `Type name` — ref ends immediately
		// before decl start, separated only by whitespace.
		if name := findTypeBeforeDecl(r, src, d.Span.StartByte); name != "" {
			out[d.Name] = name
			continue
		}
		// Direction B (Kotlin-ish): `name: Type` — ref starts
		// immediately after decl end plus a `:` plus whitespace.
		if name := findTypeAfterDecl(r, src, d.Span.EndByte); name != "" {
			out[d.Name] = name
			continue
		}
		// Direction C (constructor inference): `name = new Type()`
		// (Java var) or `name = Type()` (Kotlin val). The type ref
		// appears on the RHS of the assignment; no explicit type
		// annotation on the LHS.
		if name := findTypeFromConstructorInit(r, src, d.Span.EndByte); name != "" {
			out[d.Name] = name
			continue
		}
	}
	return out
}

// findTypeBeforeDecl returns the name of the ref whose end is
// closest to declStart with only whitespace separating them, or "".
// Covers the `Type name` annotation form (Java, older Kotlin).
func findTypeBeforeDecl(r *scope.Result, src []byte, declStart uint32) string {
	var bestEnd uint32
	var bestName string
	for _, ref := range r.Refs {
		if ref.Span.EndByte > declStart {
			continue
		}
		if ref.Span.EndByte <= bestEnd {
			continue
		}
		// Direction A is the `Type name` pattern (Java-style),
		// where Type and name are on the SAME LINE. A newline in
		// the gap means we've crossed a statement boundary and
		// the previous ref isn't this decl's type. Allow only
		// horizontal whitespace + the leading `*` or `&` that
		// shows up in C++ pointer/reference decls.
		if !sameLineHWhitespace(src[ref.Span.EndByte:declStart]) {
			continue
		}
		bestEnd = ref.Span.EndByte
		bestName = ref.Name
	}
	return bestName
}

// sameLineHWhitespace reports whether b contains only space, tab,
// `*`, or `&` characters — i.e., no newlines.
func sameLineHWhitespace(b []byte) bool {
	for _, c := range b {
		if c == ' ' || c == '\t' || c == '*' || c == '&' {
			continue
		}
		return false
	}
	return true
}

// findTypeAfterDecl returns the name of the ref whose start is
// closest to declEnd with only `:` + whitespace separating them, or
// "". Covers the `name: Type` annotation form (Kotlin, TS, Python
// type hints).
func findTypeAfterDecl(r *scope.Result, src []byte, declEnd uint32) string {
	var bestStart uint32 = ^uint32(0)
	var bestName string
	for _, ref := range r.Refs {
		if ref.Span.StartByte < declEnd {
			continue
		}
		if ref.Span.StartByte >= bestStart {
			continue
		}
		gap := src[declEnd:ref.Span.StartByte]
		// Must contain exactly one `:` plus whitespace.
		sawColon := false
		allowed := true
		for _, c := range gap {
			if c == ':' {
				if sawColon {
					allowed = false
					break
				}
				sawColon = true
				continue
			}
			if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
				allowed = false
				break
			}
		}
		if !allowed || !sawColon {
			continue
		}
		bestStart = ref.Span.StartByte
		bestName = ref.Name
	}
	return bestName
}

func onlyWhitespace(b []byte) bool {
	for _, c := range b {
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			return false
		}
	}
	return true
}

// findTypeFromConstructorInit returns the name of the ref whose start
// is closest to declEnd and whose preceding bytes match the
// assignment-to-constructor pattern:
//
//	\s*=\s*(new\s+)?
//
// which covers Java `var x = new Foo()` and Kotlin `val x = Foo()`.
// Returns "" when the pattern does not match. Covers the inference
// case where the LHS has no explicit type annotation but the RHS
// begins with an obvious constructor call.
func findTypeFromConstructorInit(r *scope.Result, src []byte, declEnd uint32) string {
	var bestStart uint32 = ^uint32(0)
	var bestName string
	for _, ref := range r.Refs {
		if ref.Span.StartByte < declEnd {
			continue
		}
		if ref.Span.StartByte >= bestStart {
			continue
		}
		gap := src[declEnd:ref.Span.StartByte]
		if !assignmentToConstructorGap(gap) {
			continue
		}
		bestStart = ref.Span.StartByte
		bestName = ref.Name
	}
	return bestName
}

// assignmentToConstructorGap returns true when gap matches
// \s*=\s*(new\s+)?.
func assignmentToConstructorGap(gap []byte) bool {
	i := 0
	n := len(gap)
	for i < n && (gap[i] == ' ' || gap[i] == '\t' || gap[i] == '\n' || gap[i] == '\r') {
		i++
	}
	if i >= n || gap[i] != '=' {
		return false
	}
	i++
	for i < n && (gap[i] == ' ' || gap[i] == '\t' || gap[i] == '\n' || gap[i] == '\r') {
		i++
	}
	// Optional `new ` (Java).
	if i+3 <= n && string(gap[i:i+3]) == "new" {
		j := i + 3
		if j < n && (gap[j] == ' ' || gap[j] == '\t' || gap[j] == '\n' || gap[j] == '\r') {
			i = j + 1
			for i < n && (gap[i] == ' ' || gap[i] == '\t' || gap[i] == '\n' || gap[i] == '\r') {
				i++
			}
		}
	}
	return i == n
}
