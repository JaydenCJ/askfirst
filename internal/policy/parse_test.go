// Tests for the policy parser. The parser is the operator-facing surface
// of askfirst — a rule that parses differently from how it reads is a
// security bug — so error cases get as much attention as happy paths,
// and every error must carry the source line number.
package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, text string) *Policy {
	t.Helper()
	p, err := Parse(text, "<inline>")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	return p
}

func parseErr(t *testing.T, text string) error {
	t.Helper()
	_, err := Parse(text, "<inline>")
	if err == nil {
		t.Fatalf("Parse(%q) unexpectedly succeeded", text)
	}
	return err
}

func TestStarterPolicyParses(t *testing.T) {
	p := mustParse(t, Starter)
	if len(p.Rules) != 5 {
		t.Fatalf("starter policy has %d rules, want 5", len(p.Rules))
	}
	if p.Default != Ask || p.DefaultLine == 0 {
		t.Fatalf("starter default = %s (line %d), want explicit ask", p.Default, p.DefaultLine)
	}
}

func TestCommentsAndBlankLinesAreIgnored(t *testing.T) {
	p := mustParse(t, "# heading\n\n   \nallow action:a.b # trailing note\n")
	if len(p.Rules) != 1 {
		t.Fatalf("got %d rules, want 1", len(p.Rules))
	}
	// The recorded rule text must exclude the trailing comment.
	if p.Rules[0].Text != "allow action:a.b" {
		t.Fatalf("rule text = %q, want comment stripped", p.Rules[0].Text)
	}
	if p.Rules[0].Line != 4 {
		t.Fatalf("rule line = %d, want 4", p.Rules[0].Line)
	}
}

func TestHashInsideQuotesIsLiteral(t *testing.T) {
	p := mustParse(t, `deny action:notes.tag label~"#urgent"`)
	m := p.Rules[0].Matchers[1]
	if m.Value != "#urgent" {
		t.Fatalf("quoted # treated as comment: value = %q", m.Value)
	}
}

func TestUnknownEffectFailsWithLineNumber(t *testing.T) {
	err := parseErr(t, "allow action:a\n\nalow action:b\n")
	if !strings.Contains(err.Error(), "<inline>:3:") {
		t.Fatalf("error %q should name line 3", err)
	}
	if !strings.Contains(err.Error(), `"alow"`) {
		t.Fatalf("error %q should quote the bad effect", err)
	}
}

func TestRuleWithoutMatchersFails(t *testing.T) {
	err := parseErr(t, "allow\n")
	// The error must teach the fix: action:* is the explicit catch-all.
	if !strings.Contains(err.Error(), "action:*") {
		t.Fatalf("error %q should suggest action:*", err)
	}
	// A reason annotation is documentation, not a matcher.
	parseErr(t, `deny reason:"because"`)
}

func TestDuplicateReasonFails(t *testing.T) {
	err := parseErr(t, `deny action:a reason:"one" reason:"two"`)
	if !strings.Contains(err.Error(), "duplicate reason") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestQuotedValuesKeepSpaces(t *testing.T) {
	p := mustParse(t, `ask action:mail.send reason:"needs a second pair of eyes"`)
	if p.Rules[0].Reason != "needs a second pair of eyes" {
		t.Fatalf("reason = %q", p.Rules[0].Reason)
	}
}

func TestQuoteEscapes(t *testing.T) {
	// \" and \\ are unescaped; other backslashes (regex classes like \s)
	// pass through untouched so operators can write plain RE2.
	p := mustParse(t, `deny action:x note~"say \"hi\"" path~"a\\b" cmd~"rm\s+-rf"`)
	ms := p.Rules[0].Matchers
	if ms[1].Value != `say "hi"` {
		t.Fatalf("escaped quote: %q", ms[1].Value)
	}
	if ms[2].Value != `a\b` {
		t.Fatalf("escaped backslash: %q", ms[2].Value)
	}
	if ms[3].Value != `rm\s+-rf` {
		t.Fatalf("regex class should pass through: %q", ms[3].Value)
	}
}

func TestUnterminatedQuoteFails(t *testing.T) {
	err := parseErr(t, `deny action:x note~"oops`)
	if !strings.Contains(err.Error(), "unterminated quote") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnknownOperatorFails(t *testing.T) {
	err := parseErr(t, "allow action:x amount^5")
	if !strings.Contains(err.Error(), "unknown operator") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmptyValuesFail(t *testing.T) {
	for _, line := range []string{"allow action:", "allow amount=", "allow cmd~"} {
		if _, err := Parse(line, "<inline>"); err == nil {
			t.Fatalf("Parse(%q) should fail on empty value", line)
		}
	}
}

func TestBadRegexFailsAtParseTime(t *testing.T) {
	// A regex that only explodes at evaluation time would turn a policy
	// typo into a runtime approval failure; it must be caught up front.
	err := parseErr(t, `deny action:x cmd~"("`)
	if !strings.Contains(err.Error(), "bad regex") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNonNumericComparisonValueFails(t *testing.T) {
	err := parseErr(t, "allow action:x amount<cheap")
	if !strings.Contains(err.Error(), "not a number") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestReservedAndUnknownKeywords(t *testing.T) {
	err := parseErr(t, "allow action=deploy")
	if !strings.Contains(err.Error(), "action:") {
		t.Fatalf("error %q should point at the action:<value> form", err)
	}
	err = parseErr(t, "allow tool:hammer")
	if !strings.Contains(err.Error(), "unknown keyword") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDefaultDirective(t *testing.T) {
	p := mustParse(t, "allow action:a\ndefault deny\n")
	if p.Default != Deny {
		t.Fatalf("default = %s, want deny", p.Default)
	}
	for _, bad := range []string{"default", "default deny fast", "default banana"} {
		if _, err := Parse(bad, "<inline>"); err == nil {
			t.Fatalf("Parse(%q) should fail", bad)
		}
	}
	// Two defaults would make the policy ambiguous; the error names the
	// first one so the operator knows which line to delete.
	err := parseErr(t, "default deny\ndefault ask\n")
	if !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("error %q should name the first default's line", err)
	}
}

func TestParseFileFromDiskAndMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.rules")
	if err := os.WriteFile(path, []byte("allow action:a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := ParseFile(path)
	if err != nil || len(p.Rules) != 1 || p.Source != path {
		t.Fatalf("ParseFile: %v, rules=%d, source=%q", err, len(p.Rules), p.Source)
	}
	if _, err := ParseFile(filepath.Join(t.TempDir(), "missing.rules")); !os.IsNotExist(err) {
		t.Fatalf("missing file should surface os.IsNotExist, got %v", err)
	}
}
