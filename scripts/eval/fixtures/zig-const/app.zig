// Pub-const rename oracle. Renaming `MAX_RETRIES` rewrites the def
// and both call sites; a miss surfaces as a compile error
// ("use of undeclared identifier") since Zig's compiler enforces
// strict identifier resolution.

const std = @import("std");

pub const MAX_RETRIES: u32 = 3;

pub fn main() !void {
    var attempts: u32 = 0;
    while (attempts < MAX_RETRIES) : (attempts += 1) {}
    if (attempts != MAX_RETRIES) return error.WrongCount;
}
