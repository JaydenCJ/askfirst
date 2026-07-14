package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/JaydenCJ/askfirst/internal/queue"
)

// paramsWidth caps the params column in list output so one verbose
// request cannot wreck the table.
const paramsWidth = 48

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// describeDecision explains who decided and why, for one-line output.
func describeDecision(r queue.Request) string {
	who := r.DecidedBy
	if who == "" {
		who = "undecided"
	}
	if r.Rule != "" {
		// The rule text already carries any reason:"…" annotation.
		return who + ": " + r.Rule
	}
	if r.Reason != "" {
		return fmt.Sprintf("%s (%s)", who, r.Reason)
	}
	return who
}

// renderSubmit prints the one-line submit outcome. The first word is
// always the status, so shell scripts can pattern-match on it.
func renderSubmit(w io.Writer, r queue.Request, waited bool, timeout time.Duration) {
	switch r.Status {
	case queue.StatusPending:
		reason := ""
		if r.Reason != "" {
			reason = ": " + r.Reason
		}
		if waited {
			fmt.Fprintf(w, "pending %s — no decision after %s; still in the inbox%s\n", r.ID, timeout, reason)
		} else {
			fmt.Fprintf(w, "pending %s — queued for human review%s\n", r.ID, reason)
			fmt.Fprintf(w, "decide with: askfirst approve %s   (or: askfirst deny %s)\n", r.ID, r.ID)
		}
	case queue.StatusExpired:
		fmt.Fprintf(w, "expired %s — expired before review\n", r.ID)
	default:
		fmt.Fprintf(w, "%s %s — %s\n", r.Status, r.ID, describeDecision(r))
	}
}

func renderList(w io.Writer, list []queue.Request, filterName string) {
	if len(list) == 0 {
		if filterName == "all" {
			fmt.Fprintln(w, "inbox is empty")
		} else {
			fmt.Fprintf(w, "no %s requests\n", filterName)
		}
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCREATED\tAGENT\tACTION\tSTATUS\tPARAMS")
	for _, r := range list {
		agent := r.Agent
		if agent == "" {
			agent = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
			agent, r.Action, r.Status, summarizeParams(r.Params))
	}
	tw.Flush()
	unit := "request"
	if len(list) != 1 {
		unit = "requests"
	}
	fmt.Fprintf(w, "%d %s %s\n", len(list), filterName, unit)
}

func renderShow(w io.Writer, r queue.Request) {
	fmt.Fprintf(w, "id:       %s\n", r.ID)
	fmt.Fprintf(w, "status:   %s\n", r.Status)
	if r.Agent != "" {
		fmt.Fprintf(w, "agent:    %s\n", r.Agent)
	}
	fmt.Fprintf(w, "action:   %s\n", r.Action)
	fmt.Fprintf(w, "created:  %s\n", fmtTime(r.CreatedAt))
	if r.ExpiresAt != nil {
		fmt.Fprintf(w, "expires:  %s\n", fmtTime(*r.ExpiresAt))
	}
	if len(r.Params) > 0 {
		data, err := json.MarshalIndent(r.Params, "  ", "  ")
		if err != nil {
			data = []byte("(unrenderable)")
		}
		fmt.Fprintf(w, "params:   %s\n", data)
	}
	if r.DecidedAt != nil {
		fmt.Fprintf(w, "decided:  %s — %s\n", fmtTime(*r.DecidedAt), r.DecidedBy)
	}
	if r.Rule != "" {
		fmt.Fprintf(w, "rule:     %s\n", r.Rule)
	}
	if r.Reason != "" {
		fmt.Fprintf(w, "reason:   %s\n", r.Reason)
	}
}

func renderEvents(w io.Writer, events []queue.Event) {
	if len(events) == 0 {
		fmt.Fprintln(w, "audit log is empty")
		return
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tEVENT\tID\tACTOR\tACTION\tREASON")
	for _, e := range events {
		actor := e.Actor
		if actor == "" {
			actor = "-"
		}
		reason := e.Reason
		if reason == "" {
			reason = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			fmtTime(e.Time), e.Event, e.ID, actor, e.Action, reason)
	}
	tw.Flush()
}

// summarizeParams renders params as compact JSON (keys sorted by
// encoding/json, so output is deterministic), truncated for the table.
func summarizeParams(params map[string]any) string {
	if len(params) == 0 {
		return "-"
	}
	data, err := json.Marshal(params)
	if err != nil {
		return "(unrenderable)"
	}
	s := string(data)
	if len(s) > paramsWidth {
		// Cut on a rune boundary so multi-byte params cannot split.
		runes := []rune(s)
		if len(runes) > paramsWidth {
			s = string(runes[:paramsWidth-1]) + "…"
		}
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func printJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(w, string(data))
	return err
}
