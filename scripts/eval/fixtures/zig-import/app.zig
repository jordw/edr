// Cross-file rename oracle. `@import("lib.zig")` + `lib.compute(...)`
// caller. A miss surfaces as a compile error.

const lib = @import("lib.zig");

pub fn main() !void {
    if (lib.compute(5) != 10) return error.WrongResult;
}
