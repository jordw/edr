// Package php is the PHP scope + binding extractor.
//
// Produces a scope.Result for a single PHP source buffer. PHP code lives
// inside <?php ... ?> tags; everything outside the tags is treated as
// literal HTML/text and skipped. <?= is the short-echo open tag and is
// also treated as a switch into PHP mode.
//
// Handles file / function / block / class / interface / trait / enum /
// namespace scopes; emits KindFunction / KindMethod / KindClass /
// KindInterface / KindType (for traits) / KindEnum / KindVar / KindConst /
// KindField / KindParam / KindImport / KindNamespace decls. $-prefixed
// locals/params/properties carry their leading $ in the decl name
// (PHP's own view of the identifier). Bare identifiers (functions,
// classes, constants) don't.
//
// v1 limitations:
//   - Data-side type/value namespace split is in place (Stage 1):
//     interface and trait bind NSType only; class and enum dual-emit
//     (NSValue + NSType, unified by within-file merge). But ref-side
//     tagging is NOT done — refs in type position (parameter type
//     hints, return types, `extends`/`implements`, `instanceof`,
//     property types) are still emitted as NSValue. resolveRefs
//     retries in the alternate namespace on miss, which covers the
//     common cases but won't distinguish same-name type/value
//     decls in the rare case where both exist. (Stage 2 follow-up:
//     tag ref.Namespace = NSType when the syntactic position is
//     clearly type-shaped.)
//   - Top-level constants declared via define('X', ...) extract the first
//     string-literal argument as the const name.
//   - Short closure `fn($x) => ...` auto-captures everything in the
//     enclosing scope (no `use (...)` needed); we treat it like a normal
//     arrow — refs to outer vars fall through the scope chain, which
//     works for lexical capture.
//   - `global $x` declarations are treated as refs, not scope binders.
//   - Trait composition (`use TraitA { foo as bar; }` inside a class) is
//     not modelled; the trait names emit as refs.
//   - PHP 8 attributes `#[Attr(...)]` are skipped.
//   - Match expressions (`match($x) { ... }`) are treated as a block.
//   - String interpolation `"$name"` doesn't emit a ref for the var name.
//   - Readonly properties and enum backing types are only minimally handled.
package php

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/jordw/edr/internal/lexkit"
	"github.com/jordw/edr/internal/scope"
	"github.com/jordw/edr/internal/scope/builtins"
)

// Parse extracts a scope.Result from a PHP source buffer. file is the
// canonical file path used to stamp Decl.File and Ref.File.
// Parse extracts a scope.Result from a source buffer. File-scope
// decls hash with the file path.
func Parse(file string, src []byte) *scope.Result {
	return ParseCanonical(file, "", src)
}

// ParseCanonical is Parse with an explicit canonical path for
// file-scope DeclID hashing. When canonicalPath is non-empty,
// file-scope decls hash with it instead of the file path — so
// cross-file references via imports can bind to matching DeclIDs.
func ParseCanonical(file, canonicalPath string, src []byte) *scope.Result {
	b := &builder{
		file:                file,
		canonicalPath:       canonicalPath,
		res:                 &scope.Result{File: file},
		s:                   lexkit.New(src),
		pendingOwnerDecl:    -1,
		stmtStart:           true,
		pendingClassDeclIdx: -1,
		pendingTraitUseIdx:  -1,
		classSuperTypes:     map[int][]string{},
	}
	b.openScope(scope.ScopeFile, 0)
	b.run()
	b.closeScopesToDepth(0)
	b.resolveRefs()
	// Copy classSuperTypes onto Decl.SuperTypes so the cross-file
	// hierarchy walker (EmitOverrideSpans) can read them.
	for idx, supers := range b.classSuperTypes {
		if idx >= 0 && idx < len(b.res.Decls) && len(supers) > 0 {
			b.res.Decls[idx].SuperTypes = supers
		}
	}
	scope.MergeDuplicateDecls(b.res)
	return b.res
}

// scopeEntry is our per-stack-frame data. ownerDeclIdx is the index in
// res.Decls of the decl that owns this scope (class / function / etc.);
// closeTopScope patches that decl's FullSpan.EndByte on close. -1 if
// the scope was not introduced by a decl.
type scopeEntry struct {
	kind         scope.ScopeKind
	id           scope.ScopeID
	ownerDeclIdx int
	// closeAtParenZero, when non-zero, marks this scope to close as soon
	// as the ambient paren depth returns to zero (for `fn(x) => expr`
	// arrow-function scopes). The stored value is parenDepth+1 at the
	// moment the arrow opened (so 0 stays "inactive").
	closeAtParenZero int
}

type builder struct {
	file          string
	canonicalPath string
	res           *scope.Result
	s             lexkit.Scanner

	// inPHP is true while we're inside <?php ... ?> tags. HTML outside
	// the tags is silently skipped.
	inPHP bool

	stack lexkit.ScopeStack[scopeEntry]

	stmtStart bool
	prevByte  byte

	// pendingScope, if non-nil, is consumed by the next '{' as the scope
	// kind to push. Set by keywords like "function", "class", "interface".
	pendingScope *scope.ScopeKind

	// declContext, when non-empty, classifies the next identifier as a
	// declaration of this kind rather than a reference.
	declContext scope.DeclKind

	// classBodyConst is true when the next const-declared ident should
	// be emitted as a class constant (NSField, KindConst) rather than a
	// top-level const (NSValue, KindConst).
	classBodyConst bool

	// paramListPending is set after a function-like keyword. The next
	// '(' starts a parameter list.
	paramListPending bool
	// inParamList + paramDepth track being inside a function's (...) param
	// list. paramDepth tracks '(' balance so nested parens (type hints,
	// default values) don't confuse section boundaries.
	inParamList bool
	paramDepth  int
	// paramSectionNeedsName: true at the start of each comma-separated
	// param section; we're still looking for the param name (a $-ident).
	paramSectionNeedsName bool

	// inUseClause is true inside a closure's `use (...)` capture list.
	// $-idents inside become refs bound to the enclosing scope, but we
	// also need to suppress local-decl logic while scanning.
	inUseClause bool
	useDepth    int

	// parenDepth counts open '(' at the top of the parser (outside
	// param/use clauses). Used to decide when a `fn(...) => expr` arrow
	// scope should close (when we return to the parenDepth it had at
	// open time).
	parenDepth int

	// pendingFullStart captures the byte position of the most recent
	// declaration keyword (function, class, interface, trait, enum,
	// namespace, const). emitDecl uses it as FullSpan.StartByte. Stored
	// as offset+1 so 0 means "unset".
	pendingFullStart uint32

	// pendingOwnerDecl is the index in res.Decls of the last scope-owning
	// decl. Consumed by the next openScope; closeTopScope patches the
	// decl's FullSpan.EndByte. -1 when none.
	pendingOwnerDecl int

	// pendingMethodOwnerDecl mirrors pendingOwnerDecl but for methods:
	// when we emit a method name, we want the FUNCTION scope (opened on
	// '{') to patch FullSpan.EndByte on the method decl, not the
	// enclosing class scope.
	pendingMethodOwnerDecl int

	// Class-supertype tracking — populates Decl.SuperTypes for class /
	// interface decls so the cross-file hierarchy walker
	// (EmitOverrideSpans) can walk both directions.
	//
	// Header form: `class Foo extends Base implements I1, I2`. The
	// pendingClassDeclIdx holds the just-emitted class/interface
	// decl until its body `{` opens; inSuperTypes is on between
	// `extends`/`implements` and `{`; superTypeNeedsName is true
	// at the start of a section (after the keyword or a `,`).
	// classBaseLastIdent records the most recent ident in the
	// section (handles qualified names `\Foo\Bar` — last segment
	// wins) and is flushed on `,`/keyword/`{`.
	//
	// Body form: `class Foo { use Trait1, Trait2; ... }`. The trait
	// `use` statement is detected at stmt-start in class scope;
	// pendingTraitUseIdx is the enclosing class decl index, and
	// idents are captured into classSuperTypes until the `;`
	// terminator.
	pendingClassDeclIdx     int
	inSuperTypes            bool
	superTypeNeedsName      bool
	classBaseLastIdent      string
	pendingTraitUseIdx      int
	traitUseLastIdent       string
	classSuperTypes         map[int][]string

	// Namespace statement handling: `namespace Foo\Bar;` opens a
	// namespace scope for everything that follows (until another
	// namespace statement or EOF). `namespace Foo { ... }` (block form)
	// opens a scope that closes on '}'.
	// namespaceIsBlock is true when the *next* '{' should be consumed
	// as a namespace body. Set after `namespace Name` when the following
	// non-ws byte is '{' (peek).
	namespaceIsBlock bool

	// prevIdentIsThis is true when the most recently scanned token was
	// the identifier `$this`. Used with prevByte == '>' (from `->`) to
	// resolve `$this->x` against the enclosing class's NSField decls.
	prevIdentIsThis bool

	// prevWasArrow is true just after we scan `->`. The next identifier
	// is a property access (instance). Cleared after it's consumed.
	prevWasArrow bool
	// prevWasDoubleColon is true just after we scan `::`. The next
	// identifier is a property access (static). Cleared after consumed.
	prevWasDoubleColon bool
}

func (b *builder) run() {
	for !b.s.EOF() {
		if !b.inPHP {
			b.skipToPHPOpen()
			if b.s.EOF() {
				return
			}
			continue
		}
		c := b.s.Peek()
		// Check for closing ?>
		if c == '?' && b.s.PeekAt(1) == '>' {
			b.s.Advance(2)
			b.inPHP = false
			// `?>` acts like a statement terminator.
			b.endStatement()
			continue
		}
		switch {
		case c == ' ' || c == '\t' || c == '\r':
			b.s.Pos++
		case c == '\n':
			b.s.Next()
		case c == '/' && b.s.PeekAt(1) == '/':
			b.s.SkipLineComment()
		case c == '/' && b.s.PeekAt(1) == '*':
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
		case c == '#':
			// Comment form `# ...` — unless it starts `#[` (attribute).
			if b.s.PeekAt(1) == '[' {
				b.s.Advance(2)
				b.skipAttributeBody()
				b.prevByte = ']'
				continue
			}
			b.s.SkipLineComment()
		case c == '\'':
			b.s.ScanSimpleString('\'')
			b.prevByte = '\''
			b.stmtStart = false
		case c == '"':
			b.scanDoubleString()
			b.prevByte = '"'
			b.stmtStart = false
		case c == '`':
			// Backtick: shell-exec operator. Skip like a string.
			b.s.ScanSimpleString('`')
			b.prevByte = '`'
			b.stmtStart = false
		case c == '<' && b.s.PeekAt(1) == '<' && b.s.PeekAt(2) == '<':
			// Heredoc or nowdoc.
			b.scanHeredoc()
			b.prevByte = '"'
			b.stmtStart = false
		case c == '{':
			b.handleOpenBrace()
		case c == '}':
			b.handleCloseBrace()
		case c == '(':
			b.s.Pos++
			b.prevByte = '('
			if b.paramListPending {
				b.paramListPending = false
				b.inParamList = true
				b.paramDepth = 1
				b.paramSectionNeedsName = true
			} else if b.inParamList {
				b.paramDepth++
			} else if b.inUseClause {
				b.useDepth++
			} else {
				b.parenDepth++
			}
		case c == ')':
			b.s.Pos++
			b.prevByte = ')'
			if b.inParamList {
				b.paramDepth--
				if b.paramDepth == 0 {
					b.inParamList = false
					b.paramSectionNeedsName = false
				}
			} else if b.inUseClause {
				b.useDepth--
				if b.useDepth == 0 {
					b.inUseClause = false
				}
			} else if b.parenDepth > 0 {
				b.parenDepth--
				// Check for closing any fn()=>expr arrow scope whose
				// closeAtParenZero was set at this level.
				b.closeArrowIfTerminating()
			}
		case c == ',':
			b.s.Pos++
			b.prevByte = ','
			// In a param list at top depth, a comma starts the next section.
			if b.inParamList && b.paramDepth == 1 {
				b.paramSectionNeedsName = true
			}
			// Supertype list separator (`implements I1, I2`): flush
			// the captured name and reset for the next section.
			if b.inSuperTypes {
				b.flushSuperTypeArg()
			}
			// Trait-use list separator (`use Trait1, Trait2`).
			if b.pendingTraitUseIdx >= 0 {
				b.flushTraitUseArg()
			}
			b.closeArrowIfTerminating()
		case c == ';':
			b.s.Pos++
			b.prevByte = ';'
			// Trait-use statement ends here. Append the final ident
			// and clear the tracker.
			if b.pendingTraitUseIdx >= 0 {
				b.flushTraitUse()
			}
			b.endStatement()
		case c == '[':
			b.s.Pos++
			b.prevByte = '['
		case c == ']':
			b.s.Pos++
			b.prevByte = ']'
		case c == '-' && b.s.PeekAt(1) == '>':
			// Instance access. Next ident is property access.
			b.s.Advance(2)
			b.prevByte = '>'
			b.prevWasArrow = true
		case c == ':' && b.s.PeekAt(1) == ':':
			// Static/scope access. Next ident is property access.
			b.s.Advance(2)
			b.prevByte = ':'
			b.prevWasDoubleColon = true
		case c == '=' && b.s.PeekAt(1) == '>':
			// Arrow-function body or array element `=>`.
			// If we're at the top of a `fn(params)` arrow (pendingScope
			// set to ScopeFunction), open the arrow scope now.
			if b.pendingScope != nil && *b.pendingScope == scope.ScopeFunction {
				// Arrow fn: open a function scope here. The body is an
				// expression; it closes at the next terminator at the
				// ambient paren depth.
				arrowStart := uint32(b.s.Pos)
				b.s.Advance(2)
				b.pendingScope = nil
				b.openScopeArrow(scope.ScopeFunction, arrowStart, b.parenDepth+1)
				b.prevByte = '>'
				continue
			}
			b.s.Advance(2)
			b.prevByte = '>'
		case c == '$':
			b.handleDollarIdent()
		case c == '\\':
			// Namespace separator, only meaningful inside qualified names.
			b.s.Pos++
			b.prevByte = '\\'
		case lexkit.IsDefaultIdentStart(c):
			word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
			b.handleIdent(word)
		case lexkit.IsASCIIDigit(c):
			for !b.s.EOF() {
				cc := b.s.Peek()
				if !lexkit.IsASCIIDigit(cc) && cc != '.' && cc != '_' && cc != 'x' && cc != 'X' && cc != 'e' && cc != 'E' && cc != 'b' && cc != 'B' {
					break
				}
				b.s.Pos++
			}
			b.prevByte = '0'
			b.stmtStart = false
		default:
			b.s.Pos++
			b.prevByte = c
		}
	}
}

// skipToPHPOpen advances the scanner to just past the next <?php or <?=
// open tag. If no open tag is found, advances to EOF.
func (b *builder) skipToPHPOpen() {
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '<' && b.s.PeekAt(1) == '?' {
			// <?php (case-insensitive) or <?=
			if b.s.PeekAt(2) == '=' {
				b.s.Advance(3)
				b.inPHP = true
				b.stmtStart = true
				return
			}
			// Match `<?php` literally, and as a fallback plain `<?`.
			if (b.s.PeekAt(2) == 'p' || b.s.PeekAt(2) == 'P') &&
				(b.s.PeekAt(3) == 'h' || b.s.PeekAt(3) == 'H') &&
				(b.s.PeekAt(4) == 'p' || b.s.PeekAt(4) == 'P') {
				b.s.Advance(5)
				b.inPHP = true
				b.stmtStart = true
				return
			}
			// Short open tag `<?` — also accept.
			b.s.Advance(2)
			b.inPHP = true
			b.stmtStart = true
			return
		}
		b.s.Next()
	}
}

// endStatement fires at ';', '?>', and close-brace for statement-like
// contexts. Clears per-statement flags.
func (b *builder) endStatement() {
	b.stmtStart = true
	b.declContext = ""
	b.paramListPending = false
	b.prevIdentIsThis = false
	b.prevWasDoubleColon = false
	b.classBodyConst = false
	b.closeArrowIfTerminating()
}

func (b *builder) handleDollarIdent() {
	// `$...`: could be a local / param / property / global-variable access.
	// Treat `$ident` as a single identifier including the `$`.
	start := b.s.Pos
	b.s.Pos++ // consume $
	if b.s.Peek() == '$' {
		// Variable-variable `$$x`: emit nothing (not in v1). Advance
		// through the inner $-ident so we don't loop.
		b.s.Pos++
		if lexkit.IsDefaultIdentStart(b.s.Peek()) {
			_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		}
		b.prevByte = 'i'
		b.stmtStart = false
		return
	}
	if !lexkit.IsDefaultIdentStart(b.s.Peek()) {
		// Stray `$` — treat as data.
		b.prevByte = '$'
		return
	}
	_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
	name := string(b.s.Src[start:b.s.Pos])
	span := mkSpan(uint32(start), uint32(b.s.Pos))

	wasStmtStart := b.stmtStart
	b.stmtStart = false

	// Property access: `->$var` is a dynamic property access; we don't
	// treat as a decl. Same for `::$var`.
	if b.prevWasArrow || b.prevWasDoubleColon {
		b.emitPropertyRef(name, span)
		b.prevWasArrow = false
		b.prevWasDoubleColon = false
		b.prevByte = 'i'
		b.prevIdentIsThis = false
		return
	}

	// Inside a param list, the first ident of each section is a param name.
	if b.inParamList && b.paramDepth == 1 && b.paramSectionNeedsName {
		b.emitDecl(name, scope.KindParam, span, scope.NSValue)
		b.paramSectionNeedsName = false
		b.prevByte = 'i'
		b.prevIdentIsThis = false
		return
	}

	// Inside a closure `use (...)` capture list: emit as a ref (binding
	// in enclosing scope handled by the scope chain; we're at file/fn
	// scope here, the closure scope hasn't pushed yet).
	if b.inUseClause {
		b.emitRef(name, span)
		b.prevByte = 'i'
		b.prevIdentIsThis = false
		return
	}

	// Class property declaration: `public int $x;`, `private $y = 0;`,
	// `static $z;`, or bare `var $w;`. Triggered by declContext =
	// KindField set by visibility / var / static / readonly modifiers.
	if b.declContext == scope.KindField {
		b.emitDecl(name, scope.KindField, span, scope.NSField)
		b.declContext = ""
		b.prevByte = 'i'
		b.prevIdentIsThis = false
		return
	}

	// `$this` identifier — emit as a ref (resolves to file-scope nothing
	// but the following `->X` will route through tryResolveThisField).
	if name == "$this" {
		b.emitRef(name, span)
		b.prevByte = 'i'
		b.prevIdentIsThis = true
		return
	}

	// Statement-start `$var = ...` at function or block scope: emit as
	// a var decl in the current scope. We recognise by peeking past ws
	// for `=` (but not `==`, `=>`).
	if wasStmtStart && b.isAssignTarget() {
		b.emitDecl(name, scope.KindVar, span, scope.NSValue)
		b.prevByte = 'i'
		b.prevIdentIsThis = false
		return
	}

	// Otherwise treat as a ref. Scope-chain resolution binds it to a
	// local if one has been seen; else unresolved.
	b.emitRef(name, span)
	b.prevByte = 'i'
	b.prevIdentIsThis = false
}

// isAssignTarget peeks past whitespace/comments for `=` followed by
// something other than `=`, `>`, `=`. Used to detect `$x = 1` vs `$x ==
// 1` / `$x => val`. Does not mutate scanner position.
func (b *builder) isAssignTarget() bool {
	save := b.s.Pos
	saveLine := b.s.Line
	defer func() {
		b.s.Pos = save
		b.s.Line = saveLine
	}()
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			b.s.Next()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '/' {
			b.s.SkipLineComment()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '*' {
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
			continue
		}
		if c != '=' {
			return false
		}
		next := b.s.PeekAt(1)
		return next != '=' && next != '>'
	}
	return false
}

func (b *builder) handleIdent(word []byte) {
	if len(word) == 0 {
		return
	}
	startByte := uint32(b.s.Pos - len(word))
	endByte := uint32(b.s.Pos)
	name := string(word)
	wasStmtStart := b.stmtStart
	b.stmtStart = false

	// Property access: `->name` or `::name`. Resolve `$this->name`
	// against the enclosing class if possible.
	if b.prevWasArrow || b.prevWasDoubleColon {
		wasArrow := b.prevWasArrow
		b.prevWasArrow = false
		b.prevWasDoubleColon = false
		if wasArrow && b.prevIdentIsThis {
			b.prevIdentIsThis = false
			if b.tryResolveThisField(name, mkSpan(startByte, endByte)) {
				b.prevByte = 'i'
				return
			}
		}
		b.prevIdentIsThis = false
		b.emitPropertyRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}
	b.prevIdentIsThis = false

	// Keywords that change parser state.
	switch phpLower(name) {
	case "function":
		// Could be a named function, a method (inside class body), an
		// anonymous closure, or a method signature. Emit decl on the
		// next ident if there is one, else open an anonymous function
		// scope on the next '{'.
		b.declContext = scope.KindFunction
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.paramListPending = true
		// A following `(` at top-of-statement without a name means
		// anonymous closure: the `(` handler will start param collection
		// anyway; the `{` will open the function scope even though no
		// decl was emitted.
		b.prevByte = 'k'
		return
	case "fn":
		// Short closure: `fn($x) => expr`.
		k := scope.ScopeFunction
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.paramListPending = true
		b.prevByte = 'k'
		return
	case "class":
		b.declContext = scope.KindClass
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "interface":
		b.declContext = scope.KindInterface
		k := scope.ScopeInterface
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "trait":
		b.declContext = scope.KindType
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "enum":
		b.declContext = scope.KindEnum
		k := scope.ScopeClass
		b.pendingScope = &k
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "namespace":
		b.handleNamespaceKeyword(startByte)
		return
	case "use":
		// Three forms:
		//   at stmt-start at file scope: `use Foo\Bar;` import
		//   at stmt-start in class body: `use Trait1, Trait2;` mixin
		//   following `)` of a closure param list: `function() use ($x)`
		if wasStmtStart && b.currentScopeKind() != scope.ScopeClass {
			b.handleUseImport()
			return
		}
		if wasStmtStart && b.currentScopeKind() == scope.ScopeClass {
			// Trait-use statement. Bind to the enclosing class decl
			// via the scope stack's ownerDeclIdx; capture trait
			// idents until the `;` terminator (or `{` for the
			// `use Trait { method as alias; }` adjustment block).
			if classIdx, ok := b.enclosingClassDeclIdx(); ok {
				b.pendingTraitUseIdx = classIdx
				b.traitUseLastIdent = ""
			}
			b.prevByte = 'k'
			return
		}
		// Closure capture-list form.
		b.inUseClause = true
		// The ( that follows will bump useDepth.
		b.prevByte = 'k'
		return
	case "const":
		// Inside a class: NSField class constant. Otherwise top-level const.
		if b.currentScopeKind() == scope.ScopeClass {
			b.classBodyConst = true
		}
		b.declContext = scope.KindConst
		b.pendingFullStart = startByte + 1
		b.prevByte = 'k'
		return
	case "public", "private", "protected", "var":
		// Property-visibility modifier. In a class body, the next token
		// decides: if it's `function`, the normal function handling takes
		// over (method); if it's `const`, the const path; else the next
		// $-ident is a property.
		if b.currentScopeKind() == scope.ScopeClass {
			b.declContext = scope.KindField
		}
		b.prevByte = 'k'
		return
	case "static":
		// Modifier for both methods and properties, or a scope token.
		// If we're in a class body, hold a potential field context; it'll
		// be cleared if `function` follows.
		if b.currentScopeKind() == scope.ScopeClass {
			if b.declContext == "" {
				b.declContext = scope.KindField
			}
		}
		b.prevByte = 'k'
		return
	case "abstract", "final", "readonly":
		// Modifier; keep declContext intact.
		b.prevByte = 'k'
		return
	case "extends", "implements":
		// Type-position ident(s) follow. The first ident in this
		// section (and the first after each top-level `,`) names a
		// supertype to record on the pending class. `extends`
		// terminates a prior implements section and vice versa, so
		// flush before re-arming.
		if b.pendingClassDeclIdx >= 0 {
			b.flushSuperTypeSection()
			b.inSuperTypes = true
			b.superTypeNeedsName = true
		}
		b.prevByte = 'k'
		return
	case "new":
		b.prevByte = 'k'
		return
	case "self", "parent":
		// These act as receivers for `::` access; emit as refs.
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	case "true", "false", "null":
		b.prevByte = 'k'
		return
	case "return", "throw", "if", "else", "elseif", "while", "for",
		"foreach", "do", "switch", "case", "default", "break",
		"continue", "try", "catch", "finally", "echo", "print",
		"and", "or", "xor", "as", "instanceof", "yield", "match",
		"global", "goto", "require", "require_once", "include",
		"include_once", "declare", "endif", "endwhile", "endforeach",
		"endfor", "endswitch", "list", "array", "isset", "unset",
		"empty":
		b.prevByte = 'k'
		return
	}

	// After `function` keyword, the next ident is the function/method name.
	if b.declContext == scope.KindFunction {
		scopeK := b.currentScopeKind()
		kind := scope.KindFunction
		ns := scope.NSValue
		if scopeK == scope.ScopeClass {
			kind = scope.KindMethod
			ns = scope.NSField
		}
		b.emitDecl(name, kind, mkSpan(startByte, endByte), ns)
		b.declContext = ""
		// paramListPending was set by "function"; we keep it.
		b.prevByte = 'i'
		return
	}

	// After `class`, `interface`, `trait`, `enum`: ident is the decl name.
	if b.declContext == scope.KindClass {
		idx := len(b.res.Decls)
		b.emitDecl(name, scope.KindClass, mkSpan(startByte, endByte), scope.NSValue)
		b.declContext = ""
		b.pendingClassDeclIdx = idx
		b.prevByte = 'i'
		return
	}
	if b.declContext == scope.KindInterface {
		idx := len(b.res.Decls)
		b.emitDecl(name, scope.KindInterface, mkSpan(startByte, endByte), scope.NSValue)
		b.declContext = ""
		b.pendingClassDeclIdx = idx
		b.prevByte = 'i'
		return
	}
	if b.declContext == scope.KindType {
		b.emitDecl(name, scope.KindType, mkSpan(startByte, endByte), scope.NSValue)
		b.declContext = ""
		b.prevByte = 'i'
		return
	}
	if b.declContext == scope.KindEnum {
		b.emitDecl(name, scope.KindEnum, mkSpan(startByte, endByte), scope.NSValue)
		b.declContext = ""
		b.prevByte = 'i'
		return
	}

	// After `const`: first ident is the const name.
	if b.declContext == scope.KindConst {
		ns := scope.NSValue
		if b.classBodyConst {
			ns = scope.NSField
		}
		b.emitDecl(name, scope.KindConst, mkSpan(startByte, endByte), ns)
		b.declContext = ""
		b.classBodyConst = false
		b.prevByte = 'i'
		return
	}

	// If we're still in a class body with declContext=KindField but the
	// ident isn't $-prefixed, this is a type hint for the upcoming $prop.
	// Emit as a ref (the type name), leave declContext for the next $ident.
	if b.declContext == scope.KindField {
		b.emitRef(name, mkSpan(startByte, endByte))
		b.prevByte = 'i'
		return
	}

	// In a param list, type-hint ident before the $param.
	if b.inParamList && b.paramDepth == 1 && b.paramSectionNeedsName {
		// It's a type — emit as a ref unless it's a primitive.
		if !isPHPTypeKeyword(name) {
			b.emitRef(name, mkSpan(startByte, endByte))
		}
		b.prevByte = 'i'
		return
	}

	// Otherwise: emit as a ref.
	b.emitRef(name, mkSpan(startByte, endByte))
	// Supertype name capture: while inside a class header's
	// `extends`/`implements` clause OR a class-body `use Trait1,
	// Trait2;` statement, record the latest ident as the candidate
	// supertype name. Qualified names `\Foo\Bar` overwrite on each
	// segment so the leaf wins (consistent with the cross-language
	// convention used by the hierarchy walker).
	if b.inSuperTypes {
		b.classBaseLastIdent = name
		b.superTypeNeedsName = false
	}
	if b.pendingTraitUseIdx >= 0 {
		b.traitUseLastIdent = name
	}
	b.prevByte = 'i'
}

// flushSuperTypeArg appends the captured base/interface ident to
// classSuperTypes and resets the per-section state. Called on `,`
// inside the extends/implements clause.
func (b *builder) flushSuperTypeArg() {
	if b.pendingClassDeclIdx < 0 || b.classBaseLastIdent == "" {
		b.classBaseLastIdent = ""
		b.superTypeNeedsName = true
		return
	}
	b.classSuperTypes[b.pendingClassDeclIdx] = append(
		b.classSuperTypes[b.pendingClassDeclIdx], b.classBaseLastIdent)
	b.classBaseLastIdent = ""
	b.superTypeNeedsName = true
}

// flushSuperTypeSection finalises the entire extends/implements
// window and clears inSuperTypes. Called on `{` (body opens) and
// when the keyword switches (`extends Base implements I` flushes
// "Base" before re-arming for the implements list).
func (b *builder) flushSuperTypeSection() {
	b.flushSuperTypeArg()
	b.inSuperTypes = false
	b.superTypeNeedsName = false
	b.classBaseLastIdent = ""
}

// flushTraitUseArg appends the captured trait ident to the
// enclosing class's classSuperTypes. Called on `,` inside a
// `use Trait1, Trait2;` statement.
func (b *builder) flushTraitUseArg() {
	if b.pendingTraitUseIdx < 0 || b.traitUseLastIdent == "" {
		b.traitUseLastIdent = ""
		return
	}
	b.classSuperTypes[b.pendingTraitUseIdx] = append(
		b.classSuperTypes[b.pendingTraitUseIdx], b.traitUseLastIdent)
	b.traitUseLastIdent = ""
}

// flushTraitUse finalises the trait-use statement on `;` and clears
// the tracker.
func (b *builder) flushTraitUse() {
	b.flushTraitUseArg()
	b.pendingTraitUseIdx = -1
	b.traitUseLastIdent = ""
}

// enclosingClassDeclIdx returns the decl index of the nearest
// enclosing class on the scope stack, or (0, false) if not inside a
// class body. Used by the trait-use trigger.
func (b *builder) enclosingClassDeclIdx() (int, bool) {
	entries := b.stack.Entries()
	for i := len(entries) - 1; i >= 0; i-- {
		e := &entries[i]
		if e.Data.kind != scope.ScopeClass {
			continue
		}
		if e.Data.ownerDeclIdx < 0 {
			continue
		}
		return e.Data.ownerDeclIdx, true
	}
	return 0, false
}

// handleNamespaceKeyword handles `namespace Name;` and `namespace Name { ... }`.
func (b *builder) handleNamespaceKeyword(kwStart uint32) {
	b.pendingFullStart = kwStart + 1
	b.skipWS()
	// Optional qualified name.
	var nameStart, nameEnd uint32
	var fullName string
	if lexkit.IsDefaultIdentStart(b.s.Peek()) {
		nameStart = uint32(b.s.Pos)
		// Scan qualified Name\Sub\Sub.
		for !b.s.EOF() {
			if !lexkit.IsDefaultIdentStart(b.s.Peek()) {
				break
			}
			for !b.s.EOF() && lexkit.DefaultIdentCont[b.s.Peek()] {
				b.s.Pos++
			}
			if b.s.Peek() == '\\' {
				b.s.Pos++
				continue
			}
			break
		}
		nameEnd = uint32(b.s.Pos)
		fullName = string(b.s.Src[nameStart:nameEnd])
	}
	b.skipWS()
	next := byte(0)
	if !b.s.EOF() {
		next = b.s.Peek()
	}
	if fullName != "" {
		// Emit the leaf component as the namespace decl name (similar
		// to Ruby modules).
		leaf := fullName
		leafStart := nameStart
		for i := len(fullName) - 1; i >= 0; i-- {
			if fullName[i] == '\\' {
				leaf = fullName[i+1:]
				leafStart = nameStart + uint32(i+1)
				break
			}
		}
		b.emitDecl(leaf, scope.KindNamespace, mkSpan(leafStart, leafStart+uint32(len(leaf))), scope.NSNamespace)
		// Stash the full backslash-qualified namespace path on the
		// just-emitted decl's Signature so the cross-file import
		// resolver (internal/scope/store/imports_php.go) can compute
		// FQN("<namespace>\\<DeclName>") for file-scope decls.
		if n := len(b.res.Decls); n > 0 {
			b.res.Decls[n-1].Signature = fullName
		}
	}
	if next == '{' {
		// Block form: the next '{' opens the namespace scope.
		k := scope.ScopeNamespace
		b.pendingScope = &k
	} else {
		// Statement form: push a namespace scope that stays open until
		// the next `namespace` statement or EOF. We model this by
		// opening a ScopeNamespace right here; it'll close at EOF.
		// If a previous statement-form namespace scope is already open,
		// close it first so they don't nest incorrectly.
		b.closeStatementNamespaceIfOpen()
		b.openScope(scope.ScopeNamespace, kwStart)
	}
	b.prevByte = 'k'
}

// closeStatementNamespaceIfOpen closes the top-of-stack scope if and only
// if it is a ScopeNamespace that was opened by a statement-form namespace
// declaration (i.e., not enclosing a `{ ... }` block). Heuristic: pop the
// top ScopeNamespace if it is not the file scope and has no ownerDeclIdx
// mismatch (we can't easily tell block form from statement form at pop
// time, so we conservatively only pop if it is the top).
func (b *builder) closeStatementNamespaceIfOpen() {
	top := b.stack.Top()
	if top == nil {
		return
	}
	if top.Data.kind != scope.ScopeNamespace {
		return
	}
	b.closeTopScope(uint32(b.s.Pos))
}

// handleUseImport parses `use Foo\Bar;`, `use Foo\Bar as Baz;`, and
// `use Foo\{A, B as C};`. Consumes up through the terminating `;`.
//
// Populates Decl.Signature = "[<prefix>:]<modulePath>\x00<origName>"
// on each emitted KindImport decl, where modulePath is the backslash-
// separated namespace prefix (no trailing backslash) and origName is
// the imported symbol's own name in that namespace (differs from the
// emitted decl name when aliased with `as`). `<prefix>` is "function"
// or "const" for `use function` / `use const`; omitted (no leading
// colon) for plain class/interface/trait/enum imports. Consumed by
// internal/scope/store/imports_php.go.
func (b *builder) handleUseImport() {
	// Optional `function` or `const` prefix (PHP allows `use function
	// strlen;` etc.). Recognise it so the resolver can route function
	// and const imports separately from type imports.
	b.skipWS()
	save := b.s.Pos
	sigPrefix := "" // "", "function", or "const"
	if lexkit.IsDefaultIdentStart(b.s.Peek()) {
		word := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
		wl := phpLower(string(word))
		if wl == "function" || wl == "const" {
			sigPrefix = wl
		} else {
			b.s.Pos = save
		}
	}
	b.skipWS()
	// Scan qualified name.
	pathStart := b.s.Pos
	path := b.scanQualifiedName()
	b.skipWS()
	// Grouped form: `use Foo\{...}`. scanQualifiedName stops before the
	// trailing `\` when followed by a non-ident; advance past it here
	// so we recognise the `{`.
	if b.s.Peek() == '\\' && b.s.PeekAt(1) == '{' {
		b.s.Pos++
	}
	if b.s.Peek() == '{' {
		// Grouped: `use Foo\{A, B as C};`. Path is the group prefix
		// (with trailing `\` stripped).
		groupPrefix := trimTrailingBackslash(path)
		b.s.Pos++ // consume '{'
		for !b.s.EOF() {
			b.skipWS()
			if b.s.Peek() == '}' {
				b.s.Pos++
				break
			}
			// Optional per-entry `function`/`const` prefix. Overrides
			// the group-level sigPrefix for this entry.
			entryPrefix := sigPrefix
			svp := b.s.Pos
			if lexkit.IsDefaultIdentStart(b.s.Peek()) {
				w := b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				wl := phpLower(string(w))
				if wl == "function" || wl == "const" {
					entryPrefix = wl
				} else {
					b.s.Pos = svp
				}
			}
			b.skipWS()
			subStart := b.s.Pos
			sub := b.scanQualifiedName()
			subEnd := b.s.Pos
			if sub == "" {
				b.s.Pos++
				continue
			}
			b.skipWS()
			leaf, leafOff := leafOfQualified(sub)
			emitName := leaf
			emitStart := uint32(subStart + leafOff)
			emitEnd := uint32(subEnd)
			origName := leaf
			if b.peekIdentWordCI("as") {
				b.s.Advance(2)
				b.skipWS()
				if lexkit.IsDefaultIdentStart(b.s.Peek()) {
					ast := b.s.Pos
					_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
					aen := b.s.Pos
					emitName = string(b.s.Src[ast:aen])
					emitStart = uint32(ast)
					emitEnd = uint32(aen)
				}
			}
			b.emitDecl(emitName, scope.KindImport, mkSpan(emitStart, emitEnd), scope.NSValue)
			// modulePath = groupPrefix (+ "\\" + subPrefix)?. subPrefix
			// is everything in `sub` before the leaf, trimmed.
			modulePath := groupPrefix
			subPrefix := trimTrailingBackslash(sub[:leafOff])
			if subPrefix != "" {
				if modulePath != "" {
					modulePath = modulePath + "\\" + subPrefix
				} else {
					modulePath = subPrefix
				}
			}
			b.stampImportSignature(modulePath, origName, entryPrefix)
			b.skipWS()
			if b.s.Peek() == ',' {
				b.s.Pos++
			}
		}
	} else {
		// Simple or aliased: `use Foo\Bar;` or `use Foo\Bar as Baz;`.
		if path == "" {
			return
		}
		leaf, leafOff := leafOfQualified(path)
		emitName := leaf
		emitStart := uint32(pathStart + leafOff)
		emitEnd := uint32(pathStart + len(path))
		origName := leaf
		if b.peekIdentWordCI("as") {
			b.s.Advance(2)
			b.skipWS()
			if lexkit.IsDefaultIdentStart(b.s.Peek()) {
				ast := b.s.Pos
				_ = b.s.ScanIdentTable(&lexkit.DefaultIdentStart, &lexkit.DefaultIdentCont)
				aen := b.s.Pos
				emitName = string(b.s.Src[ast:aen])
				emitStart = uint32(ast)
				emitEnd = uint32(aen)
			}
		}
		b.emitDecl(emitName, scope.KindImport, mkSpan(emitStart, emitEnd), scope.NSValue)
		// modulePath = everything before the leaf, trailing `\` dropped.
		// `use Foo\Bar;` -> "Foo"; `use \Foo\Bar;` -> "\\Foo"; `use Bar;`
		// -> "" (resolver strips a leading `\\` and treats empty-path as
		// global-namespace FQN).
		modulePath := trimTrailingBackslash(path[:leafOff])
		b.stampImportSignature(modulePath, origName, sigPrefix)
	}
	// Consume through ';'.
	for !b.s.EOF() && b.s.Peek() != ';' && b.s.Peek() != '\n' {
		b.s.Pos++
	}
	b.prevByte = 'k'
}

// stampImportSignature writes Signature on the most recently emitted
// KindImport decl. Format: "[<prefix>:]<modulePath>\x00<origName>".
// No-op if the last decl is not a KindImport (defensive).
func (b *builder) stampImportSignature(modulePath, origName, prefix string) {
	n := len(b.res.Decls)
	if n == 0 {
		return
	}
	d := &b.res.Decls[n-1]
	if d.Kind != scope.KindImport {
		return
	}
	if prefix != "" {
		d.Signature = prefix + ":" + modulePath + "\x00" + origName
	} else {
		d.Signature = modulePath + "\x00" + origName
	}
}

// trimTrailingBackslash strips a single trailing `\` from a qualified
// PHP name (as produced by scanQualifiedName when parsing group-form
// `use Foo\{…}`).
func trimTrailingBackslash(s string) string {
	if n := len(s); n > 0 && s[n-1] == '\\' {
		return s[:n-1]
	}
	return s
}

// scanQualifiedName reads a possibly-qualified PHP name like Foo\Bar\Baz.
// Returns the full string. An empty return means there was no name.
func (b *builder) scanQualifiedName() string {
	start := b.s.Pos
	// Optional leading backslash (absolute name).
	if b.s.Peek() == '\\' {
		b.s.Pos++
	}
	for !b.s.EOF() {
		if !lexkit.IsDefaultIdentStart(b.s.Peek()) {
			break
		}
		for !b.s.EOF() && lexkit.DefaultIdentCont[b.s.Peek()] {
			b.s.Pos++
		}
		if b.s.Peek() == '\\' {
			// Peek past: if no ident follows, stop (could be `use Foo\{...}`).
			if !lexkit.IsDefaultIdentStart(b.s.PeekAt(1)) {
				break
			}
			b.s.Pos++
			continue
		}
		break
	}
	return string(b.s.Src[start:b.s.Pos])
}

// leafOfQualified returns (leaf, offset). Offset is where the leaf starts
// within the qualified path.
func leafOfQualified(path string) (string, int) {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '\\' {
			return path[i+1:], i + 1
		}
	}
	return path, 0
}

// handleOpenBrace opens a scope. Uses pendingScope if set; else ScopeBlock.
// For class/function/interface scopes, flushes the owner decl's FullSpan.
func (b *builder) handleOpenBrace() {
	startByte := uint32(b.s.Pos)
	b.s.Pos++
	b.prevByte = '{'
	kind := scope.ScopeBlock
	if b.pendingScope != nil {
		kind = *b.pendingScope
		b.pendingScope = nil
	}
	// Class/interface body opens — close the supertype window and
	// flush the trailing captured name onto Decl.SuperTypes. Done
	// unconditionally so a malformed class with no `{` body can't
	// leak state into the next class.
	if b.inSuperTypes {
		b.flushSuperTypeSection()
	}
	if kind == scope.ScopeClass || kind == scope.ScopeInterface {
		b.pendingClassDeclIdx = -1
	}
	b.openScope(kind, startByte)
	b.stmtStart = true
}

func (b *builder) handleCloseBrace() {
	b.s.Pos++
	b.prevByte = '}'
	b.closeTopScope(uint32(b.s.Pos))
	b.stmtStart = true
	b.declContext = ""
}

// closeArrowIfTerminating closes any open `fn()=>expr` arrow-function
// scope whose closeAtParenZero trigger has fired (ambient parenDepth is
// now strictly less than the stored level).
func (b *builder) closeArrowIfTerminating() {
	for {
		top := b.stack.Top()
		if top == nil {
			return
		}
		caz := top.Data.closeAtParenZero
		if caz == 0 {
			return
		}
		// The arrow closes when parenDepth < caz-1. Since `fn(x) => expr`
		// is typically at parenDepth 0, caz is 1 and we close when depth
		// returns to 0 after a terminator. Use <=; when we're at ',', ';',
		// or `)` we're wrapping up.
		if b.parenDepth >= caz {
			return
		}
		b.closeTopScope(uint32(b.s.Pos))
	}
}

// openScopeArrow opens a function scope for an expression-body arrow
// (`fn(params) => expr`) and records the paren-depth level at which it
// should close.
func (b *builder) openScopeArrow(kind scope.ScopeKind, startByte uint32, closeAtDepth int) {
	b.openScope(kind, startByte)
	// Mark the scope entry.
	top := b.stack.Top()
	if top != nil {
		top.Data.closeAtParenZero = closeAtDepth
	}
}

func (b *builder) openScope(kind scope.ScopeKind, startByte uint32) {
	id := scope.ScopeID(len(b.res.Scopes) + 1)
	var parent scope.ScopeID
	if top := b.stack.Top(); top != nil {
		parent = top.Data.id
	}
	b.res.Scopes = append(b.res.Scopes, scope.Scope{
		ID:     id,
		Parent: parent,
		Kind:   kind,
		Span:   scope.Span{StartByte: startByte, EndByte: 0},
	})
	owner := b.pendingOwnerDecl
	b.pendingOwnerDecl = -1
	b.stack.Push(lexkit.Scope[scopeEntry]{
		Data: scopeEntry{
			kind:         kind,
			id:           id,
			ownerDeclIdx: owner,
		},
		SymIdx:   -1,
		OpenLine: b.s.Line,
	})
}

func (b *builder) closeTopScope(endByte uint32) {
	e, ok := b.stack.Pop()
	if !ok {
		return
	}
	idx := int(e.Data.id) - 1
	if idx >= 0 && idx < len(b.res.Scopes) {
		b.res.Scopes[idx].Span.EndByte = endByte
	}
	if o := e.Data.ownerDeclIdx; o >= 0 && o < len(b.res.Decls) {
		if b.res.Decls[o].FullSpan.EndByte < endByte {
			b.res.Decls[o].FullSpan.EndByte = endByte
		}
	}
}

func (b *builder) closeScopesToDepth(depth int) {
	endByte := uint32(len(b.s.Src))
	for b.stack.Depth() > depth {
		b.closeTopScope(endByte)
	}
}

func (b *builder) currentScope() scope.ScopeID {
	if top := b.stack.Top(); top != nil {
		return top.Data.id
	}
	return 0
}

func (b *builder) currentScopeKind() scope.ScopeKind {
	if top := b.stack.Top(); top != nil {
		return top.Data.kind
	}
	return ""
}

// emitDecl emits a decl and applies PHP's type-vs-value namespace split.
// Callers pass the "natural" namespace (mostly NSValue at file scope),
// but type-owner kinds are remapped:
//   - interface / trait (KindType used for traits) → NSType only
//   - class / enum → DUAL emit: primary NSValue + shadow NSType (so
//     `$x instanceof Foo`, `function f(Foo $x)`, `new Foo()`, `Foo::X`
//     all resolve to the same merged decl). scope.MergeDuplicateDecls
//     unifies the DeclIDs after Parse.
//
// Other kinds (function/const/var/field/param/namespace/import) use the
// namespace the caller supplied unchanged.
func (b *builder) emitDecl(name string, kind scope.DeclKind, span scope.Span, ns scope.Namespace) {
	primaryNs := ns
	var dualNs scope.Namespace
	switch kind {
	case scope.KindInterface:
		primaryNs = scope.NSType
	case scope.KindType:
		// PHP builder uses KindType for `trait X`; traits bind in the
		// type namespace (you `use TraitX` in a class body — that's a
		// type-shaped ref).
		primaryNs = scope.NSType
	case scope.KindClass, scope.KindEnum:
		// Keep primary NSValue (constructor / static access / enum
		// cases) and add a shadow NSType decl for type-position refs.
		dualNs = scope.NSType
	}

	idx := b.appendDecl(name, kind, span, primaryNs)
	shadowIdx := -1
	if dualNs != "" && dualNs != primaryNs {
		shadowIdx = b.appendDecl(name, kind, span, dualNs)
	}

	// PHP has no explicit export keyword: default visibility at file or
	// namespace scope is public. Mark file-scope / namespace-scope
	// classes, interfaces, traits (KindType), enums, functions, and
	// top-level constants as Exported so the cross-file import resolver
	// (internal/scope/store/imports_php.go) can rewrite refs to them.
	// `private` / `protected` only apply inside class bodies; those
	// decls (methods/fields) emit under class scopes, which this check
	// excludes.
	if isTopLevelExportKind(kind) && b.isFileOrNamespaceScope() {
		b.res.Decls[idx].Exported = true
		if shadowIdx >= 0 {
			b.res.Decls[shadowIdx].Exported = true
		}
	}

	switch kind {
	case scope.KindFunction, scope.KindMethod, scope.KindClass,
		scope.KindInterface, scope.KindEnum, scope.KindType,
		scope.KindNamespace:
		b.pendingOwnerDecl = idx
	}
	b.pendingFullStart = 0
}

// isTopLevelExportKind reports whether kind is one of PHP's file-scope
// publicly-visible decl kinds (under PHP's implicit-public semantics).
func isTopLevelExportKind(kind scope.DeclKind) bool {
	switch kind {
	case scope.KindClass, scope.KindInterface, scope.KindType,
		scope.KindEnum, scope.KindFunction, scope.KindConst:
		return true
	}
	return false
}

// isFileOrNamespaceScope reports whether the current emit scope is the
// file scope or a namespace scope (block or statement form). Everything
// else (class/function/block body) is non-exporting.
func (b *builder) isFileOrNamespaceScope() bool {
	k := b.currentScopeKind()
	return k == scope.ScopeFile || k == scope.ScopeNamespace
}

// appendDecl appends a single Decl row and returns its index. Used by
// emitDecl (which handles namespace selection and dual-emit for types)
// and can be called directly when a caller has already resolved the
// final namespace.
func (b *builder) appendDecl(name string, kind scope.DeclKind, span scope.Span, ns scope.Namespace) int {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	// File-scope decls hash with canonicalPath when set, so
	// cross-file references through imports/includes bind to
	// matching DeclIDs. Nested-scope decls keep the file path.
	hashPath := b.file
	if scopeID == scope.ScopeID(1) && b.canonicalPath != "" {
		hashPath = b.canonicalPath
	}
	declID := hashDecl(hashPath, name, ns, scopeID)

	var fullStart uint32
	if b.pendingFullStart > 0 && b.pendingFullStart-1 <= span.StartByte {
		fullStart = b.pendingFullStart - 1
	} else {
		fullStart = span.StartByte
	}
	fullSpan := scope.Span{StartByte: fullStart, EndByte: span.EndByte}

	idx := len(b.res.Decls)
	b.res.Decls = append(b.res.Decls, scope.Decl{
		ID:        declID,
		LocID:     locID,
		Name:      name,
		Namespace: ns,
		Kind:      kind,
		Scope:     scopeID,
		File:      b.file,
		Span:      span,
		FullSpan:  fullSpan,
	})
	return idx
}

func (b *builder) emitRef(name string, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     locID,
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSValue,
		Scope:     scopeID,
	})
}

func (b *builder) emitPropertyRef(name string, span scope.Span) {
	scopeID := b.currentScope()
	locID := hashLoc(b.file, span, name)
	b.res.Refs = append(b.res.Refs, scope.Ref{
		LocID:     locID,
		File:      b.file,
		Span:      span,
		Name:      name,
		Namespace: scope.NSField,
		Scope:     scopeID,
		Binding: scope.RefBinding{
			Kind:   scope.BindProbable,
			Reason: "property_access",
		},
	})
}

// tryResolveThisField attempts to resolve `$this->name` at `span` against
// the nearest enclosing class scope's NSField decls. Returns true if a
// match was found (and a resolved ref was emitted); false otherwise.
func (b *builder) tryResolveThisField(name string, span scope.Span) bool {
	entries := b.stack.Entries()
	var classScope scope.ScopeID
	for i := len(entries) - 1; i >= 0; i-- {
		k := entries[i].Data.kind
		if k == scope.ScopeClass || k == scope.ScopeInterface {
			classScope = entries[i].Data.id
			break
		}
	}
	if classScope == 0 {
		return false
	}
	// PHP properties are declared as `$foo` (name includes `$`), but
	// accessed as `$this->foo` (name without `$`). Methods have no `$`.
	// Try both: bare name (methods) and `$`-prefixed (properties).
	altName := "$" + name
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		if d.Scope != classScope || d.Namespace != scope.NSField {
			continue
		}
		if d.Name != name && d.Name != altName {
			continue
		}
		scopeID := b.currentScope()
		locID := hashLoc(b.file, span, name)
		b.res.Refs = append(b.res.Refs, scope.Ref{
			LocID:     locID,
			File:      b.file,
			Span:      span,
			Name:      name,
			Namespace: scope.NSField,
			Scope:     scopeID,
			Binding: scope.RefBinding{
				Kind:   scope.BindResolved,
				Decl:   d.ID,
				Reason: "this_dot_field",
			},
		})
		return true
	}
	return false
}

// resolveRefs walks each Ref's scope chain and binds it to the innermost
// matching Decl, if any. Pre-bound refs (property_access, this_dot_field)
// are left alone.
func (b *builder) resolveRefs() {
	parent := make(map[scope.ScopeID]scope.ScopeID, len(b.res.Scopes))
	for _, s := range b.res.Scopes {
		parent[s.ID] = s.Parent
	}
	type key struct {
		scope scope.ScopeID
		name  string
		ns    scope.Namespace
	}
	byKey := make(map[key]*scope.Decl, len(b.res.Decls))
	for i := range b.res.Decls {
		d := &b.res.Decls[i]
		k := key{scope: d.Scope, name: d.Name, ns: d.Namespace}
		if _, ok := byKey[k]; !ok {
			byKey[k] = d
		}
	}
	for i := range b.res.Refs {
		r := &b.res.Refs[i]
		if r.Binding.Reason == "property_access" || r.Binding.Reason == "this_dot_field" {
			continue
		}
		resolved := false
		// Try the ref's own namespace first; on miss, fall back to the
		// other value/type namespace. PHP ref-emission doesn't yet tag
		// type-position refs (annotations, extends, implements,
		// instanceof) as NSType — they all come in as NSValue — so we
		// need the fallback so a ref to an `interface Foo` (NSType
		// only) still resolves.
		alt := altNamespace(r.Namespace)
		namespaces := []scope.Namespace{r.Namespace}
		if alt != r.Namespace {
			namespaces = append(namespaces, alt)
		}
		for _, ns := range namespaces {
			cur := r.Scope
			for {
				if d, ok := byKey[key{scope: cur, name: r.Name, ns: ns}]; ok {
					r.Binding = scope.RefBinding{
						Kind:   scope.BindResolved,
						Decl:   d.ID,
						Reason: "direct_scope",
					}
					resolved = true
					break
				}
				p, ok := parent[cur]
				if !ok {
					break
				}
				if p == 0 && cur != 0 {
					if d, ok := byKey[key{scope: 0, name: r.Name, ns: ns}]; ok {
						r.Binding = scope.RefBinding{
							Kind:   scope.BindResolved,
							Decl:   d.ID,
							Reason: "direct_scope",
						}
						resolved = true
					}
					break
				}
				if cur == 0 {
					break
				}
				cur = p
			}
			if resolved {
				break
			}
		}
		if !resolved {
			if builtins.PHP.Has(r.Name) {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindResolved,
					Decl:   hashBuiltinDecl(r.Name),
					Reason: "builtin",
				}
			} else {
				r.Binding = scope.RefBinding{
					Kind:   scope.BindUnresolved,
					Reason: "missing_import",
				}
			}
		}
	}
}

// scanDoubleString scans a double-quoted string body. For v1 we treat
// interpolation boundaries as literal — we do NOT emit refs for `$var`
// inside the string, we just scan through. This matches the "optional"
// note in the task description.
func (b *builder) scanDoubleString() {
	if b.s.Peek() != '"' {
		return
	}
	b.s.Pos++ // opening "
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == '\\' && b.s.PeekAt(1) != 0 {
			b.s.Advance(2)
			continue
		}
		if c == '"' {
			b.s.Pos++
			return
		}
		if c == '{' && b.s.PeekAt(1) == '$' {
			// `{$var}` interpolation — skip through matching '}'.
			b.s.Advance(2)
			depth := 1
			for !b.s.EOF() && depth > 0 {
				cc := b.s.Peek()
				switch cc {
				case '{':
					depth++
					b.s.Pos++
				case '}':
					depth--
					b.s.Pos++
				case '\\':
					b.s.Advance(2)
				case '"':
					// Nested string inside interp.
					b.s.ScanSimpleString('"')
				case '\'':
					b.s.ScanSimpleString('\'')
				default:
					b.s.Pos++
				}
			}
			continue
		}
		b.s.Pos++
	}
}

// scanHeredoc scans a heredoc `<<<TAG ... TAG;` or nowdoc `<<<'TAG' ... TAG;`.
// Body is treated as string data. Tag match is by line: a line whose
// trimmed content equals the tag ends the heredoc.
func (b *builder) scanHeredoc() {
	// We're at `<<<`.
	b.s.Advance(3)
	// Optional whitespace/tabs.
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' {
			b.s.Pos++
			continue
		}
		break
	}
	// Optional quote (nowdoc = single-quote).
	quote := byte(0)
	if b.s.Peek() == '\'' || b.s.Peek() == '"' {
		quote = b.s.Peek()
		b.s.Pos++
	}
	// Tag.
	tagStart := b.s.Pos
	for !b.s.EOF() && lexkit.DefaultIdentCont[b.s.Peek()] {
		b.s.Pos++
	}
	tag := string(b.s.Src[tagStart:b.s.Pos])
	if quote != 0 && b.s.Peek() == quote {
		b.s.Pos++
	}
	if tag == "" {
		return
	}
	// Skip to newline.
	for !b.s.EOF() && b.s.Peek() != '\n' {
		b.s.Pos++
	}
	if !b.s.EOF() {
		b.s.Next()
	}
	// Scan lines until one whose trimmed content begins with the tag.
	for !b.s.EOF() {
		lineStart := b.s.Pos
		for !b.s.EOF() && b.s.Peek() != '\n' {
			b.s.Pos++
		}
		lineEnd := b.s.Pos
		// Trim leading spaces/tabs.
		t := lineStart
		for t < lineEnd && (b.s.Src[t] == ' ' || b.s.Src[t] == '\t') {
			t++
		}
		// Check tag prefix with non-ident-cont boundary.
		if t+len(tag) <= lineEnd &&
			string(b.s.Src[t:t+len(tag)]) == tag &&
			(t+len(tag) == lineEnd || !lexkit.DefaultIdentCont[b.s.Src[t+len(tag)]]) {
			// Found end. Advance past the terminator.
			b.s.Pos = t + len(tag)
			return
		}
		if !b.s.EOF() {
			b.s.Next()
		}
	}
}

// skipAttributeBody skips through a PHP 8 attribute `#[Attr(...)]`. We've
// already consumed `#[`. Scans balanced `]` bracket, handling strings.
func (b *builder) skipAttributeBody() {
	depth := 1
	for !b.s.EOF() && depth > 0 {
		c := b.s.Peek()
		switch c {
		case '[':
			depth++
			b.s.Pos++
		case ']':
			depth--
			b.s.Pos++
		case '"':
			b.scanDoubleString()
		case '\'':
			b.s.ScanSimpleString('\'')
		case '\n':
			b.s.Next()
		default:
			b.s.Pos++
		}
	}
}

func (b *builder) skipWS() {
	for !b.s.EOF() {
		c := b.s.Peek()
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			b.s.Next()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '/' {
			b.s.SkipLineComment()
			continue
		}
		if c == '/' && b.s.PeekAt(1) == '*' {
			b.s.Advance(2)
			b.s.SkipBlockComment("*/")
			continue
		}
		if c == '#' && b.s.PeekAt(1) != '[' {
			b.s.SkipLineComment()
			continue
		}
		break
	}
}

// peekIdentWordCI reports whether the scanner is at a case-insensitive
// whole-word match for kw. Does not advance. kw must be lowercase ASCII.
func (b *builder) peekIdentWordCI(kw string) bool {
	if b.s.Pos+len(kw) > len(b.s.Src) {
		return false
	}
	for i := 0; i < len(kw); i++ {
		c := b.s.Src[b.s.Pos+i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != kw[i] {
			return false
		}
	}
	next := b.s.PeekAt(len(kw))
	if next == 0 {
		return true
	}
	return !lexkit.DefaultIdentCont[next]
}

// phpLower returns s lowercased ASCII. PHP keywords are case-insensitive.
func phpLower(s string) string {
	needs := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			needs = true
			break
		}
	}
	if !needs {
		return s
	}
	buf := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		buf[i] = c
	}
	return string(buf)
}

// isPHPTypeKeyword reports whether name is a primitive PHP type keyword
// that shouldn't be emitted as a ref (and thus not resolved/renamed).
func isPHPTypeKeyword(name string) bool {
	switch phpLower(name) {
	case "int", "integer", "float", "double", "string", "bool",
		"boolean", "array", "object", "mixed", "void", "iterable",
		"callable", "self", "static", "parent", "never", "false",
		"true", "null", "resource":
		return true
	}
	return false
}

func mkSpan(start, end uint32) scope.Span {
	return scope.Span{StartByte: start, EndByte: end}
}

func hashLoc(file string, span scope.Span, name string) scope.LocID {
	h := sha256.New()
	h.Write([]byte(file))
	h.Write([]byte{0})
	h.Write([]byte(name))
	h.Write([]byte{0})
	var buf [8]byte
	binary.LittleEndian.PutUint32(buf[0:4], span.StartByte)
	binary.LittleEndian.PutUint32(buf[4:8], span.EndByte)
	h.Write(buf[:])
	sum := h.Sum(nil)
	return scope.LocID(binary.LittleEndian.Uint64(sum[:8]))
}

func hashBuiltinDecl(name string) scope.DeclID {
	h := sha256.New()
	h.Write([]byte("<builtin:php>"))
	h.Write([]byte{0})
	h.Write([]byte(name))
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}

func hashDecl(canonicalPath, name string, ns scope.Namespace, scopeID scope.ScopeID) scope.DeclID {
	h := sha256.New()
	h.Write([]byte(canonicalPath))
	h.Write([]byte{0})
	h.Write([]byte(name))
	h.Write([]byte{0})
	h.Write([]byte(ns))
	h.Write([]byte{0})
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], uint32(scopeID))
	h.Write(buf[:])
	sum := h.Sum(nil)
	return scope.DeclID(binary.LittleEndian.Uint64(sum[:8]))
}

// altNamespace flips between value and type namespaces for the
// resolveRefs fallback. Non-value/non-type namespaces (NSField,
// NSNamespace) are returned unchanged.
func altNamespace(ns scope.Namespace) scope.Namespace {
	switch ns {
	case scope.NSValue:
		return scope.NSType
	case scope.NSType:
		return scope.NSValue
	}
	return ns
}
