// Package inbox glues policy evaluation to the queue. Every submission —
// whether it arrives through the CLI or the HTTP API — flows through
// Submit, and every human decision through Decide, so records and audit
// entries are identical no matter which door was used.
package inbox

import (
	"github.com/JaydenCJ/askfirst/internal/policy"
	"github.com/JaydenCJ/askfirst/internal/queue"
)

// Submit evaluates req against the policy, persists it with the outcome
// already applied (approved/denied for auto decisions, pending for ask),
// and appends the matching audit events.
func Submit(st *queue.Store, pol *policy.Policy, req *queue.Request) (queue.Request, policy.Decision, error) {
	dec := pol.Evaluate(policy.Input{Agent: req.Agent, Action: req.Action, Params: req.Params})
	now := st.Now()
	switch dec.Effect {
	case policy.Allow:
		req.Status = queue.StatusApproved
	case policy.Deny:
		req.Status = queue.StatusDenied
	default:
		req.Status = queue.StatusPending
	}
	if req.Status == queue.StatusPending {
		// An ask rule's reason travels with the request as reviewer context.
		req.Reason = dec.Reason
	} else {
		req.DecidedAt = &now
		req.DecidedBy = dec.Actor()
		req.Reason = dec.Reason
		if dec.Rule != nil {
			req.Rule = dec.Rule.Text
		}
	}
	if err := st.Add(req); err != nil {
		return queue.Request{}, dec, err
	}
	if err := st.Append(queue.Event{
		Time: now, Event: queue.EventSubmitted,
		ID: req.ID, Agent: req.Agent, Action: req.Action,
	}); err != nil {
		return queue.Request{}, dec, err
	}
	if req.Status != queue.StatusPending {
		ev := queue.EventAutoApproved
		if req.Status == queue.StatusDenied {
			ev = queue.EventAutoDenied
		}
		if err := st.Append(queue.Event{
			Time: now, Event: ev,
			ID: req.ID, Agent: req.Agent, Action: req.Action,
			Actor: dec.Actor(), Reason: dec.Reason,
		}); err != nil {
			return queue.Request{}, dec, err
		}
	}
	return *req, dec, nil
}

// Decide records a human decision on a pending request and audits it.
func Decide(st *queue.Store, id string, approve bool, reason string) (queue.Request, error) {
	r, err := st.Decide(id, approve, "human", reason)
	if err != nil {
		return queue.Request{}, err
	}
	ev := queue.EventApproved
	if !approve {
		ev = queue.EventDenied
	}
	if err := st.Append(queue.Event{
		Time: *r.DecidedAt, Event: ev,
		ID: r.ID, Agent: r.Agent, Action: r.Action,
		Actor: "human", Reason: reason,
	}); err != nil {
		return queue.Request{}, err
	}
	return r, nil
}
