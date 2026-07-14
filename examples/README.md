# askfirst examples

Runnable, offline, self-contained. Build the binary first
(`go build -o askfirst ./cmd/askfirst`) and put it on your `PATH`.

## agent-wrapper.sh

The one-line integration: guard any shell command behind approval. The
wrapper submits the command as a `shell.exec` action, waits up to five
minutes for a decision, and only `exec`s the command on approval.

```bash
bash examples/agent-wrapper.sh /tmp/askfirst-demo git push origin main
# in another terminal:
askfirst --dir /tmp/askfirst-demo list
askfirst --dir /tmp/askfirst-demo approve <id> --reason "release window open"
```

Denied or timed-out commands never run; the askfirst exit code
(1 denied, 4 pending/expired) passes through to the caller.

## seed-inbox.sh

Fills a fresh inbox with a realistic mix — an auto-approved read, an
auto-denied destructive command, and two queued requests — so you can
try `list`, `show`, `approve`, `deny`, and `log` right away.

```bash
bash examples/seed-inbox.sh /tmp/askfirst-demo
askfirst --dir /tmp/askfirst-demo list
```

## policy.rules

A fuller example policy for a small agent fleet: hard denials for
destructive commands and PII tables, scoped auto-approval for staging
deploys by CI agents, an amount threshold for refunds, and explicit ask
queues with reasons that show up in the inbox. Try it without touching
your real policy:

```bash
askfirst --dir /tmp/askfirst-demo policy test --policy examples/policy.rules \
  --agent ci-runner --action deploy.run --param env=staging
```
