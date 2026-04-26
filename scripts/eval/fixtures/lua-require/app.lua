-- Cross-file rename oracle. `dofile("./lib.lua")` returns the
-- module table; `lib.compute(...)` exercises the property-access
-- cross-file rewrite. A miss surfaces as a nil-call error at run.

local lib = dofile("./lib.lua")
assert(lib.compute(5) == 10, "rename missed cross-file caller")
print("ok")
