package builtins

// Rust prelude + primitive types + common stdlib names. Conservative v1
// list; extend as dogfood surfaces real gaps.
var Rust = from(
	// Primitive types
	"bool", "char", "str",
	"i8", "i16", "i32", "i64", "i128", "isize",
	"u8", "u16", "u32", "u64", "u128", "usize",
	"f32", "f64",

	// Core prelude types and enum variants
	"String", "Vec", "Box", "Option", "Result",
	"Some", "None", "Ok", "Err",

	// Common traits
	"Copy", "Clone", "Debug", "Default", "Drop",
	"Eq", "Hash", "Ord", "PartialEq", "PartialOrd",
	"Send", "Sized", "Sync", "ToString", "Into",
	"From", "TryInto", "TryFrom", "Iterator",
	"IntoIterator", "Fn", "FnMut", "FnOnce",
	"AsRef", "AsMut", "Deref", "DerefMut",

	// Common macros (no !)
	"println", "print", "eprintln", "eprint",
	"format", "write", "writeln", "vec",
	"assert", "assert_eq", "assert_ne", "panic",
	"todo", "unreachable", "dbg", "matches",

	// Common std collections
	"HashMap", "HashSet", "BTreeMap", "BTreeSet",
)
