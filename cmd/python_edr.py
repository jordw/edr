"""edr — Python bindings for the edr CLI.

Thin wrapper around the `edr` binary. Each call shells out to `edr`, parses
the JSON header, and returns typed results. Session state (op log, undo
stack, assumption tracking) is maintained by edr itself, keyed on the
calling process's PID — so any sequence of calls from a single Python
process shares state automatically.

Usage:
    import edr
    sym = edr.focus("kernel/sched/core.c:sched_tick")
    for caller in edr.callers(sym):
        edr.edit(caller.file, old="x", new="y", scope=caller.name)
    edr.undo()

Requires the `edr` binary on $PATH.
"""

from __future__ import annotations

import json
import re
import shutil
import subprocess
from dataclasses import dataclass, field

_ORIENT_LINE = re.compile(r"^([^:]+):(\d+)-(\d+):\s+(\S+)\s+(\S+)(?:\s+\((\d+)\s+matches\))?$")


class EdrError(Exception):
    """Raised when an edr invocation returns a non-zero exit or an error code."""

    def __init__(self, message: str, code: str | None = None, stderr: str = ""):
        super().__init__(message)
        self.code = code
        self.stderr = stderr


class AmbiguousError(EdrError):
    """Raised when a bare-name focus() matches multiple symbols.

    `candidates` is the ranked shortlist; pick one and call focus() with
    `path:symbol` to disambiguate.
    """

    def __init__(self, name: str, candidates: "list[Symbol]"):
        super().__init__(
            f"{name!r} is ambiguous ({len(candidates)} candidates); "
            f"use focus('path:symbol') to pick one",
            code="ambiguous",
        )
        self.name = name
        self.candidates = candidates


_SHORTLIST_LINE = re.compile(r"^\s*\d+\.\s+([^:]+):(\d+)\s+(\S+)\s+(\S+)\s*$")


class OrientResult(list):
    """A list of Symbols with truncation metadata attached.

    Behaves like ``list[Symbol]``, with extra attributes:
      * ``total`` — total symbols matching (pre-budget)
      * ``shown`` — symbols actually returned
      * ``truncated`` — True when ``total > shown``
    """

    total: int = 0
    shown: int = 0
    truncated: bool = False


@dataclass
class Symbol:
    """A code symbol returned by focus/orient/callers."""

    file: str
    name: str
    type: str  # "function", "method", "struct", "class", ...
    start_line: int
    end_line: int
    signature: str | None = None
    content: str | None = None  # full body when focus returns a single symbol


@dataclass
class EditResult:
    """Result of an edit/rename/changesig/extract/write operation."""

    file: str
    status: str  # "applied", "dry_run", "noop", "reverted"
    diff: str | None = None
    hash: str | None = None
    message: str | None = None
    files_changed: list[str] = field(default_factory=list)


@dataclass
class FileEntry:
    """A file from orient or files results."""

    path: str
    symbols: list[Symbol] = field(default_factory=list)


def _binary() -> str:
    path = shutil.which("edr")
    if not path:
        raise EdrError("`edr` binary not found on $PATH")
    return path


def _run(args: list[str], *, root: str | None = None, stdin: str | None = None) -> dict:
    """Run edr and parse the envelope. Returns the first op's result dict."""
    cmd = [_binary()]
    if root:
        cmd += ["--root", root]
    cmd += args
    try:
        proc = subprocess.run(
            cmd,
            input=stdin,
            capture_output=True,
            text=True,
            check=False,
        )
    except FileNotFoundError as e:
        raise EdrError(f"failed to exec edr: {e}") from e

    out = proc.stdout.strip()
    if not out:
        if proc.returncode != 0:
            raise EdrError(
                f"edr exited {proc.returncode}: {proc.stderr.strip() or 'no output'}",
                stderr=proc.stderr,
            )
        return {}

    # Output is JSON header (line 1) + optional body, separated by \n.
    # Multi-op batch uses `---` between ops. For single-op calls we take line 1.
    first_line, _, body = out.partition("\n")
    try:
        header = json.loads(first_line)
    except json.JSONDecodeError as e:
        raise EdrError(f"failed to parse edr JSON: {e}\noutput: {out[:500]}") from e

    if isinstance(header, dict) and "error" in header:
        raise EdrError(header["error"], code=header.get("ec"), stderr=proc.stderr)

    if proc.returncode != 0:
        raise EdrError(
            f"edr exited {proc.returncode}: {proc.stderr.strip() or header}",
            code=header.get("ec") if isinstance(header, dict) else None,
            stderr=proc.stderr,
        )

    if isinstance(header, dict) and body:
        header["_body"] = body
    return header if isinstance(header, dict) else {}


def _symbol_from_dict(d: dict, file_hint: str | None = None) -> Symbol:
    lines = d.get("lines") or [0, 0]
    return Symbol(
        file=d.get("file") or file_hint or "",
        name=d.get("name") or d.get("sym") or "",
        type=d.get("kind") or d.get("type") or "",
        start_line=int(d.get("line") or d.get("start_line") or (lines[0] if len(lines) > 0 else 0)),
        end_line=int(d.get("end_line") or (lines[1] if len(lines) > 1 else 0)),
        signature=d.get("signature") or d.get("sig"),
        content=d.get("content") or d.get("_body"),
    )


def _parse_shortlist_body(body: str) -> list[Symbol]:
    """Parse focus()'s ambiguous shortlist body.

    Format: `  N. file:line  type name`
    """
    out: list[Symbol] = []
    for line in body.split("\n"):
        m = _SHORTLIST_LINE.match(line)
        if not m:
            continue
        file, line_no, typ, name = m.groups()
        out.append(Symbol(file=file, name=name, type=typ, start_line=int(line_no), end_line=int(line_no)))
    return out


def _parse_orient_body(body: str) -> list[Symbol]:
    """Parse orient's plain-text body lines into Symbols.

    Format: `file:start-end: type name [(N matches)]`
    """
    out: list[Symbol] = []
    for line in body.split("\n"):
        m = _ORIENT_LINE.match(line)
        if not m:
            continue
        file, s, e, typ, name, _ = m.groups()
        out.append(Symbol(file=file, name=name, type=typ, start_line=int(s), end_line=int(e)))
    return out


# ---------------------------------------------------------------- focus / read


def focus(
    target: str,
    *,
    sig: bool = False,
    skeleton: bool = False,
    budget: int | None = None,
    expand: str | None = None,
    root: str | None = None,
) -> Symbol:
    """Read a symbol or file. `target` is `path`, `path:symbol`, or bare symbol name."""
    args = ["focus", target]
    if sig:
        args.append("--sig")
    if skeleton:
        args.append("--skeleton")
    if budget is not None:
        args += ["--budget", str(budget)]
    if expand:
        args += ["--expand", expand]
    result = _run(args, root=root)
    # Ambiguous bare-name: header has method="heuristic_ranking" and no file.
    if result.get("method") == "heuristic_ranking" and not result.get("file"):
        candidates = _parse_shortlist_body(result.get("_body", ""))
        raise AmbiguousError(result.get("query") or target, candidates)
    # focus result: {file, sym, lines:[s,e], hash} — sym may be missing for file-only reads.
    # Derive name from sym or from the target's :suffix.
    if "sym" not in result and ":" in target and not target.startswith("/"):
        _, _, name = target.partition(":")
        if name and not name[0].isdigit():
            result.setdefault("sym", name)
    sym = _symbol_from_dict(result)
    # When --sig was requested, the body *is* the signature; don't leave it in .content.
    if sig and sym.content and not sym.signature:
        sym.signature = sym.content.strip()
        sym.content = None
    return sym


# ---------------------------------------------------------------- orient / map


def orient(
    path: str | None = None,
    *,
    grep: str | None = None,
    body: str | None = None,
    lang: str | None = None,
    type: str | None = None,
    glob: str | None = None,
    budget: int | None = None,
    root: str | None = None,
) -> list[Symbol]:
    """List symbols in a directory, filtered by name/type/language."""
    args = ["orient"]
    if path:
        args.append(path)
    for flag, val in (("--grep", grep), ("--body", body), ("--lang", lang), ("--type", type), ("--glob", glob)):
        if val:
            args += [flag, val]
    if budget is not None:
        args += ["--budget", str(budget)]
    result = _run(args, root=root)
    # orient emits structured symbols in the plain-text body; content in the
    # JSON header is stripped before output. Parse the body instead.
    out = OrientResult(_parse_orient_body(result.get("_body", "")))
    out.total = int(result.get("symbols") or 0)
    out.shown = int(result.get("shown_symbols") or len(out))
    out.truncated = bool(result.get("trunc"))
    return out


# ---------------------------------------------------------------- edit


def edit(
    file: str,
    *,
    old: str | None = None,
    new: str | None = None,
    content: str | None = None,
    scope: str | None = None,  # --in
    mkdir: bool = False,
    delete: bool = False,
    move_after: str | None = None,
    replace_all: bool = False,
    dry_run: bool = False,
    verify: bool = False,
    root: str | None = None,
) -> EditResult:
    """Edit a file. Either find-and-replace (old/new), write (content), or move (move_after)."""
    args = ["edit", file]
    if old is not None:
        args += ["--old", old]
    if new is not None:
        args += ["--new", new]
    if content is not None:
        args += ["--content", content]
    if scope:
        args += ["--in", scope]
    if move_after:
        args += ["--move-after", move_after]
    if mkdir:
        args.append("--mkdir")
    if delete:
        args.append("--delete")
    if replace_all:
        args.append("--all")
    if dry_run:
        args.append("--dry-run")
    if verify:
        args.append("--verify")
    result = _run(args, root=root)
    return EditResult(
        file=result.get("file", file),
        status=result.get("status", "unknown"),
        diff=result.get("diff") or result.get("_body"),
        hash=result.get("hash"),
        message=result.get("msg") or result.get("message"),
    )


# ---------------------------------------------------------------- rename


def rename(target: str, to: str, *, dry_run: bool = False, root: str | None = None) -> EditResult:
    args = ["rename", target, "--to", to]
    if dry_run:
        args.append("--dry-run")
    result = _run(args, root=root)
    old_name = result.get("from") or result.get("old_name") or ""
    new_name = result.get("to") or result.get("new_name") or ""
    occurrences = result.get("n") or result.get("occurrences") or 0
    files_changed = list(result.get("files_changed") or result.get("files") or [])
    return EditResult(
        file=target.split(":", 1)[0],
        status=result.get("status", "unknown"),
        diff=result.get("_body"),
        message=f"{old_name} -> {new_name}: {occurrences} occurrences in {len(files_changed)} files",
        files_changed=files_changed,
    )


# ---------------------------------------------------------------- changesig


def changesig(
    target: str,
    *,
    add: str | None = None,
    remove: int | None = None,
    at: int | None = None,
    callarg: str | None = None,
    dry_run: bool = False,
    root: str | None = None,
) -> EditResult:
    args = ["changesig", target]
    if add is not None:
        args += ["--add", add]
    if remove is not None:
        args += ["--remove", str(remove)]
    if at is not None:
        args += ["--at", str(at)]
    if callarg is not None:
        args += ["--callarg", callarg]
    if dry_run:
        args.append("--dry-run")
    result = _run(args, root=root)
    return EditResult(
        file=result.get("file", target.split(":", 1)[0]),
        status=result.get("status", "unknown"),
        diff=result.get("diff") or result.get("_body"),
        hash=result.get("hash"),
        message=result.get("message") or result.get("msg"),
    )


# ---------------------------------------------------------------- extract


def extract(
    target: str,
    *,
    name: str,
    lines: str,
    call: str | None = None,
    dry_run: bool = False,
    root: str | None = None,
) -> EditResult:
    args = ["extract", target, "--name", name, "--lines", lines]
    if call is not None:
        args += ["--call", call]
    if dry_run:
        args.append("--dry-run")
    result = _run(args, root=root)
    return EditResult(
        file=result.get("file", target.split(":", 1)[0]),
        status=result.get("status", "unknown"),
        diff=result.get("diff") or result.get("_body"),
        hash=result.get("hash"),
        message=result.get("message"),
    )


# ---------------------------------------------------------------- files / search


def files(pattern: str, *, regex: bool = False, budget: int | None = None, root: str | None = None) -> list[str]:
    """Find files containing a pattern. Returns relative file paths."""
    args = ["files", pattern]
    if regex:
        args.append("--regex")
    if budget is not None:
        args += ["--budget", str(budget)]
    result = _run(args, root=root)
    body = result.get("_body", "")
    return [line for line in body.split("\n") if line.strip()]


# ---------------------------------------------------------------- undo / status


def undo(*, root: str | None = None) -> dict:
    """Roll back the most recent mutation. Returns the restore details."""
    return _run(["undo"], root=root)


def status(*, root: str | None = None) -> dict:
    """Current edr state: index, session, fix items."""
    return _run(["status"], root=root)


# ---------------------------------------------------------------- callers


_CALLER_LINE = re.compile(r"^(\S+)\s{2,}(.*)$")
_CALLER_NAME = re.compile(r"\b(?:func|def|fn|function|method)\s+\(?[^)]*\)?\s*(\w+)|(\w+)\s*\(")


def callers(symbol: Symbol, *, root: str | None = None) -> list[Symbol]:
    """Find symbols that reference the given symbol. Uses the focus --expand callers path.

    Returns a de-duplicated list of call-site Symbols. Each result has ``file``
    and ``signature`` populated; ``name`` is extracted from the signature when
    possible.
    """
    target = f"{symbol.file}:{symbol.name}"
    args = ["focus", target, "--expand", "callers"]
    result = _run(args, root=root)

    out: list[Symbol] = []
    seen: set[tuple[str, str]] = set()
    # Preferred: structured callers in header (if the CLI ever emits them).
    for c in result.get("callers") or []:
        if isinstance(c, dict):
            sym = _symbol_from_dict(c)
            key = (sym.file, sym.signature or sym.name)
            if key in seen:
                continue
            seen.add(key)
            out.append(sym)
    if out:
        return out

    # Fallback: parse plain body lines "<file>  <signature>".
    body = result.get("_body", "")
    for line in body.split("\n"):
        line = line.rstrip()
        if not line or line.startswith("---"):
            continue
        m = _CALLER_LINE.match(line)
        if not m:
            continue
        file, sig = m.group(1), m.group(2).strip()
        key = (file, sig)
        if key in seen:
            continue
        seen.add(key)
        name = ""
        nm = _CALLER_NAME.search(sig)
        if nm:
            name = nm.group(1) or nm.group(2) or ""
        out.append(Symbol(file=file, name=name, type="", start_line=0, end_line=0, signature=sig))
    return out
