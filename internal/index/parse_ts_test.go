package index

import "testing"

func TestParseTS_Fixture(t *testing.T) {
	src := []byte("// tricky.ts\n" +
		"import fs from 'fs';\n" +
		"import { readFile, writeFile as wf } from 'node:fs/promises';\n" +
		"import * as path from 'path';\n" +
		"import type { Config } from './config';\n" +
		"import './polyfill';\n" +
		"\n" +
		"export const VERSION = \"1.0.0\";\n" +
		"\n" +
		"export function parse<T>(input: string): T {\n" +
		"  return JSON.parse(input) as T;\n" +
		"}\n" +
		"\n" +
		"export async function* stream(url: string) {\n" +
		"  yield await fetch(url);\n" +
		"}\n" +
		"\n" +
		"export class Widget<T extends Base> extends Component implements IWidget {\n" +
		"  static readonly DEFAULT = 42;\n" +
		"  #count: number = 0;\n" +
		"\n" +
		"  constructor(public name: string, private items: T[]) {\n" +
		"    super();\n" +
		"  }\n" +
		"\n" +
		"  get size(): number {\n" +
		"    return this.items.length;\n" +
		"  }\n" +
		"\n" +
		"  set size(n: number) {\n" +
		"    this.items.length = n;\n" +
		"  }\n" +
		"\n" +
		"  async render(opts: { x: number, y: number }): Promise<string> {\n" +
		"    const header = `<<class Fake>>`;\n" +
		"    const body = `template ${this.items.map(i => `item:${i}`).join(\", \")}`;\n" +
		"    const re = /\\/class\\s+\\w+\\//g;\n" +
		"    const r2 = this.size / 2 / 1;\n" +
		"    if (r2 > 0) {\n" +
		"      return header + body;\n" +
		"    }\n" +
		"    return \"\";\n" +
		"  }\n" +
		"\n" +
		"  private validate(x: number): boolean {\n" +
		"    return x / 2 > 0;\n" +
		"  }\n" +
		"}\n" +
		"\n" +
		"export interface IWidget {\n" +
		"  render(opts: any): Promise<string>;\n" +
		"}\n" +
		"\n" +
		"export type WidgetOpts = {\n" +
		"  name: string;\n" +
		"  items: unknown[];\n" +
		"};\n" +
		"\n" +
		"export enum Status {\n" +
		"  Idle = \"idle\",\n" +
		"  Active = \"active\",\n" +
		"}\n" +
		"\n" +
		"export namespace Util {\n" +
		"  export function noop() {}\n" +
		"}\n" +
		"\n" +
		"const inner = () => {\n" +
		"  function nestedShouldNotLeak() {}\n" +
		"};\n" +
		"\n" +
		// export default class — the 'default' keyword is skipped and the
		// class is recorded under its declared name.
		"export default class DefaultWidget {\n" +
		"  greet(): string { return 'hi'; }\n" +
		"}\n" +
		"\n" +
		// abstract class + abstract method — 'abstract' is treated as a
		// modifier and stripped; both the class and its abstract method
		// are recorded normally.
		"abstract class AbstractBase {\n" +
		"  abstract doWork(): void;\n" +
		"}\n" +
		"\n" +
		// const enum — the 'const' modifier before 'enum' is stripped and
		// the enum is recorded under its name like a regular enum.
		"const enum Direction { Up, Down }\n")

	r := ParseTS(src)
	for i, s := range r.Symbols {
		t.Logf("[%d] %-9s %-20s L%d-%d parent=%d", i, s.Type, s.Name, s.StartLine, s.EndLine, s.Parent)
	}
	for i, imp := range r.Imports {
		t.Logf("imp[%d] %s (L%d)", i, imp.Path, imp.Line)
	}

	want := []struct{ typ, name string }{
		{"const", "VERSION"},
		{"function", "parse"},
		{"function", "stream"},
		{"class", "Widget"},
		{"method", "constructor"},
		{"method", "size"},
		{"method", "size"},
		{"method", "render"},
		{"method", "validate"},
		{"interface", "IWidget"},
		{"type", "WidgetOpts"},
		{"enum", "Status"},
		{"namespace", "Util"},
		{"function", "noop"},
		{"const", "inner"},
		// export default class: recorded with declared name, 'default' stripped.
		{"class", "DefaultWidget"},
		{"method", "greet"},
		// abstract class: 'abstract' treated as modifier, class recorded normally.
		{"class", "AbstractBase"},
		// abstract method: recorded as a regular method.
		{"method", "doWork"},
		// const enum: 'const' modifier stripped, recorded as enum.
		{"enum", "Direction"},
	}
	if len(r.Symbols) != len(want) {
		t.Errorf("got %d symbols, want %d", len(r.Symbols), len(want))
		for i, s := range r.Symbols {
			t.Logf("  [%d] %s %q", i, s.Type, s.Name)
		}
	}
	for i, w := range want {
		if i >= len(r.Symbols) {
			break
		}
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("symbol %d: got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}
	for _, s := range r.Symbols {
		if s.Name == "nestedShouldNotLeak" || s.Name == "Fake" || s.Name == "DEFAULT" {
			t.Errorf("spurious symbol: %+v", s)
		}
	}

	wantPaths := []string{"fs", "node:fs/promises", "path", "./config", "./polyfill"}
	if len(r.Imports) != len(wantPaths) {
		t.Errorf("got %d imports, want %d", len(r.Imports), len(wantPaths))
	}
	for i, wp := range wantPaths {
		if i >= len(r.Imports) {
			break
		}
		if r.Imports[i].Path != wp {
			t.Errorf("import %d: got %q want %q", i, r.Imports[i].Path, wp)
		}
	}
}