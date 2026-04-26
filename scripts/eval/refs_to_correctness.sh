#!/usr/bin/env bash
# refs_to_correctness.sh — caller-count oracle for refs-to.
#
# For each tuple, runs `edr refs-to file:Symbol` and asserts the
# returned count matches the expected count. Catches the bug class
# where refs-to silently undercounts callers (e.g., the
# `decl.Scope == 1` gate that recently hid class-method callers
# across Ruby/Java/Python).
#
# Counts are computed once and committed as expected values. If the
# implementation drifts above/below the expectation, the tuple FAILs
# and the diff vs expected is shown so the user can update the
# tuple intentionally or fix the regression.
#
# Tuple format: "label|repo|target|expected"
#   expected: =N (exact)  or  >=N (at least)
#
# Worktrees are not used — refs-to is read-only and operates against
# the source repos directly.

set -o pipefail

: "${REPO_BASE:=$(cd "$(dirname "$0")/../../.." && pwd)}"
: "${EDR_BIN:=edr}"

# Tuples lifted from the rename oracle plus a few refs-to-specific
# cases (e.g., the decl.Scope class-method gate). Counts captured
# from a known-good run; update intentionally when the rename layer
# changes.
TUPLES=(
  # Ruby
  "rb:compute-free|edr|scripts/eval/fixtures/ruby-demo/lib.rb:compute|=2"
  "rb:class-method|edr|scripts/eval/fixtures/ruby-method/counter.rb:value|>=1"
  "rb:module-method|edr|scripts/eval/fixtures/ruby-module/mathx.rb:compute|=2"
  "rb:hierarchy|edr|scripts/eval/fixtures/ruby-hierarchy/greeter.rb:greet|=2"

  # Python
  "py:compute-free|edr|scripts/eval/fixtures/python-demo/pkg/lib.py:compute|>=1"
  "py:class-method|edr|scripts/eval/fixtures/python-method/pkg/counter.py:value|>=1"
  "py:hierarchy|edr|scripts/eval/fixtures/python-hierarchy/pkg/greeter.py:greet|=2"

  # Java
  "java:lib-static-method|edr|scripts/eval/fixtures/java-demo/src/com/example/lib/Lib.java:compute|=2"
  "java:iface-impl|edr|scripts/eval/fixtures/java-demo/src/com/example/iface/ServiceImpl.java:run|=1"

  # Kotlin
  "kt:companion|edr|scripts/eval/fixtures/kotlin-companion/Lib.kt:compute|>=1"

  # Swift
  "swift:struct-method|edr|scripts/eval/fixtures/swift-method/Counter.swift:value|=1"

  # TypeScript
  "ts:class-method|edr|scripts/eval/fixtures/ts-method/src/counter.ts:value|>=1"

  # C# / PHP — class-method
  "cs:class-method|edr|scripts/eval/fixtures/csharp-method/Counter.cs:Value|=1"
  "php:class-method|edr|scripts/eval/fixtures/php-method/Counter.php:value|=1"

  # Lua: method-syntax (`:`) callers — covers the property-access
  # path the rename layer relies on for module-pattern code.
  "lua:method-self|edr|scripts/eval/fixtures/lua-method-self/app.lua:bump|=2"

  # Zig: enum decl + type-annotation refs.
  "zig:enum-decl|edr|scripts/eval/fixtures/zig-enum/app.zig:Status|=2"
)

pass=0; fail=0; total=0
printf "%-25s %-8s %8s  %s\n" "label" "result" "count" "notes"
printf "%s\n" "-----------------------------------------------------------------"

for tuple in "${TUPLES[@]}"; do
  total=$((total + 1))
  label=${tuple%%|*}; rest=${tuple#*|}
  repo=${rest%%|*};   rest=${rest#*|}
  target=${rest%%|*}; expected=${rest#*|}
  src_path="$REPO_BASE/$repo"
  if [ ! -d "$src_path" ]; then
    printf "%-25s %-8s %8s  %s\n" "$label" "SKIP" "-" "missing $src_path"
    continue
  fi

  json=$(cd "$src_path" && "$EDR_BIN" refs-to "$target" 2>&1 | head -1)
  if printf "%s" "$json" | grep -q error:; then
    printf "%-25s %-8s %8s  %s\n" "$label" "ERR" "-" "$(printf "%s" "$json" | cut -c1-60)"
    fail=$((fail + 1)); continue
  fi
  count=$(printf "%s" "$json" | sed -n 's/.*"count":\([0-9]*\).*/\1/p')
  [ -z "$count" ] && count=0

  # Parse expected: =N or >=N
  op=${expected%%[0-9]*}
  num=${expected#$op}
  ok=0
  case "$op" in
    "=")  [ "$count" = "$num" ] && ok=1 ;;
    ">=") [ "$count" -ge "$num" ] && ok=1 ;;
    *)
      printf "%-25s %-8s %8s  %s\n" "$label" "ERR" "-" "bad expected: $expected"
      fail=$((fail + 1)); continue ;;
  esac

  if [ "$ok" = 1 ]; then
    printf "%-25s %-8s %8s  %s\n" "$label" "PASS" "$count" ""
    pass=$((pass + 1))
  else
    printf "%-25s %-8s %8s  %s\n" "$label" "FAIL" "$count" "expected $expected"
    fail=$((fail + 1))
  fi
done

echo
printf "Total: %d  PASS: %d  FAIL: %d\n" "$total" "$pass" "$fail"
[ "$fail" -eq 0 ]
