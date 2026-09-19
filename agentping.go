package agentping

import (
	"context"
	"os"
	"regexp"
	"strconv"
	"time"
)

// StatusSnapshot is a point-in-time snapshot of the SDK's internal queue.
type StatusSnapshot struct {
	QueueSize    int
	DroppedCount uint64
	LastFlushAt  time.Time
	LastError    error
	Initialised  bool
}

// Init configures the package. Reads AGENTPING_API_KEY and AGENTPING_BASE_URL
// from the environment if no equivalent option is supplied. Init is safe to
// call multiple times; the previous worker is stopped first.
func Init(opts ...InitOption) error {
	cfg := initConfig{
		queueSize:     defaultQueueSize,
		flushInterval: defaultFlushInterval,
		batchSize:     defaultBatchSize,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.apiKey == "" {
		cfg.apiKey = os.Getenv("AGENTPING_API_KEY")
	}
	region := extractRegion(cfg.apiKey)
	if cfg.baseURL == "" {
		cfg.baseURL = os.Getenv("AGENTPING_BASE_URL")
	}
	if cfg.baseURL == "" {
		cfg.baseURL = defaultBaseURLForRegion(region)
	}
	if cfg.controlURL == "" {
		cfg.controlURL = os.Getenv("AGENTPING_CONTROL_URL")
	}
	if cfg.controlURL == "" {
		cfg.controlURL = defaultControlURLForRegion(region)
	}
	if cfg.queueSize <= 0 {
		cfg.queueSize = defaultQueueSize
	}
	if cfg.flushInterval <= 0 {
		cfg.flushInterval = defaultFlushInterval
	}
	if cfg.batchSize <= 0 {
		cfg.batchSize = defaultBatchSize
	}

	if cfg.apiKey == "" {
		warnOnce("auth_error", "no AGENTPING_API_KEY supplied; events will be dropped client-side.")
	}

	q := newBoundedQueue(cfg.queueSize)
	client := newHTTPClient(cfg.apiKey, cfg.baseURL, cfg.httpClient, defaultRequestTimeout)
	controlClient := newHTTPClient(cfg.apiKey, cfg.controlURL, cfg.httpClient, defaultRequestTimeout)
	w := newWorker(q, client, cfg.flushInterval, cfg.batchSize)

	s := &sdkState{
		apiKey:        cfg.apiKey,
		baseURL:       cfg.baseURL,
		controlURL:    cfg.controlURL,
		region:        region,
		queue:         q,
		worker:        w,
		client:        client,
		controlClient: controlClient,
		flushInterval: cfg.flushInterval,
		batchSize:     cfg.batchSize,
	}
	setState(s)
	w.start()
	return nil
}

// NewRun starts a new run for the given agent name. The run's ID is generated
// locally and is available the instant this call returns.
// NewSessionID returns an id for Conversation. Keep it for the life of the chat.
func NewSessionID() string {
	return uuid7Hex()
}

// Conversation reports one chat session as one run, with every turn appended to
// it. A conversation has no reliable end -- the user closes the tab, or simply
// stops -- so pass the same sessionID on every turn and the turns collect on a
// single record instead of becoming a run per message.
//
// The run id and its start time are both derived from sessionID, so a stateless
// handler can rebuild this on any request without storing a mapping; re-opening
// is idempotent server-side.
//
//	chat := agentping.Conversation("support-chat", sessionID)
//	chat.Event("llm_call", map[string]any{"provider": "anthropic", "model": model,
//		"input_tokens": in, "output_tokens": out})
//	// ...and when the user closes the chat:
//	chat.Finish(agentping.FinishOptions{Status: "success"})
//
// If Finish is never called the idle timeout closes it at its last activity.
// sessionID must be 32 hex characters whose first 12 are the session's start
// time in milliseconds; NewSessionID produces one.
func Conversation(agent, sessionID string, opts ...RunOption) *Run {
	if !sessionIDPattern.MatchString(sessionID) {
		warnOnce("bad_session_id", "agentping.Conversation: sessionID must be 32 hex characters (see NewSessionID); reporting as a normal run.")

		return NewRun(agent, opts...)
	}
	ms, err := strconv.ParseInt(sessionID[:12], 16, 64)
	if err != nil || ms <= 0 {
		warnOnce("bad_session_id", "agentping.Conversation: sessionID does not carry a start time; reporting as a normal run.")

		return NewRun(agent, opts...)
	}
	region := "eu"
	if s := getState(); s != nil {
		region = s.region
	}
	defaults := []RunOption{
		WithIdleTimeout(15 * time.Minute),
		WithIdleStatus("success"),
		func(c *runConfig) {
			c.resumeID = "run_" + region + "_" + sessionID
			c.resumeStart = time.UnixMilli(ms).UTC()
		},
	}

	return NewRun(agent, append(defaults, opts...)...)
}

var sessionIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func NewRun(agent string, opts ...RunOption) *Run {
	s := getState()
	if s == nil {
		warnOnce("not_initialised", "agentping.Init not called; run will not be reported.")
		return &Run{
			ID:        newID("run", "eu"),
			Agent:     agent,
			StartedAt: time.Now().UTC(),
		}
	}
	cfg := runConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.parentRunID == "" {
		cfg.parentRunID = os.Getenv("AGENTPING_PARENT_RUN")
	}
	// A conversation reuses the id and start time derived from its session, so
	// every turn re-opens the same run instead of minting a new one.
	runID, startedAt := newID("run", s.region), time.Now().UTC()
	if cfg.resumeID != "" {
		runID, startedAt = cfg.resumeID, cfg.resumeStart
	}
	r := &Run{
		ID:        runID,
		Agent:     agent,
		StartedAt: startedAt,
		state:     s,
	}
	body := map[string]any{
		"id":         r.ID,
		"agent":      agent,
		"started_at": r.StartedAt.Format(time.RFC3339Nano),
	}
	if cfg.customerID != "" {
		body["customer_id"] = cfg.customerID
	}
	if cfg.feature != "" {
		body["feature"] = cfg.feature
	}
	if cfg.metadata != nil {
		body["metadata"] = cfg.metadata
	}
	if cfg.parentRunID != "" {
		body["parent_run_id"] = cfg.parentRunID
	}
	if cfg.idleTimeout > 0 {
		body["idle_timeout_seconds"] = cfg.idleTimeout
		if cfg.idleStatus != "" {
			body["idle_timeout_status"] = cfg.idleStatus
		}
	}
	s.queue.push(envelope{kind: envRunStart, runID: r.ID, body: body})
	s.worker.notifyEnqueued()
	return r
}

// Heartbeat records a one-shot health ping. Suitable for cron jobs and short
// scripts that do not warrant a full run lifecycle.
func Heartbeat(agent string, status HeartbeatStatus, opts ...HeartbeatOption) error {
	s := getState()
	if s == nil {
		return ErrNotInitialized
	}
	cfg := heartbeatConfig{}
	for _, o := range opts {
		o(&cfg)
	}
	finished := cfg.finishedAt
	if finished.IsZero() {
		finished = time.Now().UTC()
	}
	started := cfg.startedAt
	if started.IsZero() {
		if cfg.durationMs != nil {
			started = finished.Add(-time.Duration(*cfg.durationMs) * time.Millisecond)
		} else {
			started = finished
		}
	}
	body := map[string]any{
		"id":          newID("run", s.region),
		"agent":       agent,
		"status":      string(status),
		"started_at":  started.Format(time.RFC3339Nano),
		"finished_at": finished.Format(time.RFC3339Nano),
	}
	if cfg.costUSD != nil {
		body["cost_usd"] = *cfg.costUSD
	}
	if cfg.metadata != nil {
		body["metadata"] = cfg.metadata
	}
	s.queue.push(envelope{kind: envHeartbeat, body: body})
	s.worker.notifyEnqueued()
	return nil
}

// Status returns a snapshot of internal SDK state for debugging and health
// checks. Always safe to call.
func Status() StatusSnapshot {
	s := getState()
	if s == nil {
		return StatusSnapshot{}
	}
	lastFlush, lastErr := s.worker.snapshot()
	return StatusSnapshot{
		QueueSize:    s.queue.size(),
		DroppedCount: s.queue.droppedCount(),
		LastFlushAt:  lastFlush,
		LastError:    lastErr,
		Initialised:  true,
	}
}

// Flush forces a synchronous drain of the queue. The caller's context bounds
// the wait. Returns the context error if it expires with items still queued.
func Flush(ctx context.Context) error {
	s := getState()
	if s == nil {
		return ErrNotInitialized
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	return s.worker.flushAll(ctx)
}

// Shutdown drains pending events and stops the background worker. The caller's
// context bounds the wait. Recommended pattern is
//
//	defer agentping.Shutdown(context.Background())
//
// in the user's main function. If Shutdown is not called, in-flight events
// may be lost when the process exits.
func Shutdown(ctx context.Context) error {
	s := getState()
	if s == nil {
		return nil
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	err := s.worker.flushAll(ctx)
	clearStateForTest()
	return err
}
