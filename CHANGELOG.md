# Changelog

## Done

### Session trace collection

Append-only `traces.db` captures structured traces per MCP session: calls, edit events, verify events, query events, and session optimization stats (delta reads, body dedup, slim edits). Async flush goroutine with buffered channel ensures zero impact on MCP response latency.

`edr bench-session` scores a completed session with derived analysis: read efficiency, edit success rate, verify pass rate, optimization rate, tokens per call, edits reverted.

**Benchmark baseline** (55-call multi-language session across Go/Python/Rust/C/Java/Ruby/JS/TSX):
- 25% read efficiency (8 delta reads / 32 total reads)
- 27% optimization rate (delta + dedup + slim hits / total optimizable calls)
- 180 tokens/call average
- 7 body dedup hits, 8 delta reads
- ~14ms/session, ~17.5KB total response

### Bug fixes from evaluation

- `explore` with nonexistent symbol returns `ok: true` with empty data instead of an error. Fixed in `gather.go:GatherBySearch` — now returns proper error.
- `diff` query returns "no diff stored" with no explanation. Error message now explains session-scoping and suggests alternatives.
- `write --inside` doesn't accept `--new_text` in CLI mode. Added `--content` and `--new_text` flags to write command; fixed stdin fallback logic.
