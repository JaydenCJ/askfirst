package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/JaydenCJ/askfirst/internal/httpapi"
	"github.com/JaydenCJ/askfirst/internal/inbox"
	"github.com/JaydenCJ/askfirst/internal/policy"
	"github.com/JaydenCJ/askfirst/internal/queue"
	"github.com/JaydenCJ/askfirst/internal/version"
)

func (c *ctx) cmdInit(args []string) int {
	fs := c.newFlagSet("init")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	st := c.store()
	_, statErr := os.Stat(st.PolicyPath())
	existed := statErr == nil
	if err := st.Init(policy.Starter); err != nil {
		return c.runtimeError("init failed: %v", err)
	}
	pol, err := policy.ParseFile(st.PolicyPath())
	if err != nil {
		return c.runtimeError("existing policy is invalid: %v", err)
	}
	fmt.Fprintf(c.out, "initialized %s\n", c.dir)
	verb := "wrote starter policy"
	if existed {
		verb = "kept existing policy"
	}
	fmt.Fprintf(c.out, "%s: %s (%d rules, default %s)\n", verb, st.PolicyPath(), len(pol.Rules), pol.Default)
	fmt.Fprintf(c.out, "edit the policy, then let agents call: askfirst submit --action <name>\n")
	return ExitOK
}

func (c *ctx) cmdSubmit(args []string) int {
	fs := c.newFlagSet("submit")
	agent := fs.String("agent", "", "agent identity submitting the action")
	action := fs.String("action", "", "action name, e.g. payments.refund")
	paramsJSON := fs.String("params", "", "parameters as a JSON object")
	var kvs repeated
	fs.Var(&kvs, "param", "one key=value parameter (repeatable; numbers/booleans/null are typed)")
	ttl := fs.Duration("ttl", 0, "expire the request if still undecided after this long")
	wait := fs.Bool("wait", false, "block until a human decides (bounded by --timeout)")
	timeout := fs.Duration("timeout", 10*time.Minute, "give up waiting after this long (with --wait)")
	poll := fs.Duration("poll", 250*time.Millisecond, "poll interval while waiting")
	format := fs.String("format", "text", "output format: text or json")
	policyPath := fs.String("policy", "", "policy file (default <dir>/policy.rules)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() == 1 && *action == "" {
		*action = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return c.usageError("unexpected arguments %v (flags go before the action name)", fs.Args())
	}
	if *action == "" {
		return c.usageError("submit needs --action (or a single positional action name)")
	}
	if *format != "text" && *format != "json" {
		return c.usageError("unknown --format %q (want text or json)", *format)
	}
	params, err := buildParams(*paramsJSON, kvs)
	if err != nil {
		return c.usageError("%v", err)
	}
	pol, err := c.loadPolicy(*policyPath)
	if err != nil {
		return c.runtimeError("%v", err)
	}
	st := c.store()
	req := queue.Request{Agent: *agent, Action: *action, Params: params}
	if *ttl > 0 {
		exp := st.Now().Add(*ttl)
		req.ExpiresAt = &exp
	}
	rec, _, err := inbox.Submit(st, pol, &req)
	if err != nil {
		return c.runtimeError("submit failed: %v", err)
	}
	waited := false
	if *wait && rec.Status == queue.StatusPending {
		waited = true
		rec, err = st.Wait(rec.ID, *timeout, *poll)
		if err != nil {
			return c.runtimeError("wait failed: %v", err)
		}
	}
	if *format == "json" {
		if err := printJSON(c.out, rec); err != nil {
			return c.runtimeError("%v", err)
		}
	} else {
		renderSubmit(c.out, rec, waited, *timeout)
	}
	return exitFor(rec.Status)
}

func (c *ctx) cmdList(args []string) int {
	fs := c.newFlagSet("list")
	status := fs.String("status", "pending", "pending, approved, denied, expired, or all")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *format != "text" && *format != "json" {
		return c.usageError("unknown --format %q (want text or json)", *format)
	}
	filter := queue.Status(*status)
	if *status == "all" {
		filter = ""
	} else if !queue.ValidFilter(filter) {
		return c.usageError("unknown --status %q (want pending, approved, denied, expired, or all)", *status)
	}
	list, err := c.store().List(filter)
	if err != nil {
		return c.runtimeError("%v", err)
	}
	if *format == "json" {
		if list == nil {
			list = []queue.Request{}
		}
		if err := printJSON(c.out, list); err != nil {
			return c.runtimeError("%v", err)
		}
		return ExitOK
	}
	renderList(c.out, list, *status)
	return ExitOK
}

func (c *ctx) cmdShow(args []string) int {
	id, rest := takeID(args)
	fs := c.newFlagSet("show")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	if id == "" {
		return c.usageError("show needs a request id")
	}
	rec, err := c.store().Get(id)
	if err != nil {
		return c.runtimeError("%v", err)
	}
	if *format == "json" {
		if err := printJSON(c.out, rec); err != nil {
			return c.runtimeError("%v", err)
		}
		return ExitOK
	}
	renderShow(c.out, rec)
	return ExitOK
}

func (c *ctx) cmdDecide(args []string, approve bool) int {
	verb := "approve"
	if !approve {
		verb = "deny"
	}
	id, rest := takeID(args)
	fs := c.newFlagSet(verb)
	reason := fs.String("reason", "", "note recorded with the decision")
	if err := fs.Parse(rest); err != nil {
		return ExitUsage
	}
	if id == "" {
		return c.usageError("%s needs a request id", verb)
	}
	rec, err := inbox.Decide(c.store(), id, approve, *reason)
	switch {
	case errors.Is(err, queue.ErrExpired):
		fmt.Fprintf(c.err, "askfirst: %v\n", err)
		return ExitPending
	case err != nil:
		return c.runtimeError("%v", err)
	}
	fmt.Fprintf(c.out, "%s %s — %s\n", rec.Status, rec.ID, describeDecision(rec))
	return ExitOK
}

func (c *ctx) cmdLog(args []string) int {
	fs := c.newFlagSet("log")
	format := fs.String("format", "text", "output format: text or json")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	events, err := c.store().Events()
	if err != nil {
		return c.runtimeError("%v", err)
	}
	if *format == "json" {
		if events == nil {
			events = []queue.Event{}
		}
		if err := printJSON(c.out, events); err != nil {
			return c.runtimeError("%v", err)
		}
		return ExitOK
	}
	renderEvents(c.out, events)
	return ExitOK
}

func (c *ctx) cmdPolicy(args []string) int {
	if len(args) == 0 {
		return c.usageError("policy needs a subcommand: check or test")
	}
	switch args[0] {
	case "check":
		return c.cmdPolicyCheck(args[1:])
	case "test":
		return c.cmdPolicyTest(args[1:])
	}
	return c.usageError("unknown policy subcommand %q (want check or test)", args[0])
}

func (c *ctx) cmdPolicyCheck(args []string) int {
	fs := c.newFlagSet("policy check")
	policyPath := fs.String("policy", "", "policy file (default <dir>/policy.rules)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	pol, err := c.loadPolicy(*policyPath)
	if err != nil {
		fmt.Fprintf(c.err, "policy INVALID: %v\n", err)
		return ExitDenied
	}
	fmt.Fprintf(c.out, "policy OK: %d rules, default %s (%s)\n", len(pol.Rules), pol.Default, pol.Source)
	return ExitOK
}

func (c *ctx) cmdPolicyTest(args []string) int {
	fs := c.newFlagSet("policy test")
	agent := fs.String("agent", "", "agent identity to evaluate as")
	action := fs.String("action", "", "action name to evaluate")
	paramsJSON := fs.String("params", "", "parameters as a JSON object")
	var kvs repeated
	fs.Var(&kvs, "param", "one key=value parameter (repeatable)")
	policyPath := fs.String("policy", "", "policy file (default <dir>/policy.rules)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() == 1 && *action == "" {
		*action = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return c.usageError("unexpected arguments %v", fs.Args())
	}
	if *action == "" {
		return c.usageError("policy test needs --action (or a single positional action name)")
	}
	params, err := buildParams(*paramsJSON, kvs)
	if err != nil {
		return c.usageError("%v", err)
	}
	pol, err := c.loadPolicy(*policyPath)
	if err != nil {
		return c.runtimeError("%v", err)
	}
	dec := pol.Evaluate(policy.Input{Agent: *agent, Action: *action, Params: params})
	fmt.Fprintf(c.out, "decision: %s — %s\n", dec.Effect, dec.Actor())
	if dec.Rule != nil {
		fmt.Fprintf(c.out, "rule:     %s\n", dec.Rule.Text)
	}
	if dec.Reason != "" {
		fmt.Fprintf(c.out, "reason:   %s\n", dec.Reason)
	}
	switch dec.Effect {
	case policy.Allow:
		return ExitOK
	case policy.Deny:
		return ExitDenied
	}
	return ExitPending
}

func (c *ctx) cmdServe(args []string) int {
	fs := c.newFlagSet("serve")
	addr := fs.String("addr", "127.0.0.1:2750", "listen address")
	token := fs.String("token", "", "require this bearer token on /v1 routes")
	policyPath := fs.String("policy", "", "policy file (default <dir>/policy.rules)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		return c.usageError("bad --addr %q: %v", *addr, err)
	}
	if !isLoopbackHost(host) && *token == "" {
		return c.usageError("refusing to listen on non-loopback %q without --token", *addr)
	}
	pol, err := c.loadPolicy(*policyPath)
	if err != nil {
		return c.runtimeError("%v", err)
	}
	st := c.store()
	if err := st.Init(policy.Starter); err != nil { // policy exists; this only ensures requests/
		return c.runtimeError("%v", err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return c.runtimeError("listen failed: %v", err)
	}
	srv := &httpapi.Server{Store: st, Policy: pol, Token: *token, Version: version.Version}
	fmt.Fprintf(c.out, "askfirst %s — approval inbox listening on http://%s\n", version.Version, ln.Addr())
	fmt.Fprintf(c.out, "policy: %s (%d rules, default %s)\n", pol.Source, len(pol.Rules), pol.Default)
	if err := http.Serve(ln, srv.Handler()); err != nil {
		return c.runtimeError("serve failed: %v", err)
	}
	return ExitOK
}

// isLoopbackHost accepts "localhost" and any literal loopback IP; serving
// anywhere else requires an explicit --token so the inbox is never open
// to a network by accident.
func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// repeated collects repeatable string flags like --param.
type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ", ") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

// buildParams merges --params JSON with --param key=value pairs (the
// pairs win). Values are coerced to mirror JSON typing so policies can
// compare them numerically.
func buildParams(paramsJSON string, kvs []string) (map[string]any, error) {
	out := map[string]any{}
	if paramsJSON != "" {
		if err := json.Unmarshal([]byte(paramsJSON), &out); err != nil {
			return nil, fmt.Errorf("--params must be a JSON object: %v", err)
		}
	}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return nil, fmt.Errorf("--param wants key=value, got %q", kv)
		}
		out[k] = coerce(v)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// coerce types a --param value the way JSON would: numbers, booleans, and
// null become typed values; everything else stays a string.
func coerce(v string) any {
	switch v {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	return v
}
