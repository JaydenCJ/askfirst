package policy

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Starter is the policy written by `askfirst init`. It is a working
// example, not a recommendation — teams are expected to replace it.
const Starter = `# askfirst policy — evaluated top to bottom, first match wins.
# Effects: allow (auto-approve), deny (auto-reject), ask (queue for a human).
# Matchers: action:<glob>  agent:<glob>  and param tests (= != ~ < >).
# Full reference: docs/policy.md in the askfirst repository.

# Read-only actions are safe to approve automatically.
allow action:fs.read
allow action:search.*

# Destructive shell commands never run, no matter which agent asks.
deny action:shell.exec command~"rm\s+-rf" reason:"destructive command"

# Small refunds are routine; large ones deserve a human decision.
allow action:payments.refund amount<50
ask action:payments.refund reason:"refund above the auto-approve limit"

# Everything unmatched waits in the inbox for review.
default ask
`

// reserved words that name rule parts rather than request params.
var reservedKeys = map[string]bool{"action": true, "agent": true, "reason": true}

var keyRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+`)

// ParseFile reads and parses a policy file from disk.
func ParseFile(path string) (*Policy, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(string(data), path)
}

// Parse parses policy text. source is used in error messages and in
// Policy.Source (pass "<inline>" for in-memory policies).
func Parse(text, source string) (*Policy, error) {
	p := &Policy{Default: Ask, Source: source}
	for n, raw := range strings.Split(text, "\n") {
		line := n + 1
		toks, clean, err := splitTokens(raw)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", source, line, err)
		}
		if len(toks) == 0 {
			continue
		}
		if toks[0] == "default" {
			if err := p.setDefault(toks, line); err != nil {
				return nil, fmt.Errorf("%s:%d: %v", source, line, err)
			}
			continue
		}
		rule, err := parseRule(toks, line, clean)
		if err != nil {
			return nil, fmt.Errorf("%s:%d: %v", source, line, err)
		}
		p.Rules = append(p.Rules, rule)
	}
	return p, nil
}

func (p *Policy) setDefault(toks []string, line int) error {
	if p.DefaultLine != 0 {
		return fmt.Errorf("duplicate default directive (first on line %d)", p.DefaultLine)
	}
	if len(toks) != 2 {
		return fmt.Errorf("default takes exactly one effect: default allow|deny|ask")
	}
	eff, ok := parseEffect(toks[1])
	if !ok {
		return fmt.Errorf("unknown default effect %q (want allow, deny, or ask)", toks[1])
	}
	p.Default = eff
	p.DefaultLine = line
	return nil
}

func parseRule(toks []string, line int, clean string) (Rule, error) {
	eff, ok := parseEffect(toks[0])
	if !ok {
		return Rule{}, fmt.Errorf("unknown effect %q (want allow, deny, ask, or default)", toks[0])
	}
	rule := Rule{Effect: eff, Line: line, Text: clean}
	for _, tok := range toks[1:] {
		m, reason, err := parseMatcher(tok)
		if err != nil {
			return Rule{}, err
		}
		if reason != "" {
			if rule.Reason != "" {
				return Rule{}, fmt.Errorf("duplicate reason annotation")
			}
			rule.Reason = reason
			continue
		}
		rule.Matchers = append(rule.Matchers, m)
	}
	if len(rule.Matchers) == 0 {
		return Rule{}, fmt.Errorf("rule has no matchers; use action:* to match every action")
	}
	return rule, nil
}

func parseEffect(s string) (Effect, bool) {
	switch Effect(s) {
	case Allow, Deny, Ask:
		return Effect(s), true
	}
	return "", false
}

// parseMatcher turns one token into a Matcher, or returns the reason text
// for reason:"…" annotations.
func parseMatcher(tok string) (Matcher, string, error) {
	key := keyRe.FindString(tok)
	if key == "" {
		return Matcher{}, "", fmt.Errorf("bad matcher %q: expected key<op>value", tok)
	}
	rest := tok[len(key):]
	if rest == "" {
		return Matcher{}, "", fmt.Errorf("bad matcher %q: missing operator (: = != ~ < >)", tok)
	}
	if strings.HasPrefix(rest, ":") {
		val := rest[1:]
		if val == "" {
			return Matcher{}, "", fmt.Errorf("bad matcher %q: empty value", tok)
		}
		switch key {
		case "action", "agent":
			return Matcher{Kind: key, Value: val}, "", nil
		case "reason":
			return Matcher{}, val, nil
		}
		return Matcher{}, "", fmt.Errorf("unknown keyword %q (param matchers use = != ~ < >)", key+":")
	}
	if reservedKeys[key] {
		return Matcher{}, "", fmt.Errorf("%s matcher is written %s:<value>", key, key)
	}
	var op Op
	var val string
	switch {
	case strings.HasPrefix(rest, "!="):
		op, val = OpNe, rest[2:]
	case strings.HasPrefix(rest, "="):
		op, val = OpEq, rest[1:]
	case strings.HasPrefix(rest, "~"):
		op, val = OpRe, rest[1:]
	case strings.HasPrefix(rest, "<"):
		op, val = OpLt, rest[1:]
	case strings.HasPrefix(rest, ">"):
		op, val = OpGt, rest[1:]
	default:
		return Matcher{}, "", fmt.Errorf("bad matcher %q: unknown operator (use : = != ~ < >)", tok)
	}
	if val == "" {
		return Matcher{}, "", fmt.Errorf("bad matcher %q: empty value", tok)
	}
	m := Matcher{Kind: KindParam, Key: key, Op: op, Value: val}
	switch op {
	case OpRe:
		re, err := regexp.Compile(val)
		if err != nil {
			return Matcher{}, "", fmt.Errorf("bad regex in %q: %v", tok, err)
		}
		m.re = re
	case OpLt, OpGt:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return Matcher{}, "", fmt.Errorf("bad matcher %q: %q is not a number", tok, val)
		}
		m.num = f
	}
	return m, "", nil
}

// splitTokens splits a rule line into whitespace-separated tokens while
// honoring double-quoted spans (which may sit anywhere inside a token,
// e.g. command~"rm -rf" or reason:"needs review"). Inside quotes, \" and
// \\ are unescaped; every other backslash passes through untouched so
// regex escapes like \s survive. A '#' outside quotes starts a comment.
// The second return value is the trimmed line with any comment removed,
// used verbatim as the rule text in explanations.
func splitTokens(raw string) ([]string, string, error) {
	var (
		toks    []string
		cur     strings.Builder
		inQuote bool
		started bool
	)
	end := len(raw)
scan:
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		switch {
		case inQuote && c == '\\' && i+1 < len(raw):
			next := raw[i+1]
			if next == '"' || next == '\\' {
				cur.WriteByte(next)
				i++
			} else {
				cur.WriteByte(c)
			}
		case c == '"':
			inQuote = !inQuote
			started = true
		case !inQuote && c == '#':
			end = i
			break scan
		case !inQuote && (c == ' ' || c == '\t'):
			if started {
				toks = append(toks, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if inQuote {
		return nil, "", fmt.Errorf("unterminated quote")
	}
	if started {
		toks = append(toks, cur.String())
	}
	return toks, strings.TrimSpace(raw[:end]), nil
}
