// Enum-decl rename oracle. Renaming `Status` must rewrite the type
// def and every `: Status` annotation in the file. A miss is a
// compile error.

const std = @import("std");

const Status = enum {
    ok,
    err,
};

fn isOk(s: Status) bool {
    return s == .ok;
}

pub fn main() !void {
    const s: Status = .ok;
    if (!isOk(s)) return error.WrongStatus;
}
