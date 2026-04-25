package dispatch

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/jordw/edr/internal/index"
	"github.com/jordw/edr/internal/scope/namespace"
)

// goStdlibIface is one entry in the conformance catalog: an interface
// whose method set the rename pass cannot follow into the stdlib (the
// interface declaration lives in source we can't edit).
type goStdlibIface struct {
	pkg     string   // import path, "" for builtin (e.g. error)
	name    string   // interface name within the package
	methods []string // full method set
}

// goStdlibInterfaces enumerates well-known stdlib interfaces. Only
// interfaces whose method set is small AND distinctive enough that
// presence of all methods on a type strongly implies conformance.
// Conservative by design — over-refusal is preferred to silently
// breaking compile (the failure mode of unguarded stdlib renames).
var goStdlibInterfaces = []goStdlibIface{
	{"", "error", []string{"Error"}},
	{"io", "Reader", []string{"Read"}},
	{"io", "Writer", []string{"Write"}},
	{"io", "Closer", []string{"Close"}},
	{"io", "ReadCloser", []string{"Read", "Close"}},
	{"io", "WriteCloser", []string{"Write", "Close"}},
	{"io", "ReadWriter", []string{"Read", "Write"}},
	{"io", "ReadWriteCloser", []string{"Read", "Write", "Close"}},
	{"io", "Seeker", []string{"Seek"}},
	{"io", "ReaderAt", []string{"ReadAt"}},
	{"io", "WriterAt", []string{"WriteAt"}},
	{"io", "ByteReader", []string{"ReadByte"}},
	{"io", "ByteWriter", []string{"WriteByte"}},
	{"io", "RuneReader", []string{"ReadRune"}},
	{"io", "StringWriter", []string{"WriteString"}},
	{"fmt", "Stringer", []string{"String"}},
	{"fmt", "GoStringer", []string{"GoString"}},
	{"fmt", "Formatter", []string{"Format"}},
	{"fmt", "Scanner", []string{"Scan"}},
	{"encoding/json", "Marshaler", []string{"MarshalJSON"}},
	{"encoding/json", "Unmarshaler", []string{"UnmarshalJSON"}},
	{"encoding", "TextMarshaler", []string{"MarshalText"}},
	{"encoding", "TextUnmarshaler", []string{"UnmarshalText"}},
	{"encoding", "BinaryMarshaler", []string{"MarshalBinary"}},
	{"encoding", "BinaryUnmarshaler", []string{"UnmarshalBinary"}},
	{"sort", "Interface", []string{"Len", "Less", "Swap"}},
	{"net/http", "Handler", []string{"ServeHTTP"}},
	{"context", "Context", []string{"Deadline", "Done", "Err", "Value"}},
	{"database/sql", "Scanner", []string{"Scan"}},
	{"database/sql/driver", "Valuer", []string{"Value"}},
	{"hash", "Hash", []string{"Write", "Sum", "Reset", "Size", "BlockSize"}},
	{"flag", "Value", []string{"String", "Set"}},
}

// goConflictingStdlibIface returns the package + name of a stdlib
// interface that the rename target's receiver type satisfies AND
// whose method set includes the rename target's name. Empty strings
// mean no conflict — proceed with the rename.
//
// Returns non-empty when ALL of:
//   - target is a method (sym.Receiver != "")
//   - target's file is a .go file
//   - the receiver type defines every method in the interface's set
//     (across the file + same-package siblings)
//   - the interface's package is imported by the target's file
//     (or the interface is a builtin like `error`)
//
// Refusing here protects the caller from a silent compile break: the
// stdlib interface declaration can't be renamed to match, so any code
// that uses our type as that interface will stop compiling.
func goConflictingStdlibIface(ctx context.Context, db index.SymbolStore, sym *index.SymbolInfo) (pkg, name string) {
	if sym.Receiver == "" {
		return "", ""
	}
	if !strings.EqualFold(filepath.Ext(sym.File), ".go") {
		return "", ""
	}

	// Gather receiver method set across the same package — same logic
	// as goSatisfiedInterfaces.
	resolver := namespace.NewGoResolver(db.Root())
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
		return "", ""
	}

	src, _ := os.ReadFile(sym.File)
	imports := goImportSet(src)

	for _, iface := range goStdlibInterfaces {
		// Target must be one of this interface's methods.
		isTarget := false
		for _, m := range iface.methods {
			if m == sym.Name {
				isTarget = true
				break
			}
		}
		if !isTarget {
			continue
		}
		// Receiver must implement every method in the interface's set.
		complete := true
		for _, m := range iface.methods {
			if !receiverMethods[m] {
				complete = false
				break
			}
		}
		if !complete {
			continue
		}
		// Import-presence guard for non-builtins. The receiver could
		// satisfy io.Reader by duck typing without importing "io",
		// but in that case no caller in this file uses it as an
		// io.Reader — so the conformance break is less likely to
		// matter. Builtins like `error` don't have an import.
		if iface.pkg != "" && !imports[iface.pkg] {
			continue
		}
		return iface.pkg, iface.name
	}
	return "", ""
}

// goImportSet returns the set of import paths declared in a Go source
// file. Textual scan only — handles `import "x"` (single) and
// `import (...)` (block) forms. Quoted strings inside comments or
// identifiers don't appear in import slots, so a slot-aware scan is
// good enough; we don't try to parse Go syntax here.
func goImportSet(src []byte) map[string]bool {
	out := map[string]bool{}
	if len(src) == 0 {
		return out
	}
	rest := src
	for {
		idx := bytes.Index(rest, []byte("import"))
		if idx < 0 {
			break
		}
		// Ensure word boundary on the left.
		if idx > 0 {
			prev := rest[idx-1]
			if (prev >= 'a' && prev <= 'z') || (prev >= 'A' && prev <= 'Z') ||
				(prev >= '0' && prev <= '9') || prev == '_' {
				rest = rest[idx+1:]
				continue
			}
		}
		after := rest[idx+len("import"):]
		// Skip leading whitespace.
		j := 0
		for j < len(after) && (after[j] == ' ' || after[j] == '\t' || after[j] == '\n') {
			j++
		}
		if j >= len(after) {
			break
		}
		if after[j] == '(' {
			// Block form: collect quoted strings until matching ')'.
			end := bytes.IndexByte(after[j:], ')')
			if end < 0 {
				break
			}
			block := after[j : j+end]
			collectQuoted(block, out)
			rest = after[j+end:]
			continue
		}
		// Single line form: read until newline.
		nl := bytes.IndexByte(after[j:], '\n')
		if nl < 0 {
			collectQuoted(after[j:], out)
			break
		}
		collectQuoted(after[j:j+nl], out)
		rest = after[j+nl:]
	}
	return out
}

func collectQuoted(b []byte, out map[string]bool) {
	for i := 0; i < len(b); i++ {
		if b[i] != '"' {
			continue
		}
		end := bytes.IndexByte(b[i+1:], '"')
		if end < 0 {
			return
		}
		out[string(b[i+1:i+1+end])] = true
		i += end + 1
	}
}
