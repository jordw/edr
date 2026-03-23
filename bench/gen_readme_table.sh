#!/usr/bin/env bash
# Generate the README.md benchmark tables from real-repo benchmark results.
#
# Usage:
#   bash bench/run_real_repo_benchmarks.sh   # run first (~10 min)
#   bash bench/gen_readme_table.sh           # then generate tables
#
# Reads results from $RESULTS_DIR (default: /tmp/edr-bench-results/*.out).
# Outputs markdown to stdout — paste into README.md.
set -euo pipefail

RESULTS_DIR="${RESULTS_DIR:-/tmp/edr-bench-results}"

if [ ! -d "$RESULTS_DIR" ]; then
    echo "No results directory at $RESULTS_DIR" >&2
    echo "Run: bash bench/run_real_repo_benchmarks.sh" >&2
    exit 1
fi

# Repo metadata: display name, GitHub URL, language, approximate file count.
REPOS=(
    "urfave-cli|[urfave/cli](https://github.com/urfave/cli)|Go|~70"
    "vitess-sqlparser|[vitess/sqlparser](https://github.com/vitessio/vitess)|Go|~70"
    "vitess-vtgate|[vitess/vtgate](https://github.com/vitessio/vitess)|Go|~490"
    "click|[pallets/click](https://github.com/pallets/click)|Python|~17"
    "thor|[rails/thor](https://github.com/rails/thor)|Ruby|~35"
    "redux-toolkit|[reduxjs/redux-toolkit](https://github.com/reduxjs/redux-toolkit)|TS|~190"
    "django|[django/django](https://github.com/django/django)|Python|~880"
)

extract_json() {
    # The JSON summary is the last JSON object in the file.
    sed -n '/^{$/,/^}$/p' "$1" | tail -n +1
}

fmt_kb() {
    awk -v b="$1" 'BEGIN { printf "%dKB", int(b / 1024 + 0.5) }'
}

# --- Summary table ---
echo "| Repo | Lang | Files | Baseline | edr | Reduction |"
echo "|---|---|---|---|---|---|"

pcts=""

for entry in "${REPOS[@]}"; do
    IFS='|' read -r key display lang files <<< "$entry"
    outfile="$RESULTS_DIR/${key}.out"
    if [ ! -f "$outfile" ]; then
        echo "| $display | $lang | $files | — | — | — |"
        continue
    fi

    json=$(extract_json "$outfile")
    native_bytes=$(echo "$json" | jq '.totals.native_bytes')
    edr_bytes=$(echo "$json" | jq '.totals.edr_bytes')
    native_calls=$(echo "$json" | jq '.totals.native_calls')
    edr_calls=$(echo "$json" | jq '.totals.edr_calls')
    pct=$(echo "$json" | jq '.totals.savings_pct')

    pcts="$pcts $pct"

    echo "| $display | $lang | $files | $(fmt_kb "$native_bytes") / ${native_calls} calls | $(fmt_kb "$edr_bytes") / ${edr_calls} calls | **${pct}%** |"
done

# Compute median reduction
median_pct=$(echo "$pcts" | tr ' ' '\n' | grep -v '^$' | sort -n | awk 'NR==1{n=0} {a[n++]=$1} END{print a[int(n/2)]}')
echo ""
echo "Median reduction: **${median_pct}%** across repos. edr loses on plain text search (structured JSON adds overhead vs raw grep — see breakdown below), but wins everywhere else. Call counts are summed across all 9 scenarios; each edr scenario is 1 call."

# --- Per-scenario breakdown (urfave/cli) ---
echo ""
echo "<details>"
echo "<summary>Per-scenario breakdown (urfave/cli)</summary>"
echo ""

urfave_file="$RESULTS_DIR/urfave-cli.out"
if [ -f "$urfave_file" ]; then
    urfave_json=$(extract_json "$urfave_file")

    echo "| Scenario | Baseline | edr | Reduction |"
    echo "|---|---|---|---|"

    # scenario_name|display_name|native_note|edr_note
    scenarios=(
        "understand_api|Understand a class API|whole file|\`--signatures\`"
        "read_symbol|Read a specific function|grep + range read|symbol read"
        "find_refs|Find references|grep + 3 range reads|\`refs\`"
        "search_context|Search with context|grep -C3|structured"
        "orient_map|Orient in codebase|glob + 3 reads|\`map\`"
        "edit_function|Edit a function|grep + range + edit|batch"
        "add_method|Add method to a class|grep + range + write|\`--inside\`"
        "multi_file_read|Multi-file read||batched"
        "explore_symbol|Explore a symbol|grep + 3 range reads|"
    )

    for s in "${scenarios[@]}"; do
        IFS='|' read -r sname display native_note edr_note <<< "$s"

        nb=$(echo "$urfave_json" | jq -r ".scenarios[] | select(.name == \"$sname\") | .native_bytes")
        nc=$(echo "$urfave_json" | jq -r ".scenarios[] | select(.name == \"$sname\") | .native_calls")
        eb=$(echo "$urfave_json" | jq -r ".scenarios[] | select(.name == \"$sname\") | .edr_bytes")
        ec=$(echo "$urfave_json" | jq -r ".scenarios[] | select(.name == \"$sname\") | .edr_calls")
        pct=$(echo "$urfave_json" | jq -r ".scenarios[] | select(.name == \"$sname\") | .savings_pct")

        [ -z "$nb" ] || [ "$nb" = "null" ] && continue

        native_str="${nb}B"
        [ "$nc" -gt 1 ] 2>/dev/null && native_str="${nb}B / ${nc} calls"
        [ -n "$native_note" ] && native_str="${native_str} (${native_note})"

        edr_str="${eb}B"
        [ "$ec" -gt 1 ] 2>/dev/null && edr_str="${eb}B / ${ec} calls"
        [ -n "$edr_note" ] && edr_str="${edr_str} (${edr_note})"

        echo "| $display | $native_str | $edr_str | **${pct}%** |"
    done

    # Totals
    tn=$(echo "$urfave_json" | jq '.totals.native_bytes')
    te=$(echo "$urfave_json" | jq '.totals.edr_bytes')
    tnc=$(echo "$urfave_json" | jq '.totals.native_calls')
    tec=$(echo "$urfave_json" | jq '.totals.edr_calls')
    tp=$(echo "$urfave_json" | jq '.totals.savings_pct')
    echo "| **Total** | **${tn}B / ${tnc} calls** | **${te}B / ${tec} calls** | **${tp}%** |"
fi

echo ""
echo "</details>"
