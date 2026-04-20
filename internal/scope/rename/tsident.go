package rename

// TS v1 identifier validation. ASCII-only on purpose: the TS builder
// itself uses ASCII-only ident tables (builder.go:identStart/identCont),
// so accepting non-ASCII newNames here would let rename create names
// the builder cannot re-parse.

func isValidTSIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 0 {
			if !isIdentStart(c) {
				return false
			}
			continue
		}
		if !isIdentCont(c) {
			return false
		}
	}
	return true
}

func isIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '$'
}

func isIdentCont(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// tsReserved combines ES2022 reserved words, strict-mode reserved
// words, contextual keywords whose use as bindings is syntactically
// ambiguous (await, yield), TS-specific keywords, TS type-level
// primitives (any, number, string, …), and runtime literals that
// can't legally be bound (null, true, false, undefined, this).
//
// Type primitives are included even though TS technically allows
// them as identifiers at value scope: renaming to them is
// overwhelmingly a mistake and would make any type annotation
// referencing the new name ambiguous between binding and type.
var tsReserved = map[string]bool{
	"break": true, "case": true, "catch": true, "class": true, "const": true,
	"continue": true, "debugger": true, "default": true, "delete": true, "do": true,
	"else": true, "enum": true, "export": true, "extends": true, "false": true,
	"finally": true, "for": true, "function": true, "if": true, "import": true,
	"in": true, "instanceof": true, "new": true, "null": true, "return": true,
	"super": true, "switch": true, "this": true, "throw": true, "true": true,
	"try": true, "typeof": true, "var": true, "void": true, "while": true, "with": true,
	"yield": true,

	"implements": true, "interface": true, "let": true, "package": true,
	"private": true, "protected": true, "public": true, "static": true,

	"await": true, "async": true,

	"undefined": true,

	// TS type-level primitives + contextual keywords. Refusing on these
	// avoids footguns in type annotations and declaration merging.
	"any": true, "number": true, "string": true, "boolean": true,
	"unknown": true, "never": true, "object": true, "symbol": true,
	"bigint": true, "type": true, "namespace": true, "module": true,
	"as": true, "from": true, "of": true, "satisfies": true, "keyof": true,
	"readonly": true, "abstract": true, "declare": true, "is": true,
}

func isTSReservedWord(s string) bool {
	return tsReserved[s]
}
