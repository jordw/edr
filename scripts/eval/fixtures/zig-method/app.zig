// Same-file struct-method rename oracle. The def lives inside
// `const Counter = struct { ... }`; rename must rewrite the def
// plus both `c.bump(...)` callers. Compile error on miss.

const Counter = struct {
    value: i32,

    pub fn bump(self: *Counter, by: i32) void {
        self.value += by;
    }
};

pub fn main() !void {
    var c = Counter{ .value = 0 };
    c.bump(5);
    c.bump(3);
    if (c.value != 8) return error.WrongResult;
}
