// Tests for the HTTP API. Requests are served in-process through
// httptest recorders — no sockets are opened — so the suite runs
// offline and byte-deterministic. The cases mirror the contract agents
// depend on: auto decisions come back synchronously, ask returns 202,
// and every failure mode has a precise status code.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JaydenCJ/askfirst/internal/policy"
	"github.com/JaydenCJ/askfirst/internal/queue"
)

const testPolicy = `allow action:fs.read
deny action:shell.exec reason:"no raw shell"
allow action:payments.refund amount<50
default ask
`

var base = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

func newServer(t *testing.T, token string) *Server {
	t.Helper()
	st := queue.Open(filepath.Join(t.TempDir(), "inbox"))
	st.Now = func() time.Time { return base }
	n := 0
	st.NewID = func() string { n++; return fmt.Sprintf("af-%d", n) }
	if err := st.Init(testPolicy); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Parse(testPolicy, "<inline>")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Store: st, Policy: pol, Token: token, Version: "0.1.0"}
}

// do runs one request through the handler and decodes the JSON reply.
func do(t *testing.T, s *Server, method, target, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: non-JSON reply %q", method, target, rec.Body.String())
	}
	return rec.Code, out
}

func TestHealthzReportsVersion(t *testing.T) {
	code, out := do(t, newServer(t, ""), "GET", "/healthz", "")
	if code != 200 || out["ok"] != true || out["version"] != "0.1.0" {
		t.Fatalf("healthz = %d %v", code, out)
	}
}

func TestSubmitAutoApproved(t *testing.T) {
	s := newServer(t, "")
	code, out := do(t, s, "POST", "/v1/requests", `{"agent":"bot","action":"fs.read"}`)
	if code != 200 || out["status"] != "approved" {
		t.Fatalf("got %d %v, want synchronous approval", code, out)
	}
	if out["decided_by"] != "policy (line 1)" {
		t.Fatalf("decided_by = %v", out["decided_by"])
	}
	// amount<50 must see the typed JSON number, proving params flow
	// end-to-end from body to evaluator.
	code, out = do(t, s, "POST", "/v1/requests", `{"action":"payments.refund","params":{"amount":25}}`)
	if code != 200 || out["status"] != "approved" {
		t.Fatalf("got %d %v, want auto-approval via amount<50", code, out)
	}
}

func TestSubmitAutoDenied(t *testing.T) {
	s := newServer(t, "")
	code, out := do(t, s, "POST", "/v1/requests", `{"action":"shell.exec"}`)
	if code != 200 || out["status"] != "denied" || out["reason"] != "no raw shell" {
		t.Fatalf("got %d %v, want synchronous denial with reason", code, out)
	}
}

func TestSubmitAskReturns202Pending(t *testing.T) {
	s := newServer(t, "")
	code, out := do(t, s, "POST", "/v1/requests", `{"action":"payments.refund","params":{"amount":900}}`)
	if code != 202 || out["status"] != "pending" {
		t.Fatalf("got %d %v, want 202 pending", code, out)
	}
	if out["id"] == "" {
		t.Fatal("pending reply must carry the id to poll")
	}
}

func TestSubmitValidation(t *testing.T) {
	s := newServer(t, "")
	if code, _ := do(t, s, "POST", "/v1/requests", `{"agent":"bot"}`); code != 400 {
		t.Fatalf("missing action: got %d, want 400", code)
	}
	if code, _ := do(t, s, "POST", "/v1/requests", `{not json`); code != 400 {
		t.Fatalf("bad JSON: got %d, want 400", code)
	}
	if code, _ := do(t, s, "POST", "/v1/requests", `{"action":"x","ttl_seconds":-5}`); code != 400 {
		t.Fatalf("negative ttl: got %d, want 400", code)
	}
}

func TestSubmitTTLSetsExpiry(t *testing.T) {
	s := newServer(t, "")
	_, out := do(t, s, "POST", "/v1/requests", `{"action":"deploy.run","ttl_seconds":600}`)
	want := base.Add(10 * time.Minute).Format(time.RFC3339)
	if got, _ := out["expires_at"].(string); !strings.HasPrefix(got, want[:19]) {
		t.Fatalf("expires_at = %v, want %s", out["expires_at"], want)
	}
}

func TestGetRequestByID(t *testing.T) {
	s := newServer(t, "")
	_, created := do(t, s, "POST", "/v1/requests", `{"action":"deploy.run"}`)
	id := created["id"].(string)
	code, out := do(t, s, "GET", "/v1/requests/"+id, "")
	if code != 200 || out["action"] != "deploy.run" {
		t.Fatalf("get by id = %d %v", code, out)
	}
	if code, _ := do(t, s, "GET", "/v1/requests/af-unknown", ""); code != 404 {
		t.Fatalf("unknown id: got %d, want 404", code)
	}
}

func TestListDefaultsToPendingInbox(t *testing.T) {
	s := newServer(t, "")
	do(t, s, "POST", "/v1/requests", `{"action":"fs.read"}`)    // auto-approved
	do(t, s, "POST", "/v1/requests", `{"action":"deploy.run"}`) // pending
	code, out := do(t, s, "GET", "/v1/requests", "")
	if code != 200 || out["count"] != 1.0 {
		t.Fatalf("default list = %d %v, want the 1 pending request", code, out)
	}
	code, out = do(t, s, "GET", "/v1/requests?status=all", "")
	if code != 200 || out["count"] != 2.0 {
		t.Fatalf("status=all = %d %v, want 2", code, out)
	}
	if code, _ := do(t, s, "GET", "/v1/requests?status=bogus", ""); code != 400 {
		t.Fatalf("bogus status filter should 400")
	}
}

func TestApproveAndDenyEndpoints(t *testing.T) {
	s := newServer(t, "")
	_, r1 := do(t, s, "POST", "/v1/requests", `{"action":"deploy.run"}`)
	_, r2 := do(t, s, "POST", "/v1/requests", `{"action":"mail.send"}`)
	code, out := do(t, s, "POST", "/v1/requests/"+r1["id"].(string)+"/approve", `{"reason":"change window open"}`)
	if code != 200 || out["status"] != "approved" || out["decided_by"] != "human" || out["reason"] != "change window open" {
		t.Fatalf("approve = %d %v", code, out)
	}
	code, out = do(t, s, "POST", "/v1/requests/"+r2["id"].(string)+"/deny", "")
	if code != 200 || out["status"] != "denied" {
		t.Fatalf("deny with empty body = %d %v", code, out)
	}
}

func TestDecideConflicts(t *testing.T) {
	s := newServer(t, "")
	_, r := do(t, s, "POST", "/v1/requests", `{"action":"deploy.run"}`)
	id := r["id"].(string)
	do(t, s, "POST", "/v1/requests/"+id+"/approve", "")
	if code, _ := do(t, s, "POST", "/v1/requests/"+id+"/deny", ""); code != 409 {
		t.Fatalf("re-deciding should 409, got %d", code)
	}
	if code, _ := do(t, s, "POST", "/v1/requests/af-unknown/approve", ""); code != 404 {
		t.Fatalf("deciding unknown id should 404, got %d", code)
	}
	// Expired requests conflict too: an approval that arrives after the
	// agent gave up must not retroactively green-light the action.
	_, r = do(t, s, "POST", "/v1/requests", `{"action":"deploy.run","ttl_seconds":60}`)
	s.Store.Now = func() time.Time { return base.Add(2 * time.Minute) }
	code, out := do(t, s, "POST", "/v1/requests/"+r["id"].(string)+"/approve", "")
	if code != 409 || !strings.Contains(out["error"].(string), "expired") {
		t.Fatalf("approving expired = %d %v, want 409", code, out)
	}
}

func TestTokenAuth(t *testing.T) {
	s := newServer(t, "s3cret")
	if code, _ := do(t, s, "GET", "/v1/requests", ""); code != 401 {
		t.Fatalf("missing token: got %d, want 401", code)
	}
	if code, _ := do(t, s, "GET", "/v1/requests", "", "Authorization", "Bearer wrong"); code != 401 {
		t.Fatalf("wrong token: got %d, want 401", code)
	}
	if code, _ := do(t, s, "GET", "/v1/requests", "", "Authorization", "Bearer s3cret"); code != 200 {
		t.Fatalf("right token: got %d, want 200", code)
	}
	// Liveness probes must not need credentials.
	if code, _ := do(t, s, "GET", "/healthz", ""); code != 200 {
		t.Fatalf("healthz behind auth")
	}
}

func TestWaitQueryReturnsDecidedRequest(t *testing.T) {
	s := newServer(t, "")
	_, r := do(t, s, "POST", "/v1/requests", `{"action":"deploy.run"}`)
	id := r["id"].(string)
	do(t, s, "POST", "/v1/requests/"+id+"/approve", "")
	// Already decided: the long poll returns on its first check.
	code, out := do(t, s, "GET", "/v1/requests/"+id+"?wait=30s", "")
	if code != 200 || out["status"] != "approved" {
		t.Fatalf("wait poll = %d %v", code, out)
	}
	if code, _ := do(t, s, "GET", "/v1/requests/"+id+"?wait=banana", ""); code != 400 {
		t.Fatalf("bad wait duration should 400")
	}
}

func TestPolicyEndpointExplainsRules(t *testing.T) {
	s := newServer(t, "")
	code, out := do(t, s, "GET", "/v1/policy", "")
	if code != 200 || out["default"] != "ask" {
		t.Fatalf("policy view = %d %v", code, out)
	}
	rules := out["rules"].([]any)
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(rules))
	}
	first := rules[0].(map[string]any)
	if first["effect"] != "allow" || first["text"] != "allow action:fs.read" {
		t.Fatalf("rule view = %v", first)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	s := newServer(t, "")
	req := httptest.NewRequest(http.MethodDelete, "/v1/requests", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE = %d, want 405", rec.Code)
	}
}
