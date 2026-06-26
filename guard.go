package agentping

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The guard gate: a top-of-script safety net (guard-checks-spec).
//
// GuardCheck makes one blocking call to the control plane, which checks your
// configured rules against your real cumulative spend in USD, and refuses to
// start the run if a rule is tripped (or if an operator has paused the agent).
//
// Defaults fail closed on purpose: Mode defaults to "hard" (a block returns a
// *Paused error) and OnUnreachable to "block" (do not run unless confirmed
// safe). That couples your scheduled runs to AgentPing's uptime; set
// OnUnreachable to "allow" to run when the gate is unreachable.

const guardPath = "/v1/guard/check"

// GuardRequest carries the dimensions and the fail-open/closed choices for a
// guard check. All fields are optional; zero values mean "not sent" /
// "default".
type GuardRequest struct {
	CustomerRef   string
	Agent         string
	Function      string
	Environment   string
	Mode          string // "hard" (default) | "soft"
	OnUnreachable string // "block" (default) | "allow"
}

// GuardVerdict is the full server verdict (guard-checks-spec, "API contract").
type GuardVerdict struct {
	Decision       string           `json:"decision"`
	BlockedBy      []string         `json:"blocked_by"`
	Rules          []map[string]any `json:"rules"`
	StaleAsOf      *string          `json:"stale_as_of"`
	Active         bool             `json:"active"`
	InactiveReason *string          `json:"inactive_reason"`
	GuardCheckID   *string          `json:"guard_check_id"`
	CheckedAt      *string          `json:"checked_at"`
}

// Blocked reports whether the gate blocked the run.
func (v GuardVerdict) Blocked() bool { return v.Decision == "block" }

// Paused is returned by GuardCheck in hard mode when the gate blocks. It
// carries the full verdict so the caller can see what blocked and by how much.
type Paused struct {
	Verdict GuardVerdict
}

func (e *Paused) Error() string {
	reason := strings.Join(e.Verdict.BlockedBy, ", ")
	if reason == "" {
		reason = "a guard rule"
	}
	return "agentping: guard blocked this run (" + reason + ")"
}

// GuardCheck asks the gate whether to run. In hard mode (default) it returns a
// *Paused error on a block and the verdict on an allow; in soft mode it always
// returns the verdict with a nil error (inspect Verdict.Blocked()).
//
// A rate-limited gate (HTTP 429) is our condition, not a safety signal: it is
// retried once and then leans allow even under OnUnreachable "block". An
// unreachable gate follows OnUnreachable. An inactive plan is a no-op allow
// plus a loud one-time warning that the script is running unguarded.
func GuardCheck(ctx context.Context, req GuardRequest) (GuardVerdict, error) {
	mode := req.Mode
	if mode == "" {
		mode = "hard"
	}
	onUnreachable := req.OnUnreachable
	if onUnreachable == "" {
		onUnreachable = "block"
	}

	body := map[string]any{}
	if req.CustomerRef != "" {
		body["customer_ref"] = req.CustomerRef
	}
	if req.Agent != "" {
		body["agent"] = req.Agent
	}
	if req.Function != "" {
		body["function"] = req.Function
	}
	if req.Environment != "" {
		body["environment"] = req.Environment
	}

	s := getState()
	if s == nil || s.controlClient == nil || s.apiKey == "" {
		return guardUnreachable("not_initialized", mode, onUnreachable)
	}

	status, raw, err := s.controlClient.postRaw(ctx, guardPath, body)
	if err != nil {
		return guardUnreachable("network", mode, onUnreachable)
	}

	if status == 429 {
		time.Sleep(500 * time.Millisecond)
		status, raw, err = s.controlClient.postRaw(ctx, guardPath, body)
		if err != nil || status == 429 {
			// A throttle is our condition, not a safety signal: lean allow.
			return GuardVerdict{Decision: "allow", Active: true}, nil
		}
	}

	if status != 200 {
		return guardUnreachable(fmt.Sprintf("http_%d", status), mode, onUnreachable)
	}

	var v GuardVerdict
	if e := json.Unmarshal(raw, &v); e != nil || v.Decision == "" {
		return guardUnreachable("bad_response", mode, onUnreachable)
	}

	if !v.Active {
		warnOnce("guard_inactive_on_plan",
			"guard is inactive on your plan; this script is running UNGUARDED. Upgrade to the Team or Business plan to enable it.")
	}

	if v.Blocked() && mode == "hard" {
		return v, &Paused{Verdict: v}
	}
	return v, nil
}

func guardUnreachable(reason, mode, onUnreachable string) (GuardVerdict, error) {
	warnOnce("guard_unreachable",
		fmt.Sprintf("guard gate unreachable (%s); applying on_unreachable=%s.", reason, onUnreachable))

	v := GuardVerdict{Decision: "allow", Active: true}
	if onUnreachable == "block" {
		v.Decision = "block"
		v.BlockedBy = []string{"unreachable"}
	}
	if v.Blocked() && mode == "hard" {
		return v, &Paused{Verdict: v}
	}
	return v, nil
}
