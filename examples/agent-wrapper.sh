#!/usr/bin/env bash
# Guard any shell command behind askfirst approval.
#
#   bash examples/agent-wrapper.sh <inbox-dir> <command> [args...]
#
# The command is submitted as a shell.exec action with the full command
# line as its `command` parameter. It only executes if the submission
# comes back approved — by a policy rule or by a human while we wait.
# Denials, timeouts, and expiry all block execution, each with the
# askfirst exit code (1 denied, 4 pending/expired) passed through.
set -euo pipefail

if [ $# -lt 2 ]; then
  echo "usage: $0 <inbox-dir> <command> [args...]" >&2
  exit 2
fi

DIR="$1"
shift

if askfirst --dir "$DIR" submit \
  --agent "wrapper:$(whoami)" \
  --action shell.exec \
  --param command="$*" \
  --ttl 5m --wait --timeout 5m; then
  exec "$@"
else
  status=$?
  echo "blocked: askfirst did not approve (exit $status)" >&2
  exit "$status"
fi
