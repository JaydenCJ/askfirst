// Tests for the file-backed store. The store is the system of record for
// what was approved and by whom, so round-trip fidelity, decision
// idempotence, and clock-driven expiry are the load-bearing cases. All
// tests pin the clock and the ID generator — nothing here depends on
// wall time.
package queue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var base = time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC)

// testStore builds an initialized store with a deterministic clock and
// sequential IDs (af-1, af-2, …).
func testStore(t *testing.T) *Store {
	t.Helper()
	st := Open(filepath.Join(t.TempDir(), "inbox"))
	st.Now = func() time.Time { return base }
	n := 0
	st.NewID = func() string { n++; return fmt.Sprintf("af-%d", n) }
	if err := st.Init("default ask\n"); err != nil {
		t.Fatal(err)
	}
	return st
}

func addPending(t *testing.T, st *Store, action string) Request {
	t.Helper()
	r := Request{Agent: "test-agent", Action: action}
	if err := st.Add(&r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInitCreatesLayoutAndStarterPolicy(t *testing.T) {
	st := Open(filepath.Join(t.TempDir(), "inbox"))
	if err := st.Init("# starter\ndefault ask\n"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(st.PolicyPath())
	if err != nil || !strings.Contains(string(data), "default ask") {
		t.Fatalf("policy not written: %v / %q", err, data)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "requests")); err != nil {
		t.Fatalf("requests dir missing: %v", err)
	}
}

func TestInitNeverOverwritesEditedPolicy(t *testing.T) {
	st := testStore(t)
	if err := os.WriteFile(st.PolicyPath(), []byte("default deny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.Init("default ask\n"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(st.PolicyPath())
	if string(data) != "default deny\n" {
		t.Fatalf("re-init clobbered the operator's policy: %q", data)
	}
}

func TestAddAssignsIDCreatedAtAndStatus(t *testing.T) {
	st := testStore(t)
	r := addPending(t, st, "fs.read")
	if r.ID != "af-1" || !r.CreatedAt.Equal(base) || r.Status != StatusPending {
		t.Fatalf("defaults not applied: %+v", r)
	}
}

func TestAddRoundTripsAllFields(t *testing.T) {
	st := testStore(t)
	exp := base.Add(10 * time.Minute)
	in := Request{
		Agent:     "billing-bot",
		Action:    "payments.refund",
		Params:    map[string]any{"amount": 120.0, "note": "order 8812", "urgent": true},
		ExpiresAt: &exp,
	}
	if err := st.Add(&in); err != nil {
		t.Fatal(err)
	}
	got, err := st.Get(in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != in.Agent || got.Action != in.Action {
		t.Fatalf("identity fields lost: %+v", got)
	}
	if got.Params["amount"] != 120.0 || got.Params["urgent"] != true || got.Params["note"] != "order 8812" {
		t.Fatalf("params lost typing on round trip: %#v", got.Params)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(exp) {
		t.Fatalf("expiry lost: %+v", got.ExpiresAt)
	}
}

func TestAddRejectsInvalidRequests(t *testing.T) {
	st := testStore(t)
	if err := st.Add(&Request{Agent: "x"}); err == nil {
		t.Fatal("request without an action was accepted")
	}
	r := addPending(t, st, "fs.read")
	dup := Request{ID: r.ID, Action: "fs.read"}
	if err := st.Add(&dup); err == nil {
		t.Fatal("duplicate ID overwrote an existing request")
	}
}

func TestGetUnknownIsNotFound(t *testing.T) {
	st := testStore(t)
	_, err := st.Get("af-nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}

func TestGetRejectsPathTraversalIDs(t *testing.T) {
	// The ID is the only user input that becomes a file path; a crafted
	// ID must never escape the requests directory.
	st := testStore(t)
	for _, id := range []string{"../policy", "a/b", "..", "x\x00y", ""} {
		if _, err := st.Get(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestListSortsByCreationTime(t *testing.T) {
	st := testStore(t)
	times := []time.Time{base.Add(2 * time.Hour), base, base.Add(time.Hour)}
	for i, ts := range times {
		r := Request{Action: fmt.Sprintf("act.%d", i), CreatedAt: ts}
		if err := st.Add(&r); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 || list[0].Action != "act.1" || list[1].Action != "act.2" || list[2].Action != "act.0" {
		t.Fatalf("wrong order: %+v", list)
	}
}

func TestListFiltersByStatus(t *testing.T) {
	st := testStore(t)
	addPending(t, st, "a.one")
	r2 := addPending(t, st, "a.two")
	if _, err := st.Decide(r2.ID, true, "human", ""); err != nil {
		t.Fatal(err)
	}
	pending, _ := st.List(StatusPending)
	approved, _ := st.List(StatusApproved)
	if len(pending) != 1 || pending[0].Action != "a.one" {
		t.Fatalf("pending filter: %+v", pending)
	}
	if len(approved) != 1 || approved[0].Action != "a.two" {
		t.Fatalf("approved filter: %+v", approved)
	}
}

func TestDecideApprovesPending(t *testing.T) {
	st := testStore(t)
	r := addPending(t, st, "fs.write")
	got, err := st.Decide(r.ID, true, "human", "looks safe")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved || got.DecidedBy != "human" || got.Reason != "looks safe" {
		t.Fatalf("decision not recorded: %+v", got)
	}
	if got.DecidedAt == nil || !got.DecidedAt.Equal(base) {
		t.Fatalf("decided_at = %v, want pinned clock", got.DecidedAt)
	}
}

func TestDecideDenies(t *testing.T) {
	st := testStore(t)
	r := addPending(t, st, "shell.exec")
	got, err := st.Decide(r.ID, false, "human", "too broad")
	if err != nil || got.Status != StatusDenied || got.Reason != "too broad" {
		t.Fatalf("deny failed: %v %+v", err, got)
	}
}

func TestDecideTwiceFails(t *testing.T) {
	st := testStore(t)
	r := addPending(t, st, "fs.write")
	if _, err := st.Decide(r.ID, true, "human", ""); err != nil {
		t.Fatal(err)
	}
	// A second decision must not silently flip the recorded outcome.
	if _, err := st.Decide(r.ID, false, "human", ""); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("got %v, want ErrAlreadyDecided", err)
	}
	got, _ := st.Get(r.ID)
	if got.Status != StatusApproved {
		t.Fatalf("second decision overwrote the first: %s", got.Status)
	}
}

func TestExpiryIsComputedFromClock(t *testing.T) {
	st := testStore(t)
	exp := base.Add(5 * time.Minute)
	r := Request{Action: "deploy.run", ExpiresAt: &exp}
	if err := st.Add(&r); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Get(r.ID)
	if got.Status != StatusPending {
		t.Fatalf("not yet expired, got %s", got.Status)
	}
	st.Now = func() time.Time { return base.Add(6 * time.Minute) }
	got, _ = st.Get(r.ID)
	if got.Status != StatusExpired {
		t.Fatalf("past deadline, got %s", got.Status)
	}
	// Expiry is computed, not persisted: winding the clock back (as no
	// real deployment would, but tests do) restores pending.
	st.Now = func() time.Time { return base }
	got, _ = st.Get(r.ID)
	if got.Status != StatusPending {
		t.Fatalf("expiry was persisted prematurely: %s", got.Status)
	}
}

func TestDecideExpiredFails(t *testing.T) {
	st := testStore(t)
	exp := base.Add(time.Minute)
	r := Request{Action: "deploy.run", ExpiresAt: &exp}
	if err := st.Add(&r); err != nil {
		t.Fatal(err)
	}
	st.Now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, err := st.Decide(r.ID, true, "human", ""); !errors.Is(err, ErrExpired) {
		t.Fatalf("approving an expired request: got %v, want ErrExpired", err)
	}
}

func TestEmptyDecisionReasonPreservesAskContext(t *testing.T) {
	// An ask rule's reason rides along as reviewer context; a human
	// decision without a note must not erase it.
	st := testStore(t)
	r := Request{Action: "payments.refund", Reason: "refund above the auto-approve limit"}
	if err := st.Add(&r); err != nil {
		t.Fatal(err)
	}
	got, err := st.Decide(r.ID, true, "human", "")
	if err != nil || got.Reason != "refund above the auto-approve limit" {
		t.Fatalf("context reason lost: %v %+v", err, got)
	}
}

func TestAuditAppendAndReadBack(t *testing.T) {
	st := testStore(t)
	events := []Event{
		{Time: base, Event: EventSubmitted, ID: "af-1", Agent: "bot", Action: "fs.read"},
		{Time: base, Event: EventAutoApproved, ID: "af-1", Actor: "policy (line 2)"},
		{Time: base.Add(time.Minute), Event: EventDenied, ID: "af-2", Actor: "human", Reason: "no"},
	}
	for _, e := range events {
		if err := st.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Events()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Event != EventSubmitted || got[2].Reason != "no" {
		t.Fatalf("audit round trip: %+v", got)
	}
	if !got[2].Time.Equal(base.Add(time.Minute)) {
		t.Fatalf("event time drifted: %v", got[2].Time)
	}
}

func TestEventsMissingLogIsEmptyHistory(t *testing.T) {
	st := testStore(t)
	got, err := st.Events()
	if err != nil || got != nil {
		t.Fatalf("fresh store should have empty history, got %v / %v", got, err)
	}
}

func TestWaitReturnsDecidedImmediately(t *testing.T) {
	// A request decided before Wait is called must return on the first
	// poll — this is the fast path submit --wait relies on.
	st := testStore(t)
	r := addPending(t, st, "fs.write")
	if _, err := st.Decide(r.ID, true, "human", ""); err != nil {
		t.Fatal(err)
	}
	got, err := st.Wait(r.ID, 10*time.Second, time.Millisecond)
	if err != nil || got.Status != StatusApproved {
		t.Fatalf("wait on decided request: %v %+v", err, got)
	}
}

func TestWaitZeroTimeoutChecksOnce(t *testing.T) {
	st := testStore(t)
	r := addPending(t, st, "fs.write")
	got, err := st.Wait(r.ID, 0, time.Millisecond)
	if err != nil || got.Status != StatusPending {
		t.Fatalf("zero-timeout wait should report pending: %v %+v", err, got)
	}
	// Waiting on a request that never existed must fail, not spin.
	if _, err := st.Wait("af-missing", 0, time.Millisecond); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v, want ErrNotFound", err)
	}
}
