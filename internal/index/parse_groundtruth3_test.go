package index

import "testing"

// TestGroundTruth3_Lua tests ParseLua against a verbatim copy of
// neovim/runtime/lua/coxpcall.lua (116 lines).
//
// Symbol inventory (every function declaration, top-level only):
//   line 23  local function isCoroutineSafe   → function
//   line 51  local function handleReturnValue → function
//   line 62  function performResume           → function
//   line 67  local function id               → function
//   line 71  function _G.coxpcall            → function  (name = "coxpcall")
//   line 95  local function corunning        → function
//
// Non-symbols (verified):
//   line 43  local performResume  — forward decl, not "local function", no symbol
//   line 44  local oldpcall, ...  — local vars, no symbol
//   line 45  local pack = ...     — local var (= function(...) ...), not tracked
//   line 47  local running = ...  — local var, no symbol
//   line 49  local coromap = ...  — local var, no symbol
//   line 112 function _G.copcall — NOT recorded: the parser double-counts `do` in
//            `while ... do` (both `while` and `do` push blockDepth), so corunning's
//            closing `end` is absorbed and copcall's `end` closes corunning's scope
//            instead, leaving copcall mis-classified as nested (inFunction=true).
//
// Imports: none (no require() calls in this file).
func TestGroundTruth3_Lua(t *testing.T) {
	src := []byte(`-------------------------------------------------------------------------------
-- (Not needed for LuaJIT or Lua 5.2+)
--
-- Coroutine safe xpcall and pcall versions
--
-- https://keplerproject.github.io/coxpcall/
--
-- Encapsulates the protected calls with a coroutine based loop, so errors can
-- be dealed without the usual Lua 5.x pcall/xpcall issues with coroutines
-- yielding inside the call to pcall or xpcall.
--
-- Authors: Roberto Ierusalimschy and Andre Carregal
-- Contributors: Thomas Harning Jr., Ignacio Burgueño, Fabio Mascarenhas
--
-- Copyright 2005 - Kepler Project
--
-- $Id: coxpcall.lua,v 1.13 2008/05/19 19:20:02 mascarenhas Exp $
-------------------------------------------------------------------------------

-------------------------------------------------------------------------------
-- Checks if (x)pcall function is coroutine safe
-------------------------------------------------------------------------------
local function isCoroutineSafe(func)
    local co = coroutine.create(function()
        return func(coroutine.yield, function() end)
    end)

    coroutine.resume(co)
    return coroutine.resume(co)
end

-- No need to do anything if pcall and xpcall are already safe.
if isCoroutineSafe(pcall) and isCoroutineSafe(xpcall) then
    _G.copcall = pcall
    _G.coxpcall = xpcall
    return { pcall = pcall, xpcall = xpcall, running = coroutine.running }
end

-------------------------------------------------------------------------------
-- Implements xpcall with coroutines
-------------------------------------------------------------------------------
---@diagnostic disable-next-line
local performResume
local oldpcall, oldxpcall = pcall, xpcall
local pack = table.pack or function(...) return {n = select("#", ...), ...} end
local unpack = table.unpack or unpack
local running = coroutine.running
--- @type table<thread,thread>
local coromap = setmetatable({}, { __mode = "k" })

local function handleReturnValue(err, co, status, ...)
    if not status then
        return false, err(debug.traceback(co, (...)), ...)
    end
    if coroutine.status(co) == 'suspended' then
        return performResume(err, co, coroutine.yield(...))
    else
        return true, ...
    end
end

function performResume(err, co, ...)
    return handleReturnValue(err, co, coroutine.resume(co, ...))
end

--- @diagnostic disable-next-line: unused-vararg
local function id(trace, ...)
    return trace
end

function _G.coxpcall(f, err, ...)
    local current = running()
    if not current then
        if err == id then
            return oldpcall(f, ...)
        else
            if select("#", ...) > 0 then
                local oldf, params = f, pack(...)
                f = function() return oldf(unpack(params, 1, params.n)) end
            end
            return oldxpcall(f, err)
        end
    else
        local res, co = oldpcall(coroutine.create, f)
        if not res then
            local newf = function(...) return f(...) end
            co = coroutine.create(newf)
        end
        coromap[co] = current
        return performResume(err, co, ...)
    end
end

--- @param coro? thread
local function corunning(coro)
  if coro ~= nil then
    assert(type(coro)=="thread", "Bad argument; expected thread, got: "..type(coro))
  else
    coro = running()
  end
  while coromap[coro] do
    coro = coromap[coro]
  end
  if coro == "mainthread" then return nil end
  return coro
end

-------------------------------------------------------------------------------
-- Implements pcall with coroutines
-------------------------------------------------------------------------------

function _G.copcall(f, ...)
    return coxpcall(f, id, ...)
end

return { pcall = copcall, xpcall = coxpcall, running = corunning }
`)

	r := ParseLua(src)

	wantSyms := []struct{ typ, name string }{
		{"function", "isCoroutineSafe"},
		{"function", "handleReturnValue"},
		{"function", "performResume"},
		{"function", "id"},
		{"function", "coxpcall"},
		{"function", "corunning"},
		{"function", "copcall"},
	}

	if len(r.Symbols) != len(wantSyms) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s", i, s.Type, s.Name)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(wantSyms))
	}
	for i, w := range wantSyms {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}

	if len(r.Imports) != 0 {
		t.Errorf("got %d imports, want 0: %v", len(r.Imports), r.Imports)
	}
}

// TestGroundTruth3_Zig tests ParseZig against a verbatim copy of
// zig/lib/std/buf_set.zig (146 lines).
//
// Symbol inventory (top-level only):
//   line 1   const std = @import("std.zig")       → constant "std"
//   line 2   const StringHashMap = std.StringHashMap → constant "StringHashMap"
//   line 3   const mem = @import("mem.zig")         → constant "mem"
//   line 4   const Allocator = mem.Allocator         → constant "Allocator"
//   line 5   const testing = std.testing             → constant "testing"
//   line 10  pub const BufSet = struct { ... }       → struct "BufSet"
//   line 135 test "clone with arena" { ... }         → function "clone with arena"
//
// Non-symbols (verified):
//   Inside BufSet struct (lines 10-119): BufSetHashMap const, Iterator const,
//   init/deinit/insert/contains/remove/count/iterator/allocator/cloneWithAllocator/clone fn,
//   test clone, free fn, copy fn — all suppressed by inContainer().
//   line 121 test BufSet { ... }         — unnamed (identifier, not string), skipped
//
// Imports:
//   "std.zig" (from const std = @import("std.zig"))
//   "mem.zig"  (from const mem = @import("mem.zig"))
func TestGroundTruth3_Zig(t *testing.T) {
	src := []byte(`const std = @import("std.zig");
const StringHashMap = std.StringHashMap;
const mem = @import("mem.zig");
const Allocator = mem.Allocator;
const testing = std.testing;

/// A BufSet is a set of strings.  The BufSet duplicates
/// strings internally, and never takes ownership of strings
/// which are passed to it.
pub const BufSet = struct {
    hash_map: BufSetHashMap,

    const BufSetHashMap = StringHashMap(void);
    pub const Iterator = BufSetHashMap.KeyIterator;

    /// Create a BufSet using an allocator.  The allocator will
    /// be used internally for both backing allocations and
    /// string duplication.
    pub fn init(a: Allocator) BufSet {
        return .{ .hash_map = BufSetHashMap.init(a) };
    }

    /// Free a BufSet along with all stored keys.
    pub fn deinit(self: *BufSet) void {
        var it = self.hash_map.keyIterator();
        while (it.next()) |key_ptr| {
            self.free(key_ptr.*);
        }
        self.hash_map.deinit();
        self.* = undefined;
    }

    /// Insert an item into the BufSet.  The item will be
    /// copied, so the caller may delete or reuse the
    /// passed string immediately.
    pub fn insert(self: *BufSet, value: []const u8) !void {
        const gop = try self.hash_map.getOrPut(value);
        if (!gop.found_existing) {
            gop.key_ptr.* = self.copy(value) catch |err| {
                _ = self.hash_map.remove(value);
                return err;
            };
        }
    }

    /// Check if the set contains an item matching the passed string
    pub fn contains(self: BufSet, value: []const u8) bool {
        return self.hash_map.contains(value);
    }

    /// Remove an item from the set.
    pub fn remove(self: *BufSet, value: []const u8) void {
        const kv = self.hash_map.fetchRemove(value) orelse return;
        self.free(kv.key);
    }

    /// Returns the number of items stored in the set
    pub fn count(self: *const BufSet) usize {
        return self.hash_map.count();
    }

    /// Returns an iterator over the items stored in the set.
    /// Iteration order is arbitrary.
    pub fn iterator(self: *const BufSet) Iterator {
        return self.hash_map.keyIterator();
    }

    /// Get the allocator used by this set
    pub fn allocator(self: *const BufSet) Allocator {
        return self.hash_map.allocator;
    }

    /// Creates a copy of this BufSet, using a specified allocator.
    pub fn cloneWithAllocator(
        self: *const BufSet,
        new_allocator: Allocator,
    ) Allocator.Error!BufSet {
        const cloned_hashmap = try self.hash_map.cloneWithAllocator(new_allocator);
        const cloned = BufSet{ .hash_map = cloned_hashmap };
        var it = cloned.hash_map.keyIterator();
        while (it.next()) |key_ptr| {
            key_ptr.* = try cloned.copy(key_ptr.*);
        }

        return cloned;
    }

    /// Creates a copy of this BufSet, using the same allocator.
    pub fn clone(self: *const BufSet) Allocator.Error!BufSet {
        return self.cloneWithAllocator(self.allocator());
    }

    test clone {
        var original = BufSet.init(testing.allocator);
        defer original.deinit();
        try original.insert("x");

        var cloned = try original.clone();
        defer cloned.deinit();
        cloned.remove("x");
        try testing.expect(original.count() == 1);
        try testing.expect(cloned.count() == 0);

        try testing.expectError(
            error.OutOfMemory,
            original.cloneWithAllocator(testing.failing_allocator),
        );
    }

    fn free(self: *const BufSet, value: []const u8) void {
        self.hash_map.allocator.free(value);
    }

    fn copy(self: *const BufSet, value: []const u8) ![]const u8 {
        const result = try self.hash_map.allocator.alloc(u8, value.len);
        @memcpy(result, value);
        return result;
    }
};

test BufSet {
    var bufset = BufSet.init(std.testing.allocator);
    defer bufset.deinit();

    try bufset.insert("x");
    try testing.expect(bufset.count() == 1);
    bufset.remove("x");
    try testing.expect(bufset.count() == 0);

    try bufset.insert("x");
    try bufset.insert("y");
    try bufset.insert("z");
}

test "clone with arena" {
    const allocator = std.testing.allocator;
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();

    var buf = BufSet.init(allocator);
    defer buf.deinit();
    try buf.insert("member1");
    try buf.insert("member2");

    _ = try buf.cloneWithAllocator(arena.allocator());
}
`)

	r := ParseZig(src)

	wantSyms := []struct{ typ, name string }{
		{"constant", "std"},
		{"constant", "StringHashMap"},
		{"constant", "mem"},
		{"constant", "Allocator"},
		{"constant", "testing"},
		{"struct", "BufSet"},
		{"function", "clone with arena"},
	}

	if len(r.Symbols) != len(wantSyms) {
		for i, s := range r.Symbols {
			t.Logf("[%d] %s %s", i, s.Type, s.Name)
		}
		t.Fatalf("got %d symbols, want %d", len(r.Symbols), len(wantSyms))
	}
	for i, w := range wantSyms {
		if r.Symbols[i].Type != w.typ || r.Symbols[i].Name != w.name {
			t.Errorf("[%d] got %s %q, want %s %q", i, r.Symbols[i].Type, r.Symbols[i].Name, w.typ, w.name)
		}
	}

	wantImports := []string{"std.zig", "mem.zig"}
	if len(r.Imports) != len(wantImports) {
		for i, imp := range r.Imports {
			t.Logf("[%d] import %q", i, imp.Path)
		}
		t.Fatalf("got %d imports, want %d", len(r.Imports), len(wantImports))
	}
	for i, want := range wantImports {
		if r.Imports[i].Path != want {
			t.Errorf("import[%d] got %q, want %q", i, r.Imports[i].Path, want)
		}
	}
}
