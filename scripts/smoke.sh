#!/usr/bin/env bash
# End-to-end smoke test for askfirst: builds the binary, initializes a
# fresh inbox in a temp dir, and walks the whole approval lifecycle —
# auto-approve, auto-deny, queue, human decision, audit trail, policy
# dry-run, and the local HTTP API. No external network, idempotent,
# finishes in seconds.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKDIR="$(mktemp -d)"
SRV_PID=""
cleanup() {
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null || true
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

fail() {
  echo "SMOKE FAIL: $*" >&2
  exit 1
}

# Multi-line command output is captured into a variable before grepping.
# Piping straight into `grep -q` races under pipefail: grep exits at the
# first match, and the tool's remaining writes die with SIGPIPE (141).

BIN="$WORKDIR/askfirst"
INBOX="$WORKDIR/inbox"

echo "1. build"
(cd "$ROOT" && go build -o "$BIN" ./cmd/askfirst) || fail "go build failed"

echo "2. version matches manifest"
"$BIN" --version | grep -qx "askfirst 0.1.0" || fail "--version mismatch"

echo "3. init writes the starter policy"
OUT="$("$BIN" --dir "$INBOX" init)" || fail "init exited non-zero"
echo "$OUT" | grep -q "wrote starter policy" || fail "init output wrong"
[ -f "$INBOX/policy.rules" ] || fail "policy.rules missing"
"$BIN" --dir "$INBOX" policy check | grep -q "policy OK: 5 rules, default ask" \
  || fail "starter policy should validate"

echo "4. safe action is auto-approved (exit 0)"
OUT="$("$BIN" --dir "$INBOX" submit --agent smoke-bot --action fs.read)" \
  || fail "auto-approve should exit 0"
echo "$OUT" | grep -q "^approved af-" || fail "missing approved line"
echo "$OUT" | grep -q "policy (line" || fail "decision should name the rule"

echo "5. destructive action is auto-denied (exit 1)"
set +e
"$BIN" --dir "$INBOX" submit --agent smoke-bot --action shell.exec \
  --param "command=rm -rf /srv/cache" > "$WORKDIR/deny.out"
CODE=$?
set -e
[ "$CODE" -eq 1 ] || fail "auto-deny should exit 1, got $CODE"
grep -q "destructive command" "$WORKDIR/deny.out" || fail "deny reason missing"

echo "6. risky action queues as pending (exit 4)"
set +e
OUT="$("$BIN" --dir "$INBOX" submit --agent billing-bot --action payments.refund \
  --param amount=1200 --param currency=usd)"
CODE=$?
set -e
[ "$CODE" -eq 4 ] || fail "ask should exit 4, got $CODE"
ID="$(echo "$OUT" | head -1 | awk '{print $2}')"
[ -n "$ID" ] || fail "could not extract request id"

echo "7. the inbox lists the pending request"
LIST="$("$BIN" --dir "$INBOX" list)" || fail "list exited non-zero"
echo "$LIST" | grep -q "$ID" || fail "pending request not listed"
echo "$LIST" | grep -q "1 pending request" || fail "count line wrong"

echo "8. a human approves it with a reason"
"$BIN" --dir "$INBOX" approve "$ID" --reason "verified with finance" \
  | grep -q "^approved $ID" || fail "approve failed"
SHOW="$("$BIN" --dir "$INBOX" show "$ID")" || fail "show exited non-zero"
echo "$SHOW" | grep -q "verified with finance" || fail "reason not persisted"

echo "9. deciding twice fails loudly"
if "$BIN" --dir "$INBOX" deny "$ID" 2>/dev/null; then
  fail "second decision should be rejected"
fi

echo "10. the audit trail has every event"
LOG="$("$BIN" --dir "$INBOX" log)"
for needle in submitted auto_approved auto_denied approved "verified with finance"; do
  echo "$LOG" | grep -q "$needle" || fail "audit log missing: $needle"
done

echo "11. policy test dry-runs without touching the queue"
OUT="$("$BIN" --dir "$INBOX" policy test --action payments.refund --param amount=10)" \
  || fail "policy test allow should exit 0"
echo "$OUT" | grep -q "decision: allow" || fail "policy test should allow small refund"
set +e
"$BIN" --dir "$INBOX" policy test --action shell.exec --param "command=rm -rf /" >/dev/null
[ $? -eq 1 ] || fail "policy test deny should exit 1"
set -e

echo "12. HTTP API round trip on 127.0.0.1"
if command -v curl >/dev/null 2>&1; then
  "$BIN" --dir "$INBOX" serve --addr 127.0.0.1:0 > "$WORKDIR/serve.log" 2>&1 &
  SRV_PID=$!
  for _ in $(seq 1 50); do
    grep -q "listening on" "$WORKDIR/serve.log" 2>/dev/null && break
    sleep 0.1
  done
  URL="$(sed -n 's/.*listening on \(http[^ ]*\).*/\1/p' "$WORKDIR/serve.log")"
  [ -n "$URL" ] || fail "server never reported its address"
  # --noproxy '*' keeps loopback traffic away from any configured proxy.
  curl_local() { curl -sS --noproxy '*' "$@"; }
  curl_local "$URL/healthz" | grep -q '"ok":true' || fail "healthz failed"
  API_ID="$(curl_local -X POST "$URL/v1/requests" \
    -d '{"agent":"api-bot","action":"deploy.run","params":{"replicas":12}}' \
    | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')"
  [ -n "$API_ID" ] || fail "API submit returned no id"
  curl_local -X POST "$URL/v1/requests/$API_ID/deny" -d '{"reason":"not during freeze"}' \
    | grep -q '"status":"denied"' || fail "API deny failed"
  curl_local "$URL/v1/requests/$API_ID" | grep -q '"not during freeze"' \
    || fail "API decision not persisted"
  kill "$SRV_PID" 2>/dev/null || true
  SRV_PID=""
else
  echo "   (curl not found — skipping HTTP round trip)"
fi

echo "13. usage errors exit 2"
set +e
"$BIN" --dir "$INBOX" frobnicate >/dev/null 2>&1
[ $? -eq 2 ] || fail "unknown command should exit 2"
"$BIN" --dir "$INBOX" serve --addr 0.0.0.0:0 >/dev/null 2>&1
[ $? -eq 2 ] || fail "non-loopback serve without --token should exit 2"
set -e

echo "SMOKE OK"
