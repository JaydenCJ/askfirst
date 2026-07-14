// Package httpapi exposes the approval inbox over a local HTTP API so
// agents written in any language can submit actions and poll decisions
// with nothing more than an HTTP client. The API is designed to be
// served on 127.0.0.1; an optional bearer token gates every /v1 route.
package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/JaydenCJ/askfirst/internal/inbox"
	"github.com/JaydenCJ/askfirst/internal/policy"
	"github.com/JaydenCJ/askfirst/internal/queue"
)

// maxWait caps ?wait long-polling so a stray client cannot pin a handler.
const maxWait = 60 * time.Second

// Server wires the store and policy into HTTP handlers.
type Server struct {
	Store   *queue.Store
	Policy  *policy.Policy
	Token   string // optional; empty disables auth
	Version string
}

// Handler returns the full route table. /healthz stays outside auth so
// liveness probes work without credentials.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.HandleFunc("POST /v1/requests", s.handleSubmit)
	mux.HandleFunc("GET /v1/requests", s.handleList)
	mux.HandleFunc("GET /v1/requests/{id}", s.handleGet)
	mux.HandleFunc("POST /v1/requests/{id}/approve", s.decide(true))
	mux.HandleFunc("POST /v1/requests/{id}/deny", s.decide(false))
	mux.HandleFunc("GET /v1/policy", s.handlePolicy)
	return s.auth(mux)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token != "" && r.URL.Path != "/healthz" {
			got := r.Header.Get("Authorization")
			want := "Bearer " + s.Token
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				writeError(w, http.StatusUnauthorized, "missing or invalid bearer token")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.Version})
}

type submitBody struct {
	Agent      string         `json:"agent"`
	Action     string         `json:"action"`
	Params     map[string]any `json:"params"`
	TTLSeconds int            `json:"ttl_seconds"`
}

func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var body submitBody
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Action == "" {
		writeError(w, http.StatusBadRequest, `"action" is required`)
		return
	}
	req := queue.Request{Agent: body.Agent, Action: body.Action, Params: body.Params}
	if body.TTLSeconds < 0 {
		writeError(w, http.StatusBadRequest, `"ttl_seconds" must be >= 0`)
		return
	}
	if body.TTLSeconds > 0 {
		exp := s.Store.Now().Add(time.Duration(body.TTLSeconds) * time.Second)
		req.ExpiresAt = &exp
	}
	rec, _, err := inbox.Submit(s.Store, s.Policy, &req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	status := http.StatusOK
	if rec.Status == queue.StatusPending {
		status = http.StatusAccepted // decision not made yet; poll GET /v1/requests/{id}
	}
	writeJSON(w, status, rec)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	filter := queue.StatusPending // the API is an inbox; pending is the default view
	if q := r.URL.Query().Get("status"); q != "" && q != "all" {
		filter = queue.Status(q)
		if !queue.ValidFilter(filter) {
			writeError(w, http.StatusBadRequest, "unknown status "+q)
			return
		}
	} else if q == "all" {
		filter = ""
	}
	list, err := s.Store.List(filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if list == nil {
		list = []queue.Request{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": list, "count": len(list)})
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var rec queue.Request
	var err error
	if waitq := r.URL.Query().Get("wait"); waitq != "" {
		d, perr := time.ParseDuration(waitq)
		if perr != nil || d < 0 {
			writeError(w, http.StatusBadRequest, "invalid wait duration "+waitq)
			return
		}
		if d > maxWait {
			d = maxWait
		}
		rec, err = s.Store.Wait(id, d, 100*time.Millisecond)
	} else {
		rec, err = s.Store.Get(id)
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

func (s *Server) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Reason string `json:"reason"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		// An empty body (io.EOF) means "no reason given" and is fine;
		// anything else malformed is a client error.
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
		rec, err := inbox.Decide(s.Store, r.PathValue("id"), approve, body.Reason)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rec)
	}
}

func (s *Server) handlePolicy(w http.ResponseWriter, _ *http.Request) {
	type ruleView struct {
		Line   int    `json:"line"`
		Effect string `json:"effect"`
		Text   string `json:"text"`
	}
	rules := make([]ruleView, 0, len(s.Policy.Rules))
	for _, r := range s.Policy.Rules {
		rules = append(rules, ruleView{Line: r.Line, Effect: string(r.Effect), Text: r.Text})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source":  s.Policy.Source,
		"default": string(s.Policy.Default),
		"rules":   rules,
	})
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, queue.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, queue.ErrAlreadyDecided), errors.Is(err, queue.ErrExpired):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	data, err := json.Marshal(v)
	if err != nil {
		data = []byte(`{"error":"encoding failure"}`)
	}
	w.Write(append(data, '\n'))
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
