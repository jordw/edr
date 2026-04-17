package builtins

// TypeScript globals: JS built-ins + TS-specific primitive and utility types.
// Not exhaustive — add names as they appear in real dogfood.
var TypeScript = from(
	// JS primitives and keywords that appear as type/value names
	"undefined", "null", "NaN", "Infinity", "globalThis", "arguments",

	// TS type primitives (type-position; value-position would be keywords)
	"any", "unknown", "never", "void", "object",
	"string", "number", "boolean", "bigint", "symbol",

	// TS type operators that look like identifiers in some positions
	"keyof", "infer", "is",

	// JS core constructors (can appear as values or types)
	"Array", "ReadonlyArray", "Map", "ReadonlyMap", "WeakMap",
	"Set", "ReadonlySet", "WeakSet",
	"Object", "String", "Number", "Boolean", "BigInt", "Symbol",
	"Function", "Error", "TypeError", "RangeError", "SyntaxError",
	"ReferenceError", "EvalError", "URIError",
	"Date", "RegExp", "Math", "JSON", "Reflect", "Proxy",
	"Promise", "PromiseLike",
	"Iterator", "AsyncIterator", "Iterable", "AsyncIterable",
	"IterableIterator", "AsyncIterableIterator",
	"IteratorResult", "AsyncIteratorResult",
	"Generator", "AsyncGenerator", "GeneratorFunction", "AsyncGeneratorFunction",
	"ArrayBuffer", "SharedArrayBuffer", "DataView", "Atomics",
	"Uint8Array", "Uint8ClampedArray", "Uint16Array", "Uint32Array",
	"Int8Array", "Int16Array", "Int32Array", "BigInt64Array", "BigUint64Array",
	"Float32Array", "Float64Array",
	"ArrayLike",

	// TS utility types
	"Partial", "Required", "Readonly", "Record", "Pick", "Omit",
	"Exclude", "Extract", "NonNullable",
	"Parameters", "ReturnType", "ConstructorParameters", "InstanceType",
	"ThisType", "Awaited",
	"Uppercase", "Lowercase", "Capitalize", "Uncapitalize",

	// Common globals
	"console", "isNaN", "isFinite", "parseInt", "parseFloat",
	"encodeURI", "encodeURIComponent", "decodeURI", "decodeURIComponent",

	// Node / browser common names (permissive; better to match than miss)
	"process", "Buffer", "setTimeout", "setInterval",
	"clearTimeout", "clearInterval", "queueMicrotask",
	"window", "document", "navigator", "location", "history", "fetch",
	"URL", "URLSearchParams", "Headers", "Request", "Response",
	"FormData", "AbortController", "AbortSignal", "Event", "EventTarget",
)
