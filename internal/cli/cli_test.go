// In-process integration tests for the CLI: every case calls Run exactly
// as main does and asserts on output plus the exit-code contract
// (0 approved · 1 denied · 2 usage · 3 runtime · 4 pending) that shell
// wrappers build on. No subprocesses, no network, no wall-clock sleeps
// beyond bounded sub-second waits.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/askfirst/internal/queue"
)

// run invokes the CLI in-process and captures everything.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// initInbox creates a fresh inbox directory with the starter policy.
func initInbox(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "inbox")
	code, _, stderr := run(t, "--dir", dir, "init")
	if code != ExitOK {
		t.Fatalf("init failed (%d): %s", code, stderr)
	}
	return dir
}

func writePolicy(t *testing.T, dir, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "policy.rules"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// submitJSON submits and decodes the resulting record.
func submitJSON(t *testing.T, dir string, extra ...string) (int, queue.Request) {
	t.Helper()
	args := append([]string{"--dir", dir, "submit", "--format", "json"}, extra...)
	code, out, _ := run(t, args...)
	var rec queue.Request
	if err := json.Unmarshal([]byte(out), &rec); err != nil {
		t.Fatalf("submit output is not a JSON record: %q", out)
	}
	return code, rec
}

func TestVersionCommandAndFlag(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--version"}} {
		code, out, _ := run(t, args...)
		if code != ExitOK || strings.TrimSpace(out) != "askfirst 0.1.0" {
			t.Fatalf("%v -> %d %q", args, code, out)
		}
	}
}

func TestHelpAndUnknownCommand(t *testing.T) {
	code, out, _ := run(t, "--help")
	if code != ExitOK || !strings.Contains(out, "approval queue") {
		t.Fatalf("--help: %d %q", code, out)
	}
	code, _, stderr := run(t, "--dir", t.TempDir(), "frobnicate")
	if code != ExitUsage || !strings.Contains(stderr, "unknown command") {
		t.Fatalf("unknown command: %d %q", code, stderr)
	}
}

func TestInitWritesStarterPolicyAndKeepsEdits(t *testing.T) {
	dir := initInbox(t)
	data, err := os.ReadFile(filepath.Join(dir, "policy.rules"))
	if err != nil || !strings.Contains(string(data), "default ask") {
		t.Fatalf("starter policy missing: %v", err)
	}
	// Re-running init must never clobber an operator's edited policy.
	writePolicy(t, dir, "default deny\n")
	code, out, _ := run(t, "--dir", dir, "init")
	if code != ExitOK || !strings.Contains(out, "kept existing policy") {
		t.Fatalf("re-init: %d %q", code, out)
	}
	data, _ = os.ReadFile(filepath.Join(dir, "policy.rules"))
	if string(data) != "default deny\n" {
		t.Fatalf("re-init clobbered policy: %q", data)
	}
}

func TestSubmitAutoApprovedExitsZero(t *testing.T) {
	dir := initInbox(t)
	code, out, _ := run(t, "--dir", dir, "submit", "--agent", "bot", "--action", "fs.read")
	if code != ExitOK || !strings.HasPrefix(out, "approved ") {
		t.Fatalf("auto-approve: %d %q", code, out)
	}
	if !strings.Contains(out, "policy (line") {
		t.Fatalf("output should name the deciding rule: %q", out)
	}
	// A single positional argument is accepted as the action name.
	code, out, _ = run(t, "--dir", dir, "submit", "fs.read")
	if code != ExitOK || !strings.HasPrefix(out, "approved ") {
		t.Fatalf("positional action: %d %q", code, out)
	}
}

func TestSubmitAutoDeniedExitsOne(t *testing.T) {
	dir := initInbox(t)
	code, out, _ := run(t, "--dir", dir, "submit", "--action", "shell.exec",
		"--param", "command=rm -rf /srv/data")
	if code != ExitDenied || !strings.HasPrefix(out, "denied ") {
		t.Fatalf("auto-deny: %d %q", code, out)
	}
}

func TestSubmitPendingExitsFour(t *testing.T) {
	dir := initInbox(t)
	code, out, _ := run(t, "--dir", dir, "submit", "--action", "payments.refund",
		"--param", "amount=1200")
	if code != ExitPending || !strings.HasPrefix(out, "pending ") {
		t.Fatalf("ask path: %d %q", code, out)
	}
	if !strings.Contains(out, "askfirst approve") {
		t.Fatalf("pending output should tell the human what to run: %q", out)
	}
}

func TestSubmitRequiresAction(t *testing.T) {
	dir := initInbox(t)
	code, _, stderr := run(t, "--dir", dir, "submit", "--agent", "bot")
	if code != ExitUsage || !strings.Contains(stderr, "--action") {
		t.Fatalf("missing action: %d %q", code, stderr)
	}
}

func TestSubmitParamTypingReachesPolicy(t *testing.T) {
	// --param amount=25 must arrive as a JSON number, or the starter
	// policy's amount<50 rule could never auto-approve anything.
	dir := initInbox(t)
	code, rec := submitJSON(t, dir, "--action", "payments.refund", "--param", "amount=25")
	if code != ExitOK || rec.Status != queue.StatusApproved {
		t.Fatalf("numeric param missed amount<50: %d %+v", code, rec)
	}
	if rec.Params["amount"] != 25.0 {
		t.Fatalf("amount stored as %T %v, want number", rec.Params["amount"], rec.Params["amount"])
	}
}

func TestSubmitParamsJSONFlag(t *testing.T) {
	dir := initInbox(t)
	code, rec := submitJSON(t, dir, "--action", "payments.refund",
		"--params", `{"amount": 10, "customer": {"tier": "gold"}}`)
	if code != ExitOK || rec.Params["customer"].(map[string]any)["tier"] != "gold" {
		t.Fatalf("nested params lost: %d %+v", code, rec)
	}
	code, _, stderr := run(t, "--dir", dir, "submit", "--action", "x", "--params", "[1,2]")
	if code != ExitUsage || !strings.Contains(stderr, "JSON object") {
		t.Fatalf("non-object params: %d %q", code, stderr)
	}
}

func TestListShowsPendingTable(t *testing.T) {
	dir := initInbox(t)
	run(t, "--dir", dir, "submit", "--agent", "billing-bot", "--action", "payments.refund",
		"--param", "amount=1200")
	run(t, "--dir", dir, "submit", "--action", "fs.read") // auto-approved: not in inbox
	code, out, _ := run(t, "--dir", dir, "list")
	if code != ExitOK {
		t.Fatalf("list failed: %d", code)
	}
	if !strings.Contains(out, "billing-bot") || !strings.Contains(out, "payments.refund") {
		t.Fatalf("pending row missing: %q", out)
	}
	if !strings.Contains(out, "1 pending request") {
		t.Fatalf("count line missing: %q", out)
	}
	if strings.Contains(out, "fs.read") {
		t.Fatalf("auto-approved request leaked into the pending inbox: %q", out)
	}
}

func TestListJSONAndStatusFilters(t *testing.T) {
	dir := initInbox(t)
	run(t, "--dir", dir, "submit", "--action", "fs.read")
	code, out, _ := run(t, "--dir", dir, "list", "--status", "approved", "--format", "json")
	if code != ExitOK {
		t.Fatalf("list --status approved: %d", code)
	}
	var list []queue.Request
	if err := json.Unmarshal([]byte(out), &list); err != nil || len(list) != 1 {
		t.Fatalf("json list: %v %q", err, out)
	}
	code, _, stderr := run(t, "--dir", dir, "list", "--status", "bogus")
	if code != ExitUsage || !strings.Contains(stderr, "bogus") {
		t.Fatalf("bogus filter: %d %q", code, stderr)
	}
}

func TestShowDisplaysFullDecision(t *testing.T) {
	dir := initInbox(t)
	_, rec := submitJSON(t, dir, "--action", "payments.refund", "--param", "amount=7")
	code, out, _ := run(t, "--dir", dir, "show", rec.ID)
	if code != ExitOK {
		t.Fatalf("show: %d", code)
	}
	for _, want := range []string{"status:   approved", "action:   payments.refund", "rule:     allow action:payments.refund amount<50"} {
		if !strings.Contains(out, want) {
			t.Fatalf("show output missing %q:\n%s", want, out)
		}
	}
}

func TestApproveFlowUpdatesRequestAndAuditLog(t *testing.T) {
	dir := initInbox(t)
	_, rec := submitJSON(t, dir, "--action", "payments.refund", "--param", "amount=800")
	code, out, _ := run(t, "--dir", dir, "approve", rec.ID, "--reason", "verified with finance")
	if code != ExitOK || !strings.HasPrefix(out, "approved ") || !strings.Contains(out, "human") {
		t.Fatalf("approve: %d %q", code, out)
	}
	code, out, _ = run(t, "--dir", dir, "log")
	if code != ExitOK {
		t.Fatalf("log: %d", code)
	}
	for _, want := range []string{"submitted", "approved", "verified with finance"} {
		if !strings.Contains(out, want) {
			t.Fatalf("audit trail missing %q:\n%s", want, out)
		}
	}
}

func TestDenyRecordsReason(t *testing.T) {
	dir := initInbox(t)
	_, rec := submitJSON(t, dir, "--action", "mail.send")
	code, out, _ := run(t, "--dir", dir, "deny", rec.ID, "--reason", "wrong recipient list")
	if code != ExitOK || !strings.HasPrefix(out, "denied ") {
		t.Fatalf("deny: %d %q", code, out)
	}
	_, shown, _ := run(t, "--dir", dir, "show", rec.ID)
	if !strings.Contains(shown, "wrong recipient list") {
		t.Fatalf("reason not persisted:\n%s", shown)
	}
}

func TestDecisionErrorPaths(t *testing.T) {
	dir := initInbox(t)
	code, _, stderr := run(t, "--dir", dir, "approve", "af-nope")
	if code != ExitRuntime || !strings.Contains(stderr, "no such request") {
		t.Fatalf("unknown id: %d %q", code, stderr)
	}
	// Re-deciding must fail loudly instead of flipping the record.
	_, rec := submitJSON(t, dir, "--action", "mail.send")
	run(t, "--dir", dir, "approve", rec.ID)
	code, _, stderr = run(t, "--dir", dir, "deny", rec.ID)
	if code != ExitRuntime || !strings.Contains(stderr, "already decided") {
		t.Fatalf("double decision: %d %q", code, stderr)
	}
}

func TestSubmitWithoutInitExplainsFix(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-initialized")
	code, _, stderr := run(t, "--dir", dir, "submit", "--action", "fs.read")
	if code != ExitRuntime || !strings.Contains(stderr, "askfirst init") {
		t.Fatalf("uninitialized dir: %d %q", code, stderr)
	}
}

func TestPolicyCheckValidAndInvalid(t *testing.T) {
	dir := initInbox(t)
	code, out, _ := run(t, "--dir", dir, "policy", "check")
	if code != ExitOK || !strings.Contains(out, "policy OK: 5 rules, default ask") {
		t.Fatalf("check valid: %d %q", code, out)
	}
	writePolicy(t, dir, "allow action:a\npermit action:b\n")
	code, _, stderr := run(t, "--dir", dir, "policy", "check")
	if code != ExitDenied || !strings.Contains(stderr, "policy INVALID") || !strings.Contains(stderr, ":2:") {
		t.Fatalf("check invalid: %d %q", code, stderr)
	}
}

func TestPolicyTestExplainsDecisionAndExitCode(t *testing.T) {
	dir := initInbox(t)
	code, out, _ := run(t, "--dir", dir, "policy", "test",
		"--action", "shell.exec", "--param", "command=rm -rf /")
	if code != ExitDenied || !strings.Contains(out, "decision: deny — policy (line 11)") {
		t.Fatalf("policy test deny: %d %q", code, out)
	}
	if !strings.Contains(out, "rule:") {
		t.Fatalf("dry run should print the matched rule: %q", out)
	}
	code, out, _ = run(t, "--dir", dir, "policy", "test", "--action", "totally.novel")
	if code != ExitPending || !strings.Contains(out, "policy (default)") {
		t.Fatalf("policy test default: %d %q", code, out)
	}
}

func TestSubmitWaitTimesOutAsPending(t *testing.T) {
	// A bounded 1 ms timeout: deterministic outcome (nobody will decide),
	// no meaningful wall-clock dependency.
	dir := initInbox(t)
	code, out, _ := run(t, "--dir", dir, "submit", "--action", "deploy.run",
		"--wait", "--timeout", "1ms", "--poll", "1ms")
	if code != ExitPending || !strings.Contains(out, "still in the inbox") {
		t.Fatalf("wait timeout: %d %q", code, out)
	}
}

func TestSubmitTTLExpiresRequest(t *testing.T) {
	// A 1 ms TTL is already past by the first --wait poll, so the submit
	// resolves as expired without any decision.
	dir := initInbox(t)
	code, rec := submitJSON(t, dir, "--action", "deploy.run", "--ttl", "1ms",
		"--wait", "--timeout", "50ms", "--poll", "1ms")
	if code != ExitPending || rec.Status != queue.StatusExpired {
		t.Fatalf("ttl expiry: %d %+v", code, rec)
	}
}

func TestServeRefusesNonLoopbackWithoutToken(t *testing.T) {
	dir := initInbox(t)
	code, _, stderr := run(t, "--dir", dir, "serve", "--addr", "0.0.0.0:0")
	if code != ExitUsage || !strings.Contains(stderr, "non-loopback") {
		t.Fatalf("non-loopback bind: %d %q", code, stderr)
	}
	code, _, stderr = run(t, "--dir", dir, "serve", "--addr", "not-an-addr")
	if code != ExitUsage {
		t.Fatalf("bad addr: %d %q", code, stderr)
	}
}

func TestEnvVarSetsInboxDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "via-env")
	t.Setenv("ASKFIRST_DIR", dir)
	code, out, _ := run(t, "init")
	if code != ExitOK || !strings.Contains(out, dir) {
		t.Fatalf("ASKFIRST_DIR ignored: %d %q", code, out)
	}
	// An explicit --dir must still win over the environment.
	other := filepath.Join(t.TempDir(), "via-flag")
	code, out, _ = run(t, "--dir", other, "init")
	if code != ExitOK || !strings.Contains(out, other) {
		t.Fatalf("--dir did not override env: %d %q", code, out)
	}
}
