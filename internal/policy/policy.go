// Package policy implements the askfirst rule language: a small,
// line-oriented format that classifies agent action requests into
// allow (auto-approve), deny (auto-reject), or ask (human review).
//
// Evaluation is pure and deterministic: rules are checked top to bottom,
// the first rule whose matchers all hold decides, and an optional
// `default` directive covers everything else (falling back to ask).
// Matchers fail closed — a missing parameter, a type mismatch, or a
// string where a number is required never satisfies a rule, so a
// malformed request can only ever fall through to the default.
package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Effect is the outcome a rule assigns to a matching request.
type Effect string

const (
	Allow Effect = "allow" // auto-approve, no human involved
	Deny  Effect = "deny"  // auto-reject, no human involved
	Ask   Effect = "ask"   // queue for human review
)

// Op is a parameter matcher operator.
type Op int

const (
	OpEq Op = iota // key=value   scalar equality
	OpNe           // key!=value  key present and not equal
	OpRe           // key~regex   RE2 search on the value as a string
	OpLt           // key<n       numeric less-than (JSON numbers only)
	OpGt           // key>n       numeric greater-than (JSON numbers only)
)

// Matcher kinds. Action and agent matchers use globs; param matchers use
// the operators above against a dotted key path into the request params.
const (
	KindAction = "action"
	KindAgent  = "agent"
	KindParam  = "param"
)

// Matcher is one ANDed condition on a rule.
type Matcher struct {
	Kind  string // KindAction, KindAgent, or KindParam
	Key   string // param key path, e.g. "amount" or "user.role"
	Op    Op     // param matchers only
	Value string

	re  *regexp.Regexp // compiled at parse time for OpRe
	num float64        // parsed at parse time for OpLt/OpGt
}

// Rule is one policy line: an effect plus one or more matchers.
type Rule struct {
	Effect   Effect
	Line     int    // 1-based line number in the policy source
	Text     string // trimmed source line, quoted in explanations
	Reason   string // optional reason:"…" annotation
	Matchers []Matcher
}

// Policy is a parsed rule set.
type Policy struct {
	Rules       []Rule
	Default     Effect // applies when no rule matches; Ask unless overridden
	DefaultLine int    // 0 when the built-in default applies
	Source      string // file path, or "<inline>" for in-memory policies
}

// Input is one action request as seen by the evaluator.
type Input struct {
	Agent  string
	Action string
	Params map[string]any
}

// Decision is the evaluation result, with enough context to explain
// exactly which rule (or default) produced it.
type Decision struct {
	Effect  Effect
	Rule    *Rule // nil when the default applied
	Default bool
	Reason  string
}

// Actor names the policy element responsible for a decision; audit log
// entries and decided_by fields use this string verbatim.
func (d Decision) Actor() string {
	if d.Default {
		return "policy (default)"
	}
	return fmt.Sprintf("policy (line %d)", d.Rule.Line)
}

// Evaluate returns the decision for one request: the first matching rule
// wins, otherwise the policy default applies.
func (p *Policy) Evaluate(in Input) Decision {
	for i := range p.Rules {
		r := &p.Rules[i]
		if r.Matches(in) {
			return Decision{Effect: r.Effect, Rule: r, Reason: r.Reason}
		}
	}
	return Decision{Effect: p.Default, Default: true}
}

// Matches reports whether every matcher on the rule holds for the input.
func (r *Rule) Matches(in Input) bool {
	for i := range r.Matchers {
		if !r.Matchers[i].match(in) {
			return false
		}
	}
	return true
}

func (m *Matcher) match(in Input) bool {
	switch m.Kind {
	case KindAction:
		return Glob(m.Value, in.Action)
	case KindAgent:
		return Glob(m.Value, in.Agent)
	}
	v, ok := lookup(in.Params, m.Key)
	if !ok {
		return false // missing keys never match, not even for !=
	}
	switch m.Op {
	case OpEq:
		return scalarEq(v, m.Value)
	case OpNe:
		return !scalarEq(v, m.Value)
	case OpRe:
		s, ok := scalarString(v)
		return ok && m.re.MatchString(s)
	case OpLt:
		f, ok := v.(float64)
		return ok && f < m.num
	case OpGt:
		f, ok := v.(float64)
		return ok && f > m.num
	}
	return false
}

// lookup navigates a dotted path through nested JSON objects. Any
// non-object along the way (including arrays) ends the walk.
func lookup(params map[string]any, path string) (any, bool) {
	var cur any = params
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[part]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// scalarEq compares a decoded JSON value against a rule literal, typed by
// the value's own kind: numbers compare numerically, booleans against
// true/false, null against "null", strings byte-for-byte. Arrays and
// objects never compare equal to anything.
func scalarEq(v any, want string) bool {
	switch x := v.(type) {
	case string:
		return x == want
	case float64:
		f, err := strconv.ParseFloat(want, 64)
		return err == nil && x == f
	case bool:
		return strconv.FormatBool(x) == want
	case nil:
		return want == "null"
	}
	return false
}

// scalarString renders a scalar for regex matching; composite values
// report !ok so regex matchers fail closed on them.
func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}
