package builtins

// Lua 5.x builtins: standard library names + reserved value names.
// Not exhaustive — add as dogfood surfaces gaps.
var Lua = from(
	// Reserved words that appear in value positions.
	"true", "false", "nil", "self",

	// Basic library
	"_G", "_ENV", "_VERSION",
	"assert", "collectgarbage", "dofile", "error", "getmetatable",
	"ipairs", "load", "loadfile", "loadstring", "next", "pairs",
	"pcall", "print", "rawequal", "rawget", "rawlen", "rawset",
	"require", "select", "setmetatable", "tonumber", "tostring",
	"type", "unpack", "xpcall",

	// Standard libraries (table values)
	"coroutine", "debug", "io", "math", "os", "package", "string",
	"table", "utf8", "bit32",
)
