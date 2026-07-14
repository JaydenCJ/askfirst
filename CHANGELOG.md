# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2026-07-12

### Added

- Line-oriented policy language: `allow` / `deny` / `ask` rules with
  first-match-wins evaluation, an optional `default` directive (built-in
  fallback `ask`), `#` comments, quoted values, and `reason:"…"`
  annotations that reach the agent, the reviewer, and the audit log.
- Matchers: `action:`/`agent:` globs (`*`, `?`), scalar equality and
  inequality, unanchored RE2 search, numeric `<`/`>` thresholds, and
  dotted paths into nested JSON params — all fail-closed (missing keys,
  numeric strings, and composite values never satisfy a matcher).
- File-backed approval queue: one atomically-written JSON document per
  request, computed TTL expiry with no background sweeper, sentinel
  errors for decided/expired conflicts, and an append-only `audit.jsonl`
  recording submitted / auto_approved / auto_denied / approved / denied.
- CLI inbox: `init`, `submit` (typed `--param` values, `--params` JSON,
  `--ttl`, blocking `--wait`), `list`, `show`, `approve`/`deny` with
  reasons, `log`, and `policy check` / `policy test` dry-runs that quote
  the deciding line.
- Exit-code contract for shell integration: 0 approved, 1 denied,
  2 usage, 3 runtime, 4 pending/timed-out/expired.
- Loopback HTTP API (`serve`): submit with synchronous auto-decisions
  (202 for pending), poll and long-poll (`?wait=`), inbox listing,
  approve/deny with 404/409 semantics, policy introspection, `/healthz`,
  optional bearer token — required before any non-loopback bind.
- Runnable examples (`examples/agent-wrapper.sh`,
  `examples/seed-inbox.sh`, `examples/policy.rules`) and a policy
  language reference (`docs/policy.md`).
- 90 deterministic offline tests (pinned clocks and IDs, in-process CLI
  and HTTP integration) and `scripts/smoke.sh`.

[0.1.0]: https://github.com/JaydenCJ/askfirst/releases/tag/v0.1.0
