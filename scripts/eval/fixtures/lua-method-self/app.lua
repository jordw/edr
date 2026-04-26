-- Method-syntax rename oracle. Renaming `bump` must rewrite the
-- `function Counter:bump` def and both `Counter:bump(...)` callers.
-- Lua's `:` syntax desugars to a property access with implicit self;
-- a missed caller surfaces as a nil-value call.

local Counter = { value = 0 }

function Counter:bump(n)
  self.value = self.value + n
end

function Counter:snapshot()
  return self.value
end

Counter:bump(5)
Counter:bump(3)
assert(Counter:snapshot() == 8, "rename broke Counter:bump callers")
print("ok")
