-- Same-file rename oracle. `local function compute` plus an internal
-- caller. If rename misses the caller, `run()` raises "attempt to call
-- a nil value" and the script exits non-zero — a definitive miss
-- signal for the eval.

local function compute(x)
  return x * 2
end

local function run()
  return compute(5)
end

assert(run() == 10, "rename broke caller binding")
print("ok")
