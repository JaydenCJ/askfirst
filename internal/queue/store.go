package queue

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sentinel errors callers branch on for exit codes and HTTP statuses.
var (
	ErrNotFound       = errors.New("no such request")
	ErrAlreadyDecided = errors.New("request is already decided")
	ErrExpired        = errors.New("request expired before review")
)

// idRe guards against path traversal: request IDs are the only
// user-supplied value that becomes part of a file path.
var idRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Store is a directory-backed request store. The zero value is not
// usable; construct with Open.
type Store struct {
	Dir string

	// Now supplies the clock; tests pin it for deterministic expiry.
	Now func() time.Time
	// NewID supplies request IDs; tests pin it for stable output.
	NewID func() string

	mu sync.Mutex // serializes in-process read-modify-write on decisions
}

// Open returns a Store rooted at dir. It performs no I/O; call Init to
// create the layout.
func Open(dir string) *Store {
	s := &Store{Dir: dir}
	s.Now = func() time.Time { return time.Now().UTC() }
	s.NewID = func() string { return newID(s.Now()) }
	return s
}

// PolicyPath is where the store's policy file lives.
func (s *Store) PolicyPath() string { return filepath.Join(s.Dir, "policy.rules") }

// AuditPath is the append-only audit log location.
func (s *Store) AuditPath() string { return filepath.Join(s.Dir, "audit.jsonl") }

func (s *Store) requestsDir() string { return filepath.Join(s.Dir, "requests") }

func (s *Store) requestPath(id string) string {
	return filepath.Join(s.requestsDir(), id+".json")
}

// Init creates the directory layout and, if no policy file exists yet,
// writes starterPolicy. Existing policies are never overwritten.
func (s *Store) Init(starterPolicy string) error {
	if err := os.MkdirAll(s.requestsDir(), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(s.PolicyPath()); errors.Is(err, os.ErrNotExist) {
		return os.WriteFile(s.PolicyPath(), []byte(starterPolicy), 0o644)
	} else if err != nil {
		return err
	}
	return nil
}

// Add persists a new request, assigning ID, CreatedAt, and Status when
// they are unset. It refuses to overwrite an existing request.
func (s *Store) Add(r *Request) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Action == "" {
		return fmt.Errorf("request has no action")
	}
	if r.ID == "" {
		r.ID = s.NewID()
	}
	if !idRe.MatchString(r.ID) {
		return fmt.Errorf("invalid request id %q", r.ID)
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = s.Now()
	}
	if r.Status == "" {
		r.Status = StatusPending
	}
	if _, err := os.Stat(s.requestPath(r.ID)); err == nil {
		return fmt.Errorf("request %s already exists", r.ID)
	}
	return s.write(*r)
}

// Get loads one request and applies computed expiry.
func (s *Store) Get(id string) (Request, error) {
	if !idRe.MatchString(id) {
		return Request{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	data, err := os.ReadFile(s.requestPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Request{}, fmt.Errorf("%w: %q", ErrNotFound, id)
	}
	if err != nil {
		return Request{}, err
	}
	var r Request
	if err := json.Unmarshal(data, &r); err != nil {
		return Request{}, fmt.Errorf("corrupt request %s: %v", id, err)
	}
	s.applyExpiry(&r)
	return r, nil
}

// List returns requests sorted by creation time (ties broken by ID),
// optionally filtered to one status. An empty filter returns everything.
func (s *Store) List(filter Status) ([]Request, error) {
	entries, err := os.ReadDir(s.requestsDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inbox not initialized at %s (run `askfirst init` first)", s.Dir)
	}
	if err != nil {
		return nil, err
	}
	var out []Request
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		r, err := s.Get(strings.TrimSuffix(name, ".json"))
		if err != nil {
			return nil, err
		}
		if filter == "" || r.Status == filter {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Decide resolves a pending request. Expired and already-decided requests
// are rejected with sentinel errors. If reason is empty, any reason
// already on the request (e.g. from an ask rule) is preserved as context.
func (s *Store) Decide(id string, approve bool, actor, reason string) (Request, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.Get(id)
	if err != nil {
		return Request{}, err
	}
	switch r.Status {
	case StatusExpired:
		return Request{}, fmt.Errorf("%w: %s", ErrExpired, id)
	case StatusApproved, StatusDenied:
		return Request{}, fmt.Errorf("%w: %s is %s", ErrAlreadyDecided, id, r.Status)
	}
	now := s.Now()
	r.Status = StatusApproved
	if !approve {
		r.Status = StatusDenied
	}
	r.DecidedAt = &now
	r.DecidedBy = actor
	if reason != "" {
		r.Reason = reason
	}
	if err := s.write(r); err != nil {
		return Request{}, err
	}
	return r, nil
}

// Wait polls until the request leaves pending (decided or expired) or the
// timeout elapses, returning the last observed state either way. A
// non-positive timeout means a single, immediate check.
func (s *Store) Wait(id string, timeout, interval time.Duration) (Request, error) {
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		r, err := s.Get(id)
		if err != nil {
			return Request{}, err
		}
		if r.Status != StatusPending {
			return r, nil
		}
		remain := time.Until(deadline)
		if timeout <= 0 || remain <= 0 {
			return r, nil
		}
		if remain < interval {
			interval = remain
		}
		time.Sleep(interval)
	}
}

// Append writes one audit event as a JSONL line.
func (s *Store) Append(e Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.AuditPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Events reads the whole audit log in append order. A missing log is an
// empty history, not an error.
func (s *Store) Events() ([]Event, error) {
	data, err := os.ReadFile(s.AuditPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Event
	for i, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("corrupt audit log line %d: %v", i+1, err)
		}
		out = append(out, e)
	}
	return out, nil
}

// applyExpiry downgrades a stored pending request to expired once its
// deadline passes. Expiry is purely computed — the file is not rewritten,
// so re-reading with an earlier clock (in tests) is side-effect free.
func (s *Store) applyExpiry(r *Request) {
	if r.Status == StatusPending && r.ExpiresAt != nil && s.Now().After(*r.ExpiresAt) {
		r.Status = StatusExpired
	}
}

// write atomically persists a request: temp file in the same directory,
// then rename over the destination.
func (s *Store) write(r Request) error {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.requestsDir(), ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, s.requestPath(r.ID))
}
