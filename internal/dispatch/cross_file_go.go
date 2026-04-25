package dispatch

import (
	"bytes"
	"context"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/namespace"
)

// goCrossFileSpans replaces the per-language Go branch of
// scopeAwareCrossFileSpans with a uniform namespace-based approach:
// for each candidate file, build its effective namespace, then accept
// refs whose name matches the target AND whose namespace entry for
// that name carries the targets canonical DeclID.
//
// This collapses the propOK widening, goRefPrefixMatches package-
// prefix check, and goSamePackageRefs supplement into one loop,
// because the namespace knows which decls are visible in each file
// and disambiguates `output.Rel` from `filepath.Rel` by DeclID.
//
// Returns (spans, ok). ok=false only when the targets canonical
// DeclID could not be computed (no go.mod, parse failure) — caller
// then falls back to the regex path. ok=true with an empty map is a
// legitimate "no cross-file callers" answer.
func goCrossFileSpans(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (map[string][]span, bool) {
	out := map[string][]span{}
	resolver := namespace.NewGoResolver(db.Root())
	canonical := resolver.CanonicalPath(sym.File)
	targetRes := resolver.Result(sym.File)

	// Same-package walker runs unconditionally as a shadow-safe backup.
	// It only emits BindUnresolved refs in same-package siblings, so
	// shadowed locals (BindResolved-to-local) are skipped by construction.
	// Even when the namespace path also runs and produces overlapping
	// spans for the same call sites, the rename engine's overlap-skip
	// dedups them.
	for _, idRef := range goSamePackageRefs(sym.File, goPackageOfFile(sym.File), sym.Name) {
		out[idRef.file] = append(out[idRef.file], span{
			start: idRef.startByte,
			end:   idRef.endByte,
		})
	}

	// Namespace path: requires a canonical path (go.mod found) and a
	// successful parse of the target file. When unavailable we still
	// return the same-package spans above — partial coverage but
	// shadow-safe.
	if canonical == "" || targetRes == nil {
		return out, true
	}

	// Find the target Decl by matching name + span containment. The
	// scope builder's Decl.Span is the identifier; sym.StartByte/
	// EndByte covers the full symbol (including signature/body). We
	// match the decl whose identifier span falls inside the symbol.
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

	// Candidate files: cross-package callers via the import graph,
	// plus same-package siblings (covered by walker above too — the
	// rename engine dedupes overlapping spans).
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
	// For method renames we also include sym.File itself so the
	// property-access ref loop disambiguates `h.method()` call sites
	// that the same-file scope pass missed (those refs come through
	// as BindAmbiguous because Go's structural typing means scope
	// can't statically resolve `h` to the receiver's concrete type).
	// varTypes pairs `h *UpgradeAwareHandler` with its type, then
	// the ref filter accepts the call.
	if sym.Receiver != "" {
		candidates[sym.File] = true
	}

	// For method renames we also need to rewrite call sites through
	// interface-typed variables. acceptableTypes is the set of type
	// names whose methods we treat as aliases of the target method:
	// the concrete receiver plus every same-package interface whose
	// full method set is a subset of the receiver's method set AND
	// includes sym.Name.
	isMethod := sym.Receiver != ""
	acceptableTypes := map[string]bool{}
	if sym.Receiver != "" {
		acceptableTypes[sym.Receiver] = true
	}
	var ifaceHits []goIfaceHit
	var siblingImpls []goSiblingImpl
	if isMethod {
		ifaceHits = goSatisfiedInterfaces(ctx, db, sym, resolver)
		for _, h := range ifaceHits {
			acceptableTypes[h.name] = true
		}
		// Sibling implementer propagation: when sym.Receiver
		// satisfies interface I in this package, ANY other type T
		// that also satisfies I shares the same method name with
		// the interface. Renaming sym.Receiver.Name + I.Name without
		// renaming T.Name leaves T no longer matching I — compile
		// breaks. Find every such T and emit its method span.
		// Also add T to acceptableTypes so callers using T-typed
		// vars get rewritten alongside.
		siblingImpls = goSiblingImplementers(ctx, db, sym, resolver, ifaceHits)
		for _, s := range siblingImpls {
			acceptableTypes[s.receiver] = true
		}
	}

	targetPkgPath := canonical
	pop := namespace.GoPopulator(resolver)
	for cand := range candidates {
		candRes := resolver.Result(cand)
		if candRes == nil {
			continue
		}
		ns := namespace.Build(cand, candRes, resolver, pop)
		// For non-method renames the namespace must carry our target
		// DeclID for sym.Name. For methods the namespace doesn't
		// surface method names at all (methods live inside type
		// scopes), so we admit every candidate and rely on per-ref
		// disambiguation below.
		if !isMethod && !ns.Matches(sym.Name, target.ID) {
			continue
		}
		// Per-file: shadow guard map + import-gateway map. Gateways
		// translate `pkg.Name` property accesses into the import path
		// `pkg` resolves to, so we can confirm the ref points to our
		// target's package — not some unrelated `other.Name` call.
		declByID := make(map[scope.DeclID]*scope.Decl, len(candRes.Decls))
		for i := range candRes.Decls {
			declByID[candRes.Decls[i].ID] = &candRes.Decls[i]
		}
		gateways := goImportGateways(candRes)
		// Source bytes for base-identifier extraction. The resolver
		// caches them — same allocation as the Result parse used.
		src := resolver.Source(cand)
		// For methods: map each local var/param/field name to the
		// last identifier of its declared type. Used to accept
		// property_access refs whose base is a var of an acceptable
		// type.
		var varTypes map[string]string
		if isMethod {
			varTypes = goBuildVarTypes(candRes, src)
		}

		for _, ref := range candRes.Refs {
			if ref.Name != sym.Name {
				continue
			}
			// Shadow guard: ref bound to a NESTED-scope same-name
			// decl in this file → it's a local, not our target.
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
			// Property-access disambiguation: `X.Name` is only our
			// target if X resolves to an import for our target's
			// package. Bare refs (BindUnresolved) skip this — they're
			// either same-package siblings (handled by walker) or
			// builtins (filtered out elsewhere).
			if ref.Binding.Reason == "property_access" && len(src) > 0 {
				baseIdent := goBaseIdentBefore(src, ref.Span.StartByte)
				if baseIdent == "" {
					continue
				}
				// Accept when base resolves to an import of the
				// target package (cross-package function call), OR
				// (methods only) when base is a local of an
				// acceptable type (concrete receiver or satisfied
				// interface).
				if gatewayPath, ok := gateways[baseIdent]; ok && gatewayPath == targetPkgPath {
					// ok
				} else if isMethod && varTypes != nil && acceptableTypes[varTypes[baseIdent]] {
					// ok
				} else {
					continue
				}
			}
			out[cand] = append(out[cand], span{
				start: ref.Span.StartByte,
				end:   ref.Span.EndByte,
			})
		}
	}

	// Emit identifier spans for each satisfied interface's
	// declaration of sym.Name. The span covers the method identifier
	// itself; the apply layer rewrites it directly.
	for _, h := range ifaceHits {
		out[h.file] = append(out[h.file], span{
			start: h.methodSpan.StartByte,
			end:   h.methodSpan.EndByte,
		})
	}

	// Emit identifier spans for each sibling implementer's method
	// named sym.Name. The symbol-index span covers the FULL method
	// (signature + body); narrow it to just the identifier so the
	// apply layer doesn't overwrite the body.
	for _, s := range siblingImpls {
		src := resolver.Source(s.file)
		if len(src) == 0 {
			continue
		}
		nameSpan, ok := goMethodNameSpan(src, s.span.StartByte, s.span.EndByte, sym.Name)
		if !ok {
			continue
		}
		out[s.file] = append(out[s.file], span{
			start: nameSpan.StartByte,
			end:   nameSpan.EndByte,
		})
	}

	return out, true
}

// goImportGateways maps each KindImport decl's local name to the
// import path it brings in. Used by goCrossFileSpans to decide
// whether `X.Name` refers to our target's package.
func goImportGateways(r *scope.Result) map[string]string {
	out := make(map[string]string)
	if r == nil {
		return out
	}
	for _, d := range r.Decls {
		if d.Kind != scope.KindImport {
			continue
		}
		path := d.Signature
		// The Go scope builder packs the import path as
		// "<importPath>\x00*" — strip the trailing marker.
		if i := strings.IndexByte(path, 0); i >= 0 {
			path = path[:i]
		}
		if path == "" {
			continue
		}
		out[d.Name] = path
	}
	return out
}

// goBaseIdentBefore returns the identifier immediately before a `.`
// at refStart, or "" if the preceding char is not a dot or no
// identifier precedes it. Pure byte-level scan — we cannot rely on
// the scope builder to expose the base of property_access refs.
func goBaseIdentBefore(src []byte, refStart uint32) string {
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



// goIfaceHit records a same-package interface whose method set is a
// subset of sym.Receiver's method set, along with the span of its
// declaration of sym.Name.
type goIfaceHit struct {
	name       string
	file       string
	methodSpan scope.Span
	// methods is the interface's full method set. Used by the caller
	// to find OTHER same-package types that also satisfy this
	// interface so their method spans get rewritten in lockstep —
	// otherwise the rename leaves the interface decl + the rename
	// target updated but breaks compile for sibling implementers
	// whose method name no longer matches the interface.
	methods []string
}

// goSatisfiedInterfaces finds same-package interfaces that sym.Receiver
// structurally implements. An interface qualifies only when every
// method it declares is also a method on sym.Receiver AND sym.Name is
// one of them. The Go scope builder emits `type X interface {...}`
// as a KindType decl plus a child ScopeInterface; we walk scopes and
// match each ScopeInterface back to its owning type decl by proximity.
func goSatisfiedInterfaces(
	ctx context.Context,
	db index.SymbolStore,
	sym *index.SymbolInfo,
	resolver *namespace.GoResolver,
) []goIfaceHit {
	if sym.Receiver == "" {
		return nil
	}
	files := append([]string{sym.File}, resolver.SamePackageFiles(sym.File)...)
	receiverMethods := map[string]bool{}
	for _, f := range files {
		syms, err := db.GetSymbolsByFile(ctx, f)
		if err != nil {
			continue
		}
		for _, s := range syms {
			if s.Type == "method" && s.Receiver == sym.Receiver {
				receiverMethods[s.Name] = true
			}
		}
	}
	if !receiverMethods[sym.Name] {
		return nil
	}
	var hits []goIfaceHit
	for _, f := range files {
		res := resolver.Result(f)
		if res == nil {
			continue
		}
		for si := range res.Scopes {
			sc := &res.Scopes[si]
			if sc.Kind != scope.ScopeInterface {
				continue
			}
			var owner *scope.Decl
			var bestEnd uint32
			for k := range res.Decls {
				d := &res.Decls[k]
				if d.Kind != scope.KindType {
					continue
				}
				if d.Span.EndByte > sc.Span.StartByte {
					continue
				}
				if d.Span.EndByte <= bestEnd {
					continue
				}
				bestEnd = d.Span.EndByte
				owner = d
			}
			if owner == nil {
				continue
			}
			// Collect methods whose Scope matches this interface.
			// Skip identifiers that don't look like exported method
			// names — the builder occasionally emits trailing
			// return-type idents like `error` as KindMethod inside
			// interface bodies.
			ifaceMethods := map[string]scope.Span{}
			allSubset := true
			for k := range res.Decls {
				m := &res.Decls[k]
				if m.Kind != scope.KindMethod || m.Scope != sc.ID {
					continue
				}
				if m.Name == "" || m.Name[0] < 'A' || m.Name[0] > 'Z' {
					continue
				}
				if !receiverMethods[m.Name] {
					allSubset = false
					break
				}
				ifaceMethods[m.Name] = m.Span
			}
			if !allSubset || len(ifaceMethods) == 0 {
				continue
			}
			if methodSpan, ok := ifaceMethods[sym.Name]; ok {
				names := make([]string, 0, len(ifaceMethods))
				for n := range ifaceMethods {
					names = append(names, n)
				}
				hits = append(hits, goIfaceHit{
					name:       owner.Name,
					file:       f,
					methodSpan: methodSpan,
					methods:    names,
				})
			}
		}
	}
	return hits
}

// goSiblingImpl describes another type in the same package that
// also satisfies one of the rename target's matched interfaces.
// Renaming the target's method without renaming the sibling's method
// would break compile: the interface decl gets the new name, but the
// sibling type's same-named method no longer matches it.
type goSiblingImpl struct {
	receiver string
	file     string
	span     scope.Span
}

// goSiblingImplementers walks same-package files for OTHER types
// (receivers) whose method set is a superset of any matched
// interface's method set, and returns the span of each such type's
// method named sym.Name. Excludes sym.Receiver itself — that's the
// rename target, already handled by the caller's main span emission.
func goSiblingImplementers(
	ctx context.Context,
	db index.SymbolStore,
	sym *index.SymbolInfo,
	resolver *namespace.GoResolver,
	ifaces []goIfaceHit,
) []goSiblingImpl {
	if len(ifaces) == 0 {
		return nil
	}
	files := append([]string{sym.File}, resolver.SamePackageFiles(sym.File)...)
	// Build receiver -> (methodName -> SymbolInfo of that method).
	type recvBucket struct {
		methods map[string]index.SymbolInfo
	}
	receivers := map[string]*recvBucket{}
	for _, f := range files {
		syms, err := db.GetSymbolsByFile(ctx, f)
		if err != nil {
			continue
		}
		for i := range syms {
			s := syms[i]
			if s.Type != "method" || s.Receiver == "" {
				continue
			}
			b := receivers[s.Receiver]
			if b == nil {
				b = &recvBucket{methods: map[string]index.SymbolInfo{}}
				receivers[s.Receiver] = b
			}
			b.methods[s.Name] = s
		}
	}

	seen := map[string]bool{} // receiver name → emitted, to dedupe across ifaces
	var out []goSiblingImpl
	for _, iface := range ifaces {
		// Compute the interface method's arity once. The methodSpan
		// is the identifier; goMethodArity reads the bytes after the
		// identifier to count params/returns. Sibling implementers
		// must match this arity to actually satisfy the interface —
		// without this check, any same-name method (e.g. an io.Reader
		// `Read([]byte) (int, error)` colocated with a custom
		// `Read() ([]byte, error)`) would be falsely propagated.
		ifaceSrc := resolver.Source(iface.file)
		ifaceParams, ifaceReturns, ifaceArityOK := goMethodArity(ifaceSrc, iface.methodSpan.EndByte)
		if !ifaceArityOK {
			continue
		}
		for recvName, b := range receivers {
			if recvName == sym.Receiver {
				continue
			}
			if seen[recvName] {
				continue
			}
			// Receiver must implement every method in this interface
			// (by name only — signature shape is checked below for
			// sym.Name; full conformance for the other methods is
			// approximated by name presence).
			complete := true
			for _, m := range iface.methods {
				if _, ok := b.methods[m]; !ok {
					complete = false
					break
				}
			}
			if !complete {
				continue
			}
			tgtMethod := b.methods[sym.Name]
			// Arity gate: signature shape of THIS implementer's
			// sym.Name method must match the interface's. Find the
			// method-name identifier inside the full method span and
			// compare arities.
			tgtSrc := resolver.Source(tgtMethod.File)
			nameSpan, ok := goMethodNameSpan(tgtSrc, tgtMethod.StartByte, tgtMethod.EndByte, sym.Name)
			if !ok {
				continue
			}
			tgtParams, tgtReturns, tgtArityOK := goMethodArity(tgtSrc, nameSpan.EndByte)
			if !tgtArityOK {
				continue
			}
			if tgtParams != ifaceParams || tgtReturns != ifaceReturns {
				continue
			}
			seen[recvName] = true
			out = append(out, goSiblingImpl{
				receiver: recvName,
				file:     tgtMethod.File,
				span:     scope.Span{StartByte: tgtMethod.StartByte, EndByte: tgtMethod.EndByte},
			})
		}
	}
	return out
}

// goMethodArity returns the (paramCount, returnCount) of a Go method
// or interface-method signature whose identifier starts at identEnd.
// identEnd is the byte position immediately after the method name (so
// the signature begins with `(` for params).
//
// Used to filter sibling-interface candidates: two methods with the
// same name but different arities don't satisfy the same interface.
// Counts top-level commas with paren-depth tracking — ignores commas
// inside generic type lists, struct embeddings, etc.
//
// Returns ok=false when the bytes don't match the expected
// `(...) ...` shape (malformed source, end-of-file, etc.).
func goMethodArity(src []byte, identEnd uint32) (params, returns int, ok bool) {
	if int(identEnd) >= len(src) {
		return 0, 0, false
	}
	i := int(identEnd)
	// Skip whitespace.
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i >= len(src) || src[i] != '(' {
		return 0, 0, false
	}
	// Count params inside the matching parens.
	pCount, pEnd, ok := goCountTopLevelArgs(src, i)
	if !ok {
		return 0, 0, false
	}

	// After params: skip whitespace, then look at return shape.
	j := pEnd + 1 // past matching ')'
	for j < len(src) && (src[j] == ' ' || src[j] == '\t') {
		j++
	}
	if j >= len(src) {
		return pCount, 0, true
	}
	switch src[j] {
	case '\n', '{', ';', '}':
		// No return value (void or interface-method line ends).
		return pCount, 0, true
	case '(':
		// Multi-return: `(T1, T2, T3)`.
		rCount, _, ok := goCountTopLevelArgs(src, j)
		if !ok {
			return pCount, 0, false
		}
		return pCount, rCount, true
	}
	// Single return value: any non-paren type until end of line / `{`.
	return pCount, 1, true
}

// goCountTopLevelArgs assumes src[start] is '(' and counts the number
// of top-level comma-separated entries inside the matching parens.
// Returns count, byte index of the matching ')', and ok.
//
// "Top-level" means at paren-depth 1; commas inside nested parens,
// brackets, or braces don't count. Empty list returns 0.
func goCountTopLevelArgs(src []byte, start int) (count, closeIdx int, ok bool) {
	if start >= len(src) || src[start] != '(' {
		return 0, 0, false
	}
	depth := 1
	commas := 0
	hasContent := false
	i := start + 1
	for i < len(src) && depth > 0 {
		switch src[i] {
		case '(', '[', '{':
			depth++
			hasContent = true
		case ')':
			depth--
			if depth == 0 {
				break
			}
		case ']', '}':
			depth--
		case ',':
			if depth == 1 {
				commas++
			}
		case ' ', '\t', '\n', '\r':
			// whitespace doesn't count as content
		default:
			hasContent = true
		}
		if depth == 0 {
			break
		}
		i++
	}
	if depth != 0 {
		return 0, 0, false
	}
	if !hasContent {
		return 0, i, true
	}
	return commas + 1, i, true
}

// goMethodNameSpan returns the byte span of the method-name identifier
// inside a `func (recv) Name(...)` declaration. The caller hands it
// the FULL method span (signature + body) from the symbol index;
// this function narrows it to just the `Name` identifier. Returns
// ok=false if the bytes don't match the expected shape (e.g. the
// symbol-index span was off, or `name` doesn't appear at the method-
// name position).
func goMethodNameSpan(src []byte, methodStart, methodEnd uint32, name string) (scope.Span, bool) {
	if int(methodEnd) > len(src) || methodStart >= methodEnd {
		return scope.Span{}, false
	}
	body := src[methodStart:methodEnd]
	// Find the receiver's opening `(` — first `(` in the method body.
	open := bytes.IndexByte(body, '(')
	if open < 0 {
		return scope.Span{}, false
	}
	// Walk past the matching close paren (handle nested parens in
	// receiver type like `func (m map[int]struct{ a int }) ...`).
	depth := 1
	j := open + 1
	for j < len(body) && depth > 0 {
		switch body[j] {
		case '(':
			depth++
		case ')':
			depth--
		}
		j++
	}
	if depth != 0 {
		return scope.Span{}, false
	}
	// Skip whitespace.
	for j < len(body) && (body[j] == ' ' || body[j] == '\t') {
		j++
	}
	// Match the identifier with word boundaries.
	if j+len(name) > len(body) {
		return scope.Span{}, false
	}
	if string(body[j:j+len(name)]) != name {
		return scope.Span{}, false
	}
	if j+len(name) < len(body) {
		c := body[j+len(name)]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			return scope.Span{}, false
		}
	}
	return scope.Span{
		StartByte: methodStart + uint32(j),
		EndByte:   methodStart + uint32(j) + uint32(len(name)),
	}, true
}

// goBuildVarTypes maps each local var/param/field decl to the last
// identifier of its declared type. Handles Go's `name Type`,
// `name *Type`, and `name pkg.Type` orderings.
func goBuildVarTypes(r *scope.Result, src []byte) map[string]string {
	out := make(map[string]string)
	if r == nil || len(src) == 0 {
		return out
	}
	for _, d := range r.Decls {
		if d.Kind != scope.KindVar && d.Kind != scope.KindParam && d.Kind != scope.KindField {
			continue
		}
		if name := goFindTypeAfterDecl(r, src, d.Span.EndByte); name != "" {
			out[d.Name] = name
			continue
		}
		// Ref-based lookup misses method receivers (`func (h *T)
		// ...`) because the Go scope builder emits T as a function
		// Decl, not a Ref. Fall back to a textual scan: skip ws/`*`
		// after the param ident, then capture the next identifier.
		if name := goScanTypeIdent(src, d.Span.EndByte); name != "" {
			out[d.Name] = name
			continue
		}
		// Short-var declarations like `a := A{}` or `a := &A{}`
		// have the type on the RHS. Skip past `:=` and an optional
		// `&`, then capture the next identifier if it's followed by
		// `{` or `(` (struct literal or function call returning T).
		if name := goScanShortVarType(src, d.Span.EndByte); name != "" {
			out[d.Name] = name
		}
	}
	return out
}

// goScanShortVarType handles `name := A{}` / `name := &A{}` /
// `name := new(A)` patterns and returns the type identifier A, or ""
// if the RHS doesn't match a recognized constructor.
func goScanShortVarType(src []byte, pos uint32) string {
	i := int(pos)
	// Skip ws + `:=` (or `=` for plain var).
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i >= len(src) {
		return ""
	}
	if i+1 < len(src) && src[i] == ':' && src[i+1] == '=' {
		i += 2
	} else if src[i] == '=' {
		i++
	} else {
		return ""
	}
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	// `&` for &T{}, or `new(` for new(T).
	if i < len(src) && src[i] == '&' {
		i++
	} else if i+4 < len(src) && string(src[i:i+4]) == "new(" {
		i += 4
	}
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	start := i
	for i < len(src) {
		c := src[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			i++
			continue
		}
		break
	}
	if i == start {
		return ""
	}
	end := i
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	if i >= len(src) {
		return ""
	}
	switch src[i] {
	case '{', '(', ')':
		return string(src[start:end])
	}
	return ""
}

// goScanTypeIdent walks forward from pos through whitespace and `*`,
// then captures the next identifier (Go ident bytes). Used to recover
// receiver types that the scope builder doesn't emit as refs.
func goScanTypeIdent(src []byte, pos uint32) string {
	i := int(pos)
	for i < len(src) {
		c := src[i]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '*' {
			i++
			continue
		}
		break
	}
	start := i
	for i < len(src) {
		c := src[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '_' {
			i++
			continue
		}
		break
	}
	if i == start {
		return ""
	}
	return string(src[start:i])
}

// goFindTypeAfterDecl returns the tail identifier of the type
// annotation that begins immediately after declEnd.
func goFindTypeAfterDecl(r *scope.Result, src []byte, declEnd uint32) string {
	var first *scope.Ref
	var firstStart uint32 = ^uint32(0)
	for i := range r.Refs {
		ref := &r.Refs[i]
		if ref.Span.StartByte < declEnd {
			continue
		}
		if ref.Span.StartByte >= firstStart {
			continue
		}
		gap := src[declEnd:ref.Span.StartByte]
		if !onlyWsOrStar(gap) {
			continue
		}
		firstStart = ref.Span.StartByte
		first = ref
	}
	if first == nil {
		return ""
	}
	cur := first
	for {
		var next *scope.Ref
		var nextStart uint32 = ^uint32(0)
		for i := range r.Refs {
			ref := &r.Refs[i]
			if ref.Span.StartByte < cur.Span.EndByte {
				continue
			}
			if ref.Span.StartByte >= nextStart {
				continue
			}
			gap := src[cur.Span.EndByte:ref.Span.StartByte]
			if len(gap) != 1 || gap[0] != '.' {
				continue
			}
			nextStart = ref.Span.StartByte
			next = ref
		}
		if next == nil {
			break
		}
		cur = next
	}
	return cur.Name
}

func onlyWsOrStar(gap []byte) bool {
	for _, c := range gap {
		switch c {
		case ' ', '\t', '\n', '\r', '*':
			continue
		}
		return false
	}
	return true
}
