// Package agentping is the Go SDK for AgentPing, production observability for
// AI agents. It captures cost, latency, and outcome telemetry for runs,
// heartbeats, and events without ever blocking user code.
//
// The SDK enqueues every payload onto a bounded in-memory queue and flushes it
// from a single background goroutine. HTTP calls carry a 2 second timeout.
// On failure the worker retries with exponential back-off up to five times,
// honouring Retry-After on 429s; further failures count as dropped events.
//
// Identifiers are generated locally as UUIDv7 with a prefix and a two-letter
// region extracted from the API key. The run ID is therefore available the
// instant Run returns and is safe to log, store, or return to a caller.
package agentping
