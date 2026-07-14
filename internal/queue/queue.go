// Package queue implements the file-backed approval queue: one JSON
// document per request under <dir>/requests/, plus an append-only audit
// log at <dir>/audit.jsonl. Every write is atomic (temp file + rename),
// so a crash never leaves a half-written record, and any process with
// access to the directory — CLI, HTTP API, or both at once — sees the
// same state.
package queue

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"time"
)

// Status is the lifecycle state of a request. Pending, approved, and
// denied are stored; expired is computed from ExpiresAt at read time, so
// no background sweeper is needed.
type Status string

const (
	StatusPending  Status = "pending"
	StatusApproved Status = "approved"
	StatusDenied   Status = "denied"
	StatusExpired  Status = "expired"
)

// ValidFilter reports whether s names a status usable as a list filter.
func ValidFilter(s Status) bool {
	switch s {
	case StatusPending, StatusApproved, StatusDenied, StatusExpired:
		return true
	}
	return false
}

// Request is one action an agent wants to perform.
type Request struct {
	ID        string         `json:"id"`
	Agent     string         `json:"agent,omitempty"`
	Action    string         `json:"action"`
	Params    map[string]any `json:"params,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt *time.Time     `json:"expires_at,omitempty"`
	Status    Status         `json:"status"`
	DecidedAt *time.Time     `json:"decided_at,omitempty"`
	DecidedBy string         `json:"decided_by,omitempty"` // "human" or "policy (line N)"
	Rule      string         `json:"rule,omitempty"`       // matched rule text, policy decisions only
	Reason    string         `json:"reason,omitempty"`
}

// Decided reports whether the request has reached a terminal decision.
func (r *Request) Decided() bool {
	return r.Status == StatusApproved || r.Status == StatusDenied
}

// Audit log event names.
const (
	EventSubmitted    = "submitted"
	EventAutoApproved = "auto_approved"
	EventAutoDenied   = "auto_denied"
	EventApproved     = "approved"
	EventDenied       = "denied"
)

// Event is one audit log line; the log is append-only JSONL.
type Event struct {
	Time   time.Time `json:"time"`
	Event  string    `json:"event"`
	ID     string    `json:"id"`
	Agent  string    `json:"agent,omitempty"`
	Action string    `json:"action,omitempty"`
	Actor  string    `json:"actor,omitempty"` // "human" or "policy (line N)"
	Reason string    `json:"reason,omitempty"`
}

// newID builds a request ID: a millisecond timestamp in base 36 (so IDs
// sort roughly by creation) plus 4 random bytes against collisions.
func newID(now time.Time) string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "af-" + strconv.FormatInt(now.UnixMilli(), 36) + "-" + hex.EncodeToString(b[:])
}
