# Contributing to askfirst

Issues, discussions and pull requests are all welcome.

## Getting started

You need Go ≥1.22; nothing else — the project has zero runtime
dependencies and the test suite runs fully offline.

```bash
git clone https://github.com/JaydenCJ/askfirst && cd askfirst
go build ./...
go test ./...
bash scripts/smoke.sh
```

`scripts/smoke.sh` builds the binary, initializes a throwaway inbox in a
temp dir, and walks the whole lifecycle — auto-approve, auto-deny, queue,
human decision, audit trail, and the 127.0.0.1 HTTP API; it must finish
by printing `SMOKE OK`.

## Before you open a pull request

1. `gofmt -l .` reports nothing (formatting is enforced).
2. `go vet ./...` passes with no findings.
3. `go test ./...` passes (90 deterministic tests, no network).
4. `bash scripts/smoke.sh` prints `SMOKE OK`.
5. Add tests for behavior changes; keep logic in pure, unit-testable
   modules (the policy engine and queue never touch the terminal or the
   network — only `cli` and `httpapi` do I/O with the outside).

## Ground rules

- Keep dependencies at zero; adding one needs strong justification in
  the PR.
- No network calls at startup, no telemetry; the HTTP API binds loopback
  unless the operator explicitly configures a token.
- Evaluation must stay fail-closed: any change that lets an ambiguous
  request reach `allow` without a positively-satisfied matcher is a bug,
  even if a test doesn't catch it yet.
- Every decision path must write its audit event; the CLI and the HTTP
  API go through `internal/inbox` so the records stay identical.
- Code comments and doc comments are written in English.

## Reporting bugs

Include the output of `askfirst version`, the exact command (or HTTP
request) you ran, the policy file, and — for wrong decisions — the
output of `askfirst policy test` with the same action and params, since
that prints exactly which rule the evaluator matched.

## Security

Please do not open public issues for security problems; use GitHub's
private vulnerability reporting on this repository instead.
