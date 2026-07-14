// Tests for policy evaluation. The evaluator decides which agent actions
// run without a human, so the failure-mode cases matter most: missing
// keys, type mismatches, and composite values must all fail closed and
// fall through to the (human-reviewed) default.
package policy

import (
	"testing"
)

func evalPolicy(t *testing.T, text string, in Input) Decision {
	t.Helper()
	return mustParse(t, text).Evaluate(in)
}

func refund(amount any) Input {
	return Input{Action: "payments.refund", Params: map[string]any{"amount": amount}}
}

func TestFirstMatchWins(t *testing.T) {
	// Both rules match; the earlier one must decide.
	d := evalPolicy(t, "deny action:payments.refund\nallow action:payments.*\n", refund(10.0))
	if d.Effect != Deny || d.Rule.Line != 1 {
		t.Fatalf("got %s from line %d, want deny from line 1", d.Effect, d.Rule.Line)
	}
}

func TestDefaultAskWhenNothingMatches(t *testing.T) {
	d := evalPolicy(t, "allow action:fs.read\n", Input{Action: "fs.write"})
	if d.Effect != Ask || !d.Default || d.Rule != nil {
		t.Fatalf("unmatched action should fall to built-in ask, got %+v", d)
	}
}

func TestExplicitDefaultDeny(t *testing.T) {
	d := evalPolicy(t, "allow action:fs.read\ndefault deny\n", Input{Action: "fs.write"})
	if d.Effect != Deny || !d.Default {
		t.Fatalf("got %+v, want default deny", d)
	}
}

func TestDecisionActorNaming(t *testing.T) {
	d := evalPolicy(t, "allow action:a\n", Input{Action: "a"})
	if d.Actor() != "policy (line 1)" {
		t.Fatalf("actor = %q", d.Actor())
	}
	d = evalPolicy(t, "allow action:a\n", Input{Action: "b"})
	if d.Actor() != "policy (default)" {
		t.Fatalf("default actor = %q", d.Actor())
	}
}

func TestActionGlobs(t *testing.T) {
	cases := []struct {
		pattern, action string
		want            bool
	}{
		{"payments.refund", "payments.refund", true},
		{"payments.refund", "payments.refunds", false},
		{"search.*", "search.web", true},
		{"search.*", "searchweb", false}, // the dot is literal
		{"*", "anything.at.all", true},
		{"fs.re?d", "fs.read", true},
		{"fs.re?d", "fs.rd", false}, // ? is exactly one character
	}
	for _, c := range cases {
		if got := Glob(c.pattern, c.action); got != c.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", c.pattern, c.action, got, c.want)
		}
	}
	// Edge cases: empty input, backtracking, and full consumption.
	if !Glob("*", "") {
		t.Error("* should match the empty string")
	}
	if !Glob("*a*b", "xxaYYaZZb") {
		t.Error("backtracking over repeated segments failed")
	}
	if Glob("a*b", "acb-tail") {
		t.Error("pattern must consume the whole string")
	}
}

func TestAgentGlobMatching(t *testing.T) {
	text := "allow action:* agent:prod-*\ndefault deny\n"
	d := evalPolicy(t, text, Input{Action: "x", Agent: "prod-deployer"})
	if d.Effect != Allow {
		t.Fatalf("prod agent should match, got %s", d.Effect)
	}
	d = evalPolicy(t, text, Input{Action: "x", Agent: "staging-deployer"})
	if d.Effect != Deny {
		t.Fatalf("staging agent should miss, got %s", d.Effect)
	}
}

func TestAgentPatternDoesNotMatchEmptyAgent(t *testing.T) {
	// A request that omits its agent identity must not slip through an
	// agent-scoped allow rule.
	d := evalPolicy(t, "allow action:* agent:prod-*\n", Input{Action: "x"})
	if d.Effect != Ask {
		t.Fatalf("anonymous request matched agent rule: %s", d.Effect)
	}
}

func TestParamStringEquality(t *testing.T) {
	text := "allow action:db.query mode=readonly\n"
	if d := evalPolicy(t, text, Input{Action: "db.query", Params: map[string]any{"mode": "readonly"}}); d.Effect != Allow {
		t.Fatalf("exact string should match, got %s", d.Effect)
	}
	if d := evalPolicy(t, text, Input{Action: "db.query", Params: map[string]any{"mode": "READONLY"}}); d.Effect != Ask {
		t.Fatalf("string equality must be case-sensitive, got %s", d.Effect)
	}
}

func TestParamNumberEquality(t *testing.T) {
	text := "allow action:x replicas=3\n"
	if d := evalPolicy(t, text, Input{Action: "x", Params: map[string]any{"replicas": 3.0}}); d.Effect != Allow {
		t.Fatalf("JSON number 3 should equal literal 3, got %s", d.Effect)
	}
	if d := evalPolicy(t, text, Input{Action: "x", Params: map[string]any{"replicas": 3.5}}); d.Effect != Ask {
		t.Fatalf("3.5 must not equal 3, got %s", d.Effect)
	}
}

func TestParamBoolAndNullEquality(t *testing.T) {
	text := "allow action:x dry_run=true\ndeny action:x target=null\n"
	if d := evalPolicy(t, text, Input{Action: "x", Params: map[string]any{"dry_run": true}}); d.Effect != Allow {
		t.Fatalf("bool true should match, got %s", d.Effect)
	}
	// Equality compares the value's canonical text, so the string "true"
	// also matches — the claim is identical either way. Range operators
	// (< >) stay strictly numeric; that is where widening would hurt.
	if d := evalPolicy(t, text, Input{Action: "x", Params: map[string]any{"dry_run": "true"}}); d.Effect != Allow {
		t.Fatalf(`string "true" should match via textual equality, got %s`, d.Effect)
	}
	if d := evalPolicy(t, text, Input{Action: "x", Params: map[string]any{"target": nil}}); d.Effect != Deny {
		t.Fatalf("JSON null should match literal null, got %s", d.Effect)
	}
}

func TestNotEqualMatchesOnlyWhenKeyPresent(t *testing.T) {
	text := "deny action:x env!=sandbox\n"
	if d := evalPolicy(t, text, Input{Action: "x", Params: map[string]any{"env": "prod"}}); d.Effect != Deny {
		t.Fatalf("env=prod should satisfy env!=sandbox, got %s", d.Effect)
	}
	// Missing key: != must NOT match, otherwise omitting a param would
	// be a way to satisfy (and abuse) inequality rules.
	if d := evalPolicy(t, text, Input{Action: "x"}); d.Effect != Ask {
		t.Fatalf("missing key satisfied !=, got %s", d.Effect)
	}
}

func TestRegexSearchOnScalars(t *testing.T) {
	text := `deny action:shell.exec command~"rm\s+-rf"` + "\n"
	in := Input{Action: "shell.exec", Params: map[string]any{"command": "cd /srv && rm  -rf cache"}}
	if d := evalPolicy(t, text, in); d.Effect != Deny {
		t.Fatalf("regex should search anywhere in the value, got %s", d.Effect)
	}
	// Numbers and bools regex-match via their canonical text.
	text = `allow action:x replicas~"^12$" force~"^false$"` + "\n"
	in = Input{Action: "x", Params: map[string]any{"replicas": 12.0, "force": false}}
	if d := evalPolicy(t, text, in); d.Effect != Allow {
		t.Fatalf("scalar coercion for regex failed, got %s", d.Effect)
	}
}

func TestRegexFailsClosedOnComposites(t *testing.T) {
	// Arrays and objects have no canonical single-line text; matching
	// them loosely would make rules depend on encoder details.
	text := `allow action:x files~"safe"` + "\n"
	in := Input{Action: "x", Params: map[string]any{"files": []any{"safe.txt"}}}
	if d := evalPolicy(t, text, in); d.Effect != Ask {
		t.Fatalf("array value matched a regex rule, got %s", d.Effect)
	}
}

func TestNumericComparisons(t *testing.T) {
	text := "allow action:payments.refund amount<50\ndeny action:payments.refund amount>1000\n"
	if d := evalPolicy(t, text, refund(49.99)); d.Effect != Allow {
		t.Fatalf("49.99 < 50 should allow, got %s", d.Effect)
	}
	if d := evalPolicy(t, text, refund(50.0)); d.Effect != Ask {
		t.Fatalf("boundary value 50 must not satisfy <50, got %s", d.Effect)
	}
	if d := evalPolicy(t, text, refund(1500.0)); d.Effect != Deny {
		t.Fatalf("1500 > 1000 should deny, got %s", d.Effect)
	}
}

func TestNumericComparisonIgnoresNumericStrings(t *testing.T) {
	// "49" (a string) must not be auto-approved by amount<50: silently
	// coercing strings would let a client's sloppy typing widen a rule.
	d := evalPolicy(t, "allow action:payments.refund amount<50\n", refund("49"))
	if d.Effect != Ask {
		t.Fatalf("numeric string satisfied a numeric comparison, got %s", d.Effect)
	}
}

func TestMissingParamNeverMatches(t *testing.T) {
	d := evalPolicy(t, "allow action:payments.refund amount<50\n",
		Input{Action: "payments.refund"})
	if d.Effect != Ask {
		t.Fatalf("request without amount was auto-approved: %s", d.Effect)
	}
}

func TestDottedPathLookup(t *testing.T) {
	text := "allow action:x user.role=admin\n"
	in := Input{Action: "x", Params: map[string]any{"user": map[string]any{"role": "admin"}}}
	if d := evalPolicy(t, text, in); d.Effect != Allow {
		t.Fatalf("nested lookup failed, got %s", d.Effect)
	}
	// Walking through a non-object must fail closed, not panic.
	in = Input{Action: "x", Params: map[string]any{"user": "admin"}}
	if d := evalPolicy(t, text, in); d.Effect != Ask {
		t.Fatalf("path through scalar matched, got %s", d.Effect)
	}
}

func TestAllMatchersMustHold(t *testing.T) {
	text := "allow action:deploy.run agent:ci-* env=staging\n"
	in := Input{Action: "deploy.run", Agent: "ci-runner", Params: map[string]any{"env": "prod"}}
	if d := evalPolicy(t, text, in); d.Effect != Ask {
		t.Fatalf("rule matched with one failing matcher, got %s", d.Effect)
	}
}

func TestReasonSurfacedInDecision(t *testing.T) {
	d := evalPolicy(t, `deny action:x reason:"too risky"`+"\n", Input{Action: "x"})
	if d.Reason != "too risky" {
		t.Fatalf("reason = %q", d.Reason)
	}
}
