package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Regression for crash where GetSymbol picked an overload-only declaration
// (EndLine=0, EndByte=0) over the real implementation. The largest-span
// tiebreaker computed the span via uint32 subtraction, which underflowed for
// the malformed symbol and made it always win.
//
// Reproducer (against vscode): edr edit src/vs/base/common/lifecycle.ts:dispose
// --move-after src/vs/base/common/async.ts:AsyncIterableSource crashed with
// "slice bounds out of range [10222:0]".
func TestGetSymbol_SkipsOverloadDeclarations(t *testing.T) {
	dir := t.TempDir()
	src := `
/**
 * Disposes of the value(s) passed in.
 */
export function dispose<T>(d: T): T;
export function dispose<T>(d: T | undefined): T | undefined;
export function dispose<T>(arg: T | undefined): any {
	if (arg) {
		(arg as any).dispose();
	}
	return arg;
}
`
	path := filepath.Join(dir, "lifecycle.ts")
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	db := NewOnDemand(dir)
	sym, err := db.GetSymbol(context.Background(), "lifecycle.ts", "dispose")
	if err != nil {
		t.Fatalf("GetSymbol: %v", err)
	}
	if !hasValidSpan(sym) {
		t.Fatalf("picked malformed symbol: lines=%d-%d bytes=%d-%d",
			sym.StartLine, sym.EndLine, sym.StartByte, sym.EndByte)
	}
	if sym.EndByte <= sym.StartByte {
		t.Fatalf("EndByte<=StartByte (would crash slice): %d <= %d", sym.EndByte, sym.StartByte)
	}
	// The implementation has a body; declarations don't. The picked symbol
	// must span more than one line.
	if sym.EndLine == sym.StartLine {
		t.Fatalf("picked a single-line decl, expected the multi-line impl: lines=%d-%d",
			sym.StartLine, sym.EndLine)
	}
}
