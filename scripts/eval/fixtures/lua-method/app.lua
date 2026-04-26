-- Module-pattern rename oracle. Renaming `compute` rewrites both the
-- def (`function M.compute`) and the property-access caller
-- (`M.compute(5)`). A miss raises "attempt to call a nil value" when
-- M.run() is invoked.

local M = {}

function M.compute(x)
  return x * 2
end

function M.run()
  return M.compute(5)
end

assert(M.run() == 10, "rename broke M.compute caller")
print("ok")
