package agentping

import (
	"sync/atomic"
	"time"
)

// Run is a single agent invocation. The ID is populated synchronously before
// any network call is made, so it is safe to log, store, or return to a caller
// immediately.
type Run struct {
	ID        string
	Agent     string
	StartedAt time.Time

	state    *sdkState
	finished atomic.Bool
}

// Event appends a typed event to the run. Returns ErrNotInitialized if the
// SDK was not initialised; otherwise never blocks and never returns an error.
func (r *Run) Event(eventType string, data map[string]any) error {
	if r == nil || r.state == nil {
		return ErrNotInitialized
	}
	if eventType == "" {
		eventType = "log"
	}
	if data == nil {
		data = map[string]any{}
	}
	if r.finished.Load() {
		warnOnce("client_error", "event() called after finish(); event still queued but server may reject.")
	}
	evt := map[string]any{
		"external_id": newID("evt", r.state.region),
		"type":        eventType,
		"ts":          time.Now().UTC().Format(time.RFC3339Nano),
		"data":        data,
	}
	r.state.queue.push(envelope{
		kind:  envRunEvent,
		runID: r.ID,
		event: evt,
	})
	r.state.worker.notifyEnqueued()
	return nil
}

// Finish closes the run with a terminal status. Idempotent: subsequent calls
// are silently ignored.
func (r *Run) Finish(status FinishStatus, opts ...FinishOption) error {
	if r == nil || r.state == nil {
		return ErrNotInitialized
	}
	if !r.finished.CompareAndSwap(false, true) {
		return nil
	}
	if status == "" {
		status = FinishStatusSuccess
	}
	cfg := finishConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	body := map[string]any{
		"status":      string(status),
		"finished_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
	if cfg.output != nil {
		body["output"] = cfg.output
	}
	if cfg.scores != nil {
		body["scores"] = cfg.scores
	}
	if cfg.metadata != nil {
		body["metadata"] = cfg.metadata
	}
	r.state.queue.push(envelope{
		kind:  envRunFinish,
		runID: r.ID,
		body:  body,
	})
	r.state.worker.notifyEnqueued()
	return nil
}
