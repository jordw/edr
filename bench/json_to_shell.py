#!/usr/bin/env python3
"""Convert a JSON scenario file to shell variable assignments.

DEPRECATED: native_comparison.sh now reads JSON directly via jq.
This script is kept for backward compatibility only.

Usage: python3 json_to_shell.py scenarios/fixture.json [BASE_DIR]

Emits shell variable assignments compatible with native_comparison.sh profiles.
"""

import json
import os
import re
import sys


def shell_dquote(s: str) -> str:
    """Escape a string for use inside double quotes in shell.

    Handles: backslash, double-quote, dollar, backtick, exclamation.
    """
    s = s.replace("\\", "\\\\")
    s = s.replace('"', '\\"')
    s = s.replace("$", "\\$")
    s = s.replace("`", "\\`")
    s = s.replace("!", "\\!")
    return f'"{s}"'


def shell_squote(s: str) -> str:
    """Escape a string for use in $'...' (ANSI-C quoting).

    Handles: backslash, single-quote, and control characters.
    """
    s = s.replace("\\", "\\\\")
    s = s.replace("'", "\\'")
    s = s.replace("\n", "\\n")
    s = s.replace("\t", "\\t")
    return f"$'{s}'"


def emit(name: str, value, style="dquote"):
    """Print a shell variable assignment."""
    if style == "int":
        print(f"{name}={value}")
    elif style == "squote":
        print(f"{name}={shell_squote(str(value))}")
    else:
        print(f"{name}={shell_dquote(str(value))}")


def emit_array(name: str, items: list[str]):
    """Print a shell array assignment with properly escaped elements."""
    if len(items) <= 3:
        elems = " ".join(shell_dquote(item) for item in items)
        print(f"{name}=({elems})")
    else:
        lines = "\n".join(f"    {shell_dquote(item)}" for item in items)
        print(f"{name}=(\n{lines}\n)")


def main():
    if len(sys.argv) < 2:
        print("usage: json_to_shell.py <scenario.json> [BASE_DIR]", file=sys.stderr)
        sys.exit(1)

    with open(sys.argv[1]) as f:
        data = json.load(f)

    base_dir = sys.argv[2] if len(sys.argv) > 2 else os.environ.get("BASE_DIR", "/tmp")

    root = data["root"].replace("${BASE_DIR}", base_dir)

    emit("BENCH_NAME", data["name"])
    emit("BENCH_ROOT", root)
    emit("SCOPE_DIR", data.get("scope_dir", "."))
    print()

    s = data["scenarios"]

    if "understand_api" in s:
        sc = s["understand_api"]
        emit("API_FILE", sc["file"])
        emit("API_READ_SPEC", sc["spec"])
        print()

    if "read_symbol" in s:
        sc = s["read_symbol"]
        emit("READ_SYMBOL_FILE", sc["file"])
        emit("READ_SYMBOL_SPEC", sc["spec"])
        print()

    if "find_refs" in s:
        sc = s["find_refs"]
        emit("REFS_PATTERN", sc["pattern"])
        emit("REFS_GREP_ROOT", sc["grep_root"])
        emit_array("REFS_ARGS", sc["args"])
        print()

    if "search_context" in s:
        sc = s["search_context"]
        emit("SEARCH_PATTERN", sc["pattern"])
        emit("SEARCH_ROOT", sc["search_root"])
        emit("SEARCH_BUDGET", sc["budget"], style="int")
        print()

    if "orient_map" in s:
        sc = s["orient_map"]
        emit("ORIENT_DIR", sc["dir"])
        emit("ORIENT_BUDGET", sc["budget"], style="int")
        emit_array("ORIENT_GLOBS", sc["globs"])
        emit_array("ORIENT_READ_FILES", sc["read_files"])
        print()

    if "edit_function" in s:
        sc = s["edit_function"]
        emit("EDIT_FILE", sc["file"])
        emit("EDIT_OLD_TEXT", sc["old_text"], style="squote")
        emit("EDIT_NEW_TEXT", sc["new_text"], style="squote")
        print()

    if "add_method" in s:
        sc = s["add_method"]
        emit("WRITE_FILE", sc["file"])
        emit("WRITE_INSIDE", sc["inside"])
        emit("WRITE_CONTENT", sc["content"], style="squote")
        print()

    if "multi_file_read" in s:
        sc = s["multi_file_read"]
        emit("MULTI_READ_BUDGET", sc["budget"], style="int")
        emit_array("MULTI_READ_FILES", sc["files"])
        print()

    if "explore_symbol" in s:
        sc = s["explore_symbol"]
        emit("EXPLORE_PATTERN", sc["pattern"])
        emit("EXPLORE_GREP_ROOT", sc["grep_root"])
        emit_array("EXPLORE_ARGS", sc["args"])
        emit_array("EXPLORE_NATIVE_READ_FILES", sc["native_read_files"])


if __name__ == "__main__":
    main()
