-- Shadow-aware rename oracle. The outer `local function compute`
-- and its caller must be rewritten; the inner shadowed `local
-- function compute` and its in-scope use must remain intact. If the
-- inner pair gets renamed too, the inner assert (calling the
-- shadowed local) breaks.

local function compute(x)
  return x * 2
end

local function user()
  local result = compute(5)
  do
    local function compute() return 999 end
    assert(compute() == 999, "inner shadow was clobbered by outer rename")
  end
  return result
end

assert(user() == 10, "outer rename missed a caller")
print("ok")
