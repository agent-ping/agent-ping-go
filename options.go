package agentping

import (
	"net/http"
	"time"
)

// InitOption configures the package on Init.
type InitOption func(*initConfig)

type initConfig struct {
	apiKey        string
	baseURL       string
	controlURL    string
	queueSize     int
	flushInterval time.Duration
	batchSize     int
	httpClient    *http.Client
	version       string
}

// WithAPIKey sets the API key directly. If not provided, AGENTPING_API_KEY is read.
func WithAPIKey(key string) InitOption {
	return func(c *initConfig) { c.apiKey = key }
}

// WithBaseURL overrides the default API base URL. AGENTPING_BASE_URL also works.
func WithBaseURL(url string) InitOption {
	return func(c *initConfig) { c.baseURL = url }
}

// WithControlURL overrides the control-plane base URL used by GuardCheck
// (guard-checks-spec). AGENTPING_CONTROL_URL also works. Defaults to the
// region's control plane (EU: https://agentping.io).
func WithControlURL(url string) InitOption {
	return func(c *initConfig) { c.controlURL = url }
}

// WithQueueSize sets the bounded queue capacity. Default 1000.
func WithQueueSize(n int) InitOption {
	return func(c *initConfig) { c.queueSize = n }
}

// WithFlushInterval sets how often the background worker drains the queue. Default 2s.
func WithFlushInterval(d time.Duration) InitOption {
	return func(c *initConfig) { c.flushInterval = d }
}

// WithBatchSize caps the number of events posted per HTTP call. Default 50.
func WithBatchSize(n int) InitOption {
	return func(c *initConfig) { c.batchSize = n }
}

// WithHTTPClient injects a custom http.Client. Useful for tests.
func WithHTTPClient(h *http.Client) InitOption {
	return func(c *initConfig) { c.httpClient = h }
}

// WithDefaultVersion labels every run this process starts with the build or
// prompt version that produced it, unless the run sets its own WithVersion.
// AGENTPING_VERSION also works. The dashboard's Spend and Verify charts mark
// where each new version was first seen.
func WithDefaultVersion(v string) InitOption {
	return func(c *initConfig) { c.version = v }
}

// RunOption configures a new run.
type RunOption func(*runConfig)

type runConfig struct {
	customerID  string
	feature     string
	metadata    map[string]any
	parentRunID string
	idleTimeout int
	idleStatus  string
	resumeID    string
	resumeStart time.Time
	version     string
}

// WithCustomerID associates a run with an end customer.
func WithCustomerID(id string) RunOption {
	return func(c *runConfig) { c.customerID = id }
}

// WithFeature attaches a feature label to the run.
func WithFeature(f string) RunOption {
	return func(c *runConfig) { c.feature = f }
}

// WithMetadata attaches arbitrary structured metadata.
func WithMetadata(m map[string]any) RunOption {
	return func(c *runConfig) { c.metadata = m }
}

// WithVersion labels the run with the build or prompt version that produced
// it (up to 64 characters), overriding Init's WithDefaultVersion.
func WithVersion(v string) RunOption {
	return func(c *runConfig) { c.version = v }
}

// WithParentRunID links this run to a parent, building a run tree.
func WithParentRunID(id string) RunOption {
	return func(c *runConfig) { c.parentRunID = id }
}

// WithIdleTimeout closes the run automatically once no event has arrived for
// this long (60s-24h). For callers that cannot be relied on to call Finish: a
// browser that gets closed, a process that gets killed. It also makes the run
// resumable, so a later event reopens it rather than being an orphan.
func WithIdleTimeout(d time.Duration) RunOption {
	return func(c *runConfig) { c.idleTimeout = int(d.Seconds()) }
}

// WithIdleStatus sets what the idle sweep finishes the run with. The server
// default is "timeout" -- we stopped waiting, we do not know how it went. A
// conversation should pass "success": the user leaving is not a failure.
func WithIdleStatus(status string) RunOption {
	return func(c *runConfig) { c.idleStatus = status }
}

// FinishOption configures Run.Finish.
type FinishOption func(*finishConfig)

type finishConfig struct {
	output   map[string]any
	scores   map[string]any
	metadata map[string]any
}

// WithOutput attaches the final output payload.
func WithOutput(m map[string]any) FinishOption {
	return func(c *finishConfig) { c.output = m }
}

// WithScores attaches self-reported scores.
func WithScores(m map[string]any) FinishOption {
	return func(c *finishConfig) { c.scores = m }
}

// WithFinishMetadata attaches metadata at finish time.
func WithFinishMetadata(m map[string]any) FinishOption {
	return func(c *finishConfig) { c.metadata = m }
}

// HeartbeatOption configures Heartbeat.
type HeartbeatOption func(*heartbeatConfig)

type heartbeatConfig struct {
	costUSD    *float64
	durationMs *int64
	metadata   map[string]any
	startedAt  time.Time
	finishedAt time.Time
}

// WithCostUSD sets the cost figure recorded on the heartbeat.
func WithCostUSD(v float64) HeartbeatOption {
	return func(c *heartbeatConfig) { c.costUSD = &v }
}

// WithDurationMs sets the duration in milliseconds.
func WithDurationMs(v int64) HeartbeatOption {
	return func(c *heartbeatConfig) { c.durationMs = &v }
}

// WithHeartbeatMetadata attaches arbitrary metadata to a heartbeat.
func WithHeartbeatMetadata(m map[string]any) HeartbeatOption {
	return func(c *heartbeatConfig) { c.metadata = m }
}

// WithHeartbeatStartedAt overrides the start timestamp.
func WithHeartbeatStartedAt(t time.Time) HeartbeatOption {
	return func(c *heartbeatConfig) { c.startedAt = t }
}

// WithHeartbeatFinishedAt overrides the finish timestamp.
func WithHeartbeatFinishedAt(t time.Time) HeartbeatOption {
	return func(c *heartbeatConfig) { c.finishedAt = t }
}

// FinishStatus represents the terminal state of a run.
type FinishStatus string

const (
	// FinishStatusSuccess indicates the run completed successfully.
	FinishStatusSuccess FinishStatus = "success"
	// FinishStatusFailed indicates the run failed.
	FinishStatusFailed FinishStatus = "failed"
	// FinishStatusTimeout indicates the run exceeded its time budget.
	FinishStatusTimeout FinishStatus = "timeout"
)

// HeartbeatStatus represents the outcome of a heartbeat ping.
type HeartbeatStatus string

const (
	// HeartbeatStatusOK indicates a healthy heartbeat.
	HeartbeatStatusOK HeartbeatStatus = "ok"
	// HeartbeatStatusFailed indicates a failed heartbeat.
	HeartbeatStatusFailed HeartbeatStatus = "failed"
	// HeartbeatStatusTimeout indicates a timed-out heartbeat.
	HeartbeatStatusTimeout HeartbeatStatus = "timeout"
)
