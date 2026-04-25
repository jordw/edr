#!/usr/bin/env bash
# rename_correctness.sh — compiler-oracle correctness eval for rename.
#
# For each tuple, apply edr rename (NOT dry-run) inside a disposable
# git worktree, run the build, capture PASS/FAIL. A compile failure
# is a definitive correctness signal (something about the rename
# broke references). A compile pass is a strong but not conclusive
# positive.
#
# Worktrees share `.git` with the source repo but have their own
# working tree under $WT_DIR — the main checkout is never touched,
# so uncommitted work is safe.

set -o pipefail

: "${REPO_BASE:=$(cd "$(dirname "$0")/../../.." && pwd)}"
: "${EDR_BIN:=edr}"
: "${EDR_EVAL_FORCE_MODE:=scope}"
export EDR_EVAL_FORCE_MODE

# Tuples: "label|repo|target|new_name|build_cmd"
TUPLES=(
  # Go: edr as self-host.
  "go:edr-dispatch|edr|internal/dispatch/dispatch.go:Dispatch|Dispatch2|go build ./..."
  "go:edr-smallfn|edr|internal/dispatch/dispatch_rename.go:expandToDocComment|expandToDocComment2|go build ./..."
  "go:edr-scopeHelper|edr|internal/dispatch/dispatch_rename_scope.go:scopeSupported|scopeSupported2|go build ./..."
  "go:edr-outputRel|edr|internal/output/output.go:Rel|Rel2|go build ./..."
  "go:edr-editTx|edr|internal/edit/transaction.go:NewTransaction|NewTransaction2|go build ./..."
  "go:edr-idxQuery|edr|internal/idx/query.go:Query|Query2|go build ./..."
  "go:edr-ondemandGetSym|edr|internal/index/ondemand.go:GetSymbol|GetSymbol2|go build ./..."
  # Java: hand-built fixture under scripts/eval/fixtures/java-demo.
  "java:lib-static-method|edr|scripts/eval/fixtures/java-demo/src/com/example/lib/Lib.java:compute|compute2|cd scripts/eval/fixtures/java-demo && javac -d /tmp/javabuild_lib_static \$(find src -name '*.java')"
  "java:lib-instance-method|edr|scripts/eval/fixtures/java-demo/src/com/example/lib/Lib.java:process|process2|cd scripts/eval/fixtures/java-demo && javac -d /tmp/javabuild_lib_inst \$(find src -name '*.java')"
  "java:iface-impl|edr|scripts/eval/fixtures/java-demo/src/com/example/iface/ServiceImpl.java:run|runImpl|cd scripts/eval/fixtures/java-demo && javac -d /tmp/javabuild_iface \$(find src -name '*.java')"
  # Rust: tokio with cargo check.
  "rust:tokio-unique|tokio|tokio/src/runtime/blocking/pool.rs:BlockingPool|BlockingPool2|cargo check --quiet --package tokio"
  "rust:tokio-common|tokio|tokio/src/task/spawn.rs:spawn|spawn_renamed|cargo check --quiet --package tokio"
  "rust:tokio-mid|tokio|tokio/src/runtime/handle.rs:Handle|Handle2|cargo check --quiet --package tokio"
  # C: hand-built fixture. Renames a function with prototype in .h,
  # def in .c, and caller in main.c — exercises the sibling-canonical
  # merge plus #include-graph caller detection.
  "c:compute-free|edr|scripts/eval/fixtures/c-demo/src/compute.c:compute|calculate|cd scripts/eval/fixtures/c-demo && gcc -Wall -Werror -o /tmp/cbuild src/*.c"
  # C++: same shape as C, .cpp/.hpp pair + caller.
  "cpp:compute-free|edr|scripts/eval/fixtures/cpp-demo/src/compute.cpp:compute|calculate|cd scripts/eval/fixtures/cpp-demo && g++ -std=c++17 -Wall -Werror -o /tmp/cppbuild src/*.cpp"
  # Python: `from pkg.lib import compute` — runs the package so any
  # broken ref surfaces as an ImportError / NameError at runtime.
  "py:compute-free|edr|scripts/eval/fixtures/python-demo/pkg/lib.py:compute|calculate|cd scripts/eval/fixtures/python-demo && python3 -m pkg"
  # Ruby: require_relative + call. Running exercises name resolution.
  "rb:compute-free|edr|scripts/eval/fixtures/ruby-demo/lib.rb:compute|calculate|cd scripts/eval/fixtures/ruby-demo && ruby app.rb"
  # Swift: free function callable across files in the same compile
  # unit. swiftc typechecks calls across files when compiled together.
  "swift:compute-free|edr|scripts/eval/fixtures/swift-demo/Lib.swift:compute|calculate|cd scripts/eval/fixtures/swift-demo && swiftc Lib.swift App.swift main.swift -o /tmp/swiftbuild"
  # TS: ES-module import + call across two files; strict tsc.
  "ts:compute-free|edr|scripts/eval/fixtures/ts-demo/src/lib.ts:compute|calculate|cd scripts/eval/fixtures/ts-demo && tsc"
  # Kotlin: top-level function in a package, called from another file.
  "kt:compute-free|edr|scripts/eval/fixtures/kotlin-demo/Lib.kt:compute|calculate|cd scripts/eval/fixtures/kotlin-demo && kotlinc *.kt -include-runtime -d /tmp/kt_demo.jar"
  # PHP: require_once + cross-file call; `php` runs the script so any
  # missing symbol raises a fatal error.
  "php:compute-free|edr|scripts/eval/fixtures/php-demo/lib.php:compute|calculate|cd scripts/eval/fixtures/php-demo && php app.php"
  # C#: static class method across two files in the same namespace.
  # `dotnet build` type-checks the whole project.
  "cs:compute-free|edr|scripts/eval/fixtures/csharp-demo/Lib.cs:Compute|Calculate|cd scripts/eval/fixtures/csharp-demo && dotnet build --nologo"
  # TS: class method called via obj.method().
  "ts:class-method|edr|scripts/eval/fixtures/ts-method/src/counter.ts:value|magnitude|cd scripts/eval/fixtures/ts-method && tsc"
  # TS: imported function name clashes with a param in another fn.
  "ts:shadow-param|edr|scripts/eval/fixtures/ts-shadow/src/lib.ts:compute|calculate|cd scripts/eval/fixtures/ts-shadow && tsc"
  # TS: same function name exported from two unrelated modules.
  "ts:ambiguous-name|edr|scripts/eval/fixtures/ts-ambig/src/lib/a.ts:compute|calculate|cd scripts/eval/fixtures/ts-ambig && tsc"
  # TS: tsconfig `paths` aliases (@/foo, @components/foo).
  "ts:tsconfig-paths|edr|scripts/eval/fixtures/ts-paths/src/components/counter.ts:compute|calculate|cd scripts/eval/fixtures/ts-paths && tsc"
  # TS: barrel re-exports — `export { X } from './bar'` chains.
  "ts:barrel-reexport|edr|scripts/eval/fixtures/ts-barrel/src/lib.ts:compute|calculate|cd scripts/eval/fixtures/ts-barrel && tsc"
  # JS: CommonJS `const { X } = require('./lib')` destructure +
  # `module.exports = { X }`. Running with node catches broken refs.
  "ts:cjs-require|edr|scripts/eval/fixtures/ts-cjs/lib.js:compute|calculate|cd scripts/eval/fixtures/ts-cjs && node app.js"
  # TS: `export default function X` — renaming the function
  # updates its definition; import sites use their own local name.
  "ts:default-export|edr|scripts/eval/fixtures/ts-default/src/lib.ts:compute|calculate|cd scripts/eval/fixtures/ts-default && tsc"
  # TS: rename a method on an interface; implementer + subclass
  # overrides must rewrite in lockstep.
  "ts:hierarchy|edr|scripts/eval/fixtures/ts-hierarchy/src/greeter.ts:greet|salute|cd scripts/eval/fixtures/ts-hierarchy && tsc"
  # Kotlin: instance method on a class called via obj.value().
  "kt:class-method|edr|scripts/eval/fixtures/kotlin-method/Counter.kt:value|magnitude|cd scripts/eval/fixtures/kotlin-method && kotlinc *.kt -include-runtime -d /tmp/kt_method.jar"
  # Kotlin: companion-object method called as Lib.compute.
  "kt:companion|edr|scripts/eval/fixtures/kotlin-companion/Lib.kt:compute|calculate|cd scripts/eval/fixtures/kotlin-companion && kotlinc *.kt -include-runtime -d /tmp/kt_comp.jar"
  # Kotlin: top-level compute + local `compute` shadow.
  "kt:shadow-local|edr|scripts/eval/fixtures/kotlin-shadow/Lib.kt:compute|calculate|cd scripts/eval/fixtures/kotlin-shadow && kotlinc *.kt -include-runtime -d /tmp/kt_shadow.jar"
  # PHP: instance method on a class called via $obj->method().
  "php:class-method|edr|scripts/eval/fixtures/php-method/Counter.php:value|magnitude|cd scripts/eval/fixtures/php-method && php app.php"
  # PHP: static method called via Class::method().
  "php:static-method|edr|scripts/eval/fixtures/php-static/Lib.php:compute|calculate|cd scripts/eval/fixtures/php-static && php app.php"
  # PHP: two unrelated classes with same static-method name.
  "php:ambiguous-name|edr|scripts/eval/fixtures/php-ambig/A.php:compute|calculate|cd scripts/eval/fixtures/php-ambig && php app.php"
  # C#: instance method on a class called via obj.Method().
  "cs:class-method|edr|scripts/eval/fixtures/csharp-method/Counter.cs:Value|Magnitude|cd scripts/eval/fixtures/csharp-method && dotnet build --nologo"
  # C#: static call site + a local variable with the same name.
  "cs:shadow-local|edr|scripts/eval/fixtures/csharp-shadow/Lib.cs:Compute|Calculate|cd scripts/eval/fixtures/csharp-shadow && dotnet build --nologo"
  # C#: two unrelated static classes with same method name.
  "cs:ambiguous-name|edr|scripts/eval/fixtures/csharp-ambig/A.cs:Compute|Calculate|cd scripts/eval/fixtures/csharp-ambig && dotnet build --nologo"
  # C#: interface + class + virtual override hierarchy.
  "cs:hierarchy|edr|scripts/eval/fixtures/csharp-hierarchy/Greeter.cs:Greet|Salute|cd scripts/eval/fixtures/csharp-hierarchy && dotnet build --nologo"
  # C: same-name static in a.c and b.c — rename one, verify the other
  # stays intact (linker would complain about duplicate externs
  # otherwise; statics are TU-local so independent).
  "c:static-isolation|edr|scripts/eval/fixtures/c-static/a.c:helper|worker|cd scripts/eval/fixtures/c-static && gcc -Wall -Werror -o /tmp/c_static a.c b.c"
  # C: global function named compute, unrelated local var also named
  # compute in another file. Rename must rewrite the global + its
  # prototype but leave the local.
  "c:shadow-local|edr|scripts/eval/fixtures/c-shadow/compute.c:compute|calculate|cd scripts/eval/fixtures/c-shadow && gcc -Wall -Werror -o /tmp/c_shadow compute.c use.c"
  # C++: method on a class called via obj.method(). Exercises the
  # (currently missing) receiver disambiguation — expected to FAIL
  # until C++ gains method-call rewriting.
  "cpp:class-method|edr|scripts/eval/fixtures/cpp-method/src/Counter.cpp:value|magnitude|cd scripts/eval/fixtures/cpp-method && g++ -std=c++17 -Wall -Werror -o /tmp/cpp_method src/*.cpp"
  # C++: same-name static in a.cpp and b.cpp — rename one, verify
  # the other stays intact.
  "cpp:static-isolation|edr|scripts/eval/fixtures/cpp-static/a.cpp:helper|worker|cd scripts/eval/fixtures/cpp-static && g++ -std=c++17 -Wall -Werror -o /tmp/cpp_static a.cpp b.cpp"
  # Python: method on class called via obj.method(). Exercises the
  # (currently missing) receiver disambiguation — expected to FAIL.
  "py:class-method|edr|scripts/eval/fixtures/python-method/pkg/counter.py:value|magnitude|cd scripts/eval/fixtures/python-method && python3 -m pkg"
  # Python: common name defined in two unrelated packages. Rename
  # pkg.lib.compute without touching other.util.compute.
  "py:ambiguous-name|edr|scripts/eval/fixtures/python-ambig/pkg/lib.py:compute|calculate|cd scripts/eval/fixtures/python-ambig && python3 -m pkg"
  # Python: relative sibling import — `from .lib import compute`.
  "py:sibling-import|edr|scripts/eval/fixtures/python-sibling/pkg/lib.py:compute|calculate|cd scripts/eval/fixtures/python-sibling && python3 -m pkg"
  # Python: ABC + class hierarchy + subclass override.
  # Rename on Hi.greet propagates to IGreeter, Loud, plus call sites.
  "py:hierarchy|edr|scripts/eval/fixtures/python-hierarchy/pkg/greeter.py:greet|salute|cd scripts/eval/fixtures/python-hierarchy && python3 -m pkg"
  # Ruby: instance method on a class called via obj.method. Exercises
  # receiver disambiguation — expected to FAIL.
  "rb:class-method|edr|scripts/eval/fixtures/ruby-method/counter.rb:value|magnitude|cd scripts/eval/fixtures/ruby-method && ruby app.rb"
  # Ruby: module method invoked as Module.method.
  "rb:module-method|edr|scripts/eval/fixtures/ruby-module/mathx.rb:compute|calculate|cd scripts/eval/fixtures/ruby-module && ruby app.rb"
  # Swift: struct method. Like class-method, tests receiver handling.
  "swift:struct-method|edr|scripts/eval/fixtures/swift-method/Counter.swift:value|magnitude|cd scripts/eval/fixtures/swift-method && swiftc Counter.swift App.swift main.swift -o /tmp/swift_method"
  # Swift: protocol extension default method. Tests hierarchy-like
  # propagation of method renames.
  "swift:protocol-default|edr|scripts/eval/fixtures/swift-protocol/Greeter.swift:greet|salute|cd scripts/eval/fixtures/swift-protocol && swiftc Greeter.swift App.swift main.swift -o /tmp/swift_protocol"
  # Swift: protocol + class conformance + subclass override.
  # Rename on Hi.greet should propagate to IGreeter.greet and
  # Loud.greet, and to call sites through either typed receiver.
  "swift:hierarchy|edr|scripts/eval/fixtures/swift-hierarchy/Greeter.swift:greet|salute|cd scripts/eval/fixtures/swift-hierarchy && swiftc Greeter.swift App.swift main.swift -o /tmp/swift_hier"
)

check_tool() { command -v "$1" >/dev/null 2>&1; }

# Worktree root. Per-repo disposable checkouts live under here so the
# main trees never get modified. Cleaned up on exit.
WT_DIR="${TMPDIR:-/tmp}/edr-oracle-wt-$$"
mkdir -p "$WT_DIR"

cleanup() {
  [ -z "$WT_DIR" ] && return
  for wt in "$WT_DIR"/*; do
    [ -d "$wt" ] || continue
    repo=$(basename "$wt")
    src="$REPO_BASE/$repo"
    if [ -d "$src/.git" ] || [ -f "$src/.git" ]; then
      git -C "$src" worktree remove --force "$wt" >/dev/null 2>&1 || rm -rf "$wt"
    else
      rm -rf "$wt"
    fi
  done
  rmdir "$WT_DIR" 2>/dev/null || true
}
trap cleanup EXIT INT TERM

# Collect unique repos referenced by tuples, then create a worktree
# at HEAD for each. Missing repos are silently skipped — the
# per-tuple loop below reports them.
repos_seen=""
for tuple in "${TUPLES[@]}"; do
  r=$(printf '%s' "$tuple" | cut -d'|' -f2)
  case " $repos_seen " in
    *" $r "*) continue ;;
  esac
  repos_seen="$repos_seen $r"
  src_path="$REPO_BASE/$r"
  [ ! -d "$src_path" ] && continue
  wt_path="$WT_DIR/$r"
  if ! git -C "$src_path" worktree add --detach "$wt_path" HEAD >/dev/null 2>&1; then
    echo "ERROR: failed to create worktree for $r at $wt_path" >&2
    exit 1
  fi
done

pass=0; fail=0; skip=0; total=0
printf "%-22s %-6s %-8s %6s  %s\n" "label" "mode" "result" "files" "notes"
printf "%s\n" "----------------------------------------------------------------------"

for tuple in "${TUPLES[@]}"; do
  total=$((total + 1))
  label=${tuple%%|*}; rest=${tuple#*|}
  repo=${rest%%|*};   rest=${rest#*|}
  target=${rest%%|*}; rest=${rest#*|}
  new_name=${rest%%|*}; build_cmd=${rest#*|}
  src_path="$REPO_BASE/$repo"
  wt_path="$WT_DIR/$repo"
  if [ ! -d "$src_path" ]; then
    printf "%-22s %-6s %-8s %6s  %s\n" "$label" "-" "SKIP" "-" "missing $src_path"
    skip=$((skip + 1)); continue
  fi
  if [ ! -d "$wt_path" ]; then
    printf "%-22s %-6s %-8s %6s  %s\n" "$label" "-" "SKIP" "-" "worktree setup failed"
    skip=$((skip + 1)); continue
  fi
  tool="${build_cmd%% *}"
  if ! check_tool "$tool"; then
    printf "%-22s %-6s %-8s %6s  %s\n" "$label" "-" "SKIP" "-" "tool $tool not installed"
    skip=$((skip + 1)); continue
  fi

  set +o pipefail
  rename_json=$(cd "$wt_path" && "$EDR_BIN" rename "$target" --to "$new_name" --cross-file --force 2>&1 | head -1)
  set -o pipefail
  status=$(printf "%s" "$rename_json" | sed -n 's/.*"status":"\([^"]*\)".*/\1/p')
  mode=$(printf "%s" "$rename_json" | sed -n 's/.*"mode":"\([^"]*\)".*/\1/p')
  files_changed=$(cd "$wt_path" && git diff --stat 2>/dev/null | tail -1 | awk '{print $1}')
  [ -z "$files_changed" ] && files_changed=0

  result="-"; notes=""
  if [ "$status" = "applied" ]; then
    log="/tmp/rename_correctness_${label//[^a-zA-Z0-9_]/_}.log"
    (cd "$wt_path" && eval "$build_cmd") >"$log" 2>&1
    if [ $? -eq 0 ]; then
      result="PASS"; pass=$((pass + 1))
    else
      result="FAIL"; fail=$((fail + 1))
      notes=$(grep -Ei "error|Error" "$log" | head -1 | cut -c1-80)
      notes="$notes (see $log)"
    fi
  elif [ "$status" = "noop" ]; then
    result="NOOP"; skip=$((skip + 1)); notes="rename reported no changes"
  else
    result="ERR"; fail=$((fail + 1))
    notes=$(printf "%s" "$rename_json" | cut -c1-60)
  fi

  printf "%-22s %-6s %-8s %6s  %s\n" "$label" "${mode:--}" "$result" "$files_changed" "$notes"

  # Restore the worktree to HEAD for the next iteration. Safe — the
  # worktree is disposable and shares .git with the source repo,
  # which is never touched.
  git -C "$wt_path" reset --hard HEAD >/dev/null 2>&1
done

echo
printf "Total: %d  PASS: %d  FAIL: %d  SKIP: %d  Mode: %s\n" "$total" "$pass" "$fail" "$skip" "$EDR_EVAL_FORCE_MODE"
