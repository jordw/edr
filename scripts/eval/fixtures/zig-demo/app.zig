// Same-file rename oracle. Renaming `compute` must rewrite both the
// def and the call site below; otherwise `zig run` fails with a
// missing-symbol error and the eval flags it.

const std = @import("std");

fn compute(x: i32) i32 {
    return x * 2;
}

pub fn main() !void {
    const result = compute(5);
    if (result != 10) return error.WrongResult;
}
