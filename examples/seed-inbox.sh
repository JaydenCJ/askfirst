#!/usr/bin/env bash
# Seed a demo inbox so you can explore the review workflow immediately.
#
#   bash examples/seed-inbox.sh /tmp/askfirst-demo
#   askfirst --dir /tmp/askfirst-demo list
#
# Submits a mix of requests against the starter policy: one auto-approved
# read, one auto-denied destructive command, and two queued requests that
# wait for your decision. Offline and idempotent-ish (re-running adds new
# requests; use a fresh directory for a clean slate).
set -euo pipefail

DIR="${1:?usage: seed-inbox.sh <inbox-dir>}"

askfirst --dir "$DIR" init

submit() {
  # Exit codes 1 (denied) and 4 (pending) are expected outcomes here.
  askfirst --dir "$DIR" submit "$@" || true
}

submit --agent docs-bot --action fs.read --param path=README.md
submit --agent ops-bot --action shell.exec --param "command=rm -rf /var/lib/cache"
submit --agent billing-bot --action payments.refund --param amount=1200 --param order=8812
submit --agent mailer-bot --action mail.send --param to=team@example.test --param subject="Weekly digest"

echo
echo "Inbox seeded. Try:"
echo "  askfirst --dir $DIR list"
echo "  askfirst --dir $DIR approve <id> --reason 'looks fine'"
echo "  askfirst --dir $DIR log"
