# The askfirst policy language

One rule per line, evaluated **top to bottom — the first rule whose
matchers all hold decides**. If nothing matches, the `default` directive
applies (and if there is no `default`, the built-in fallback is `ask`).
`#` starts a comment; blank lines are ignored.

```
<effect> <matcher> [<matcher> ...] [reason:"..."]
default <effect>
```

## Effects

| Effect | Meaning | `submit` exit code |
|---|---|---|
| `allow` | auto-approve; the agent proceeds immediately | 0 |
| `deny` | auto-reject; the agent is refused immediately | 1 |
| `ask` | queue for a human in the inbox | 4 (until decided) |

A rule needs at least one matcher. `reason:"…"` is an annotation, not a
matcher: on `deny` it is returned to the agent, on `ask` it is shown to
the reviewer as context, and it always lands in the audit log.

## Matchers

All matchers on a rule are ANDed. Values containing spaces are
double-quoted; inside quotes `\"` and `\\` are the only escapes, so
regex classes like `\s` can be written directly.

| Matcher | Example | Semantics |
|---|---|---|
| `action:<glob>` | `action:payments.*` | glob on the action name: `*` = any run of characters (dots included), `?` = exactly one |
| `agent:<glob>` | `agent:ci-*` | glob on the submitting agent's identity |
| `key=value` | `mode=readonly` | scalar equality on the value's canonical text; numbers compare numerically (`3` matches `3.0`) |
| `key!=value` | `env!=sandbox` | key **must be present** and not equal — a missing key never matches |
| `key~regex` | `command~"rm\s+-rf"` | unanchored RE2 search on the value rendered as text |
| `key<n` / `key>n` | `amount<50` | numeric comparison; **JSON numbers only** — strings never compare |

Param keys are dotted paths into the request's JSON parameters:
`user.role=admin` reads `params.user.role`. The words `action`, `agent`,
and `reason` are reserved and cannot name a top-level param matcher.

## Fail-closed semantics

The evaluator is deliberately strict about anything ambiguous. A matcher
that cannot be positively satisfied simply does not match, and the
request falls through to later rules or the default:

- a **missing key** never matches — not even `!=`, otherwise omitting a
  param would be a way to satisfy inequality rules;
- a **numeric string** (`"49"`) never satisfies `<` / `>` — silent
  coercion would let sloppy client typing widen an auto-approval;
- **arrays and objects** never match scalar operators, including `~`;
- a **path through a non-object** (`user.role` when `user` is a string)
  fails the walk.

Bad regexes and non-numeric comparison literals are rejected when the
policy is parsed (`askfirst policy check`), never at decision time.

## Ordering idioms

Because the first match wins, order encodes intent:

```
# hard denials first, so nothing can shadow them
deny action:shell.exec command~"rm\s+-rf" reason:"destructive command"

# then scoped auto-approvals
allow action:deploy.run agent:ci-* env=staging
allow action:payments.refund amount<50

# then explicit review queues with reviewer context
ask action:payments.refund reason:"refund above the auto-approve limit"

# and a safe fallthrough
default ask
```

`askfirst policy test --action … --param …` dry-runs a request and
prints exactly which line decided, without touching the queue.

## Grammar (informal)

```
policy   := (line "\n")*
line     := comment | blank | rule | default
rule     := effect matcher+ [reason]
default  := "default" effect          # at most once per file
effect   := "allow" | "deny" | "ask"
matcher  := "action:" glob | "agent:" glob | param
param    := key op value              # key: [A-Za-z0-9_.-]+
op       := "=" | "!=" | "~" | "<" | ">"
reason   := "reason:" string
```
