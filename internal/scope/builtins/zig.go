package builtins

// Zig builtins: types + frequent stdlib namespace names. Builtin
// functions like @import / @sizeOf / @TypeOf are spelled with a leading
// `@` and aren't scanned as plain idents, so they're not in this set.
var Zig = from(
	// Primitive types
	"void", "bool", "noreturn", "type", "anyerror", "anyopaque",
	"comptime_int", "comptime_float", "anytype", "anyframe",
	"u8", "u16", "u32", "u64", "u128", "usize",
	"i8", "i16", "i32", "i64", "i128", "isize",
	"f16", "f32", "f64", "f80", "f128",
	"c_short", "c_ushort", "c_int", "c_uint", "c_long", "c_ulong",
	"c_longlong", "c_ulonglong", "c_longdouble",

	// Reserved value-position names
	"true", "false", "null", "undefined", "unreachable", "self",

	// Stdlib top-level namespaces frequently bound via @import("std")
	"std", "builtin", "root",
)
