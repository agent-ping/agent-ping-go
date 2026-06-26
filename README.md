# AgentPing SDK for Go

Production observability for AI agents. Spend, Pulse, Verify.
[agentping.io](https://agentping.io) ·
[docs](https://agentping.io/docs/sdks/go).

## Install

```bash
go get github.com/agent-ping/agent-ping-go
```

Go 1.22 or later. Stdlib only.

## Quickstart

```go
package main

import (
	"context"
	"os"

	"github.com/agent-ping/agent-ping-go"
)

func main() {
	_ = agentping.Init(agentping.WithAPIKey(os.Getenv("AGENTPING_API_KEY")))
	defer agentping.Shutdown(context.Background())

	run := agentping.NewRun("support-triage",
		agentping.WithCustomerID("cust_123"),
	)
	_ = run.Event("log", map[string]any{"message": "classified ticket"})
	_ = run.Event("llm_call", map[string]any{
		"provider":      "anthropic",
		"model":         "claude-sonnet-4-5",
		"input_tokens":  1024,
		"output_tokens": 312,
		"latency_ms":    1430,
	})
	_ = run.Finish(agentping.FinishStatusSuccess,
		agentping.WithScores(map[string]any{"confidence": 0.93}),
	)
}
```

`run.ID` is populated synchronously before any network call. Safe to log, store, and return to your caller immediately.

## Heartbeats

For cron jobs and one-shot scripts:

```go
_ = agentping.Heartbeat("daily-summary", agentping.HeartbeatStatusOK,
	agentping.WithCostUSD(0.084),
	agentping.WithDurationMs(12_300),
	agentping.WithHeartbeatMetadata(map[string]any{"rows": 421}),
)
```

## Guard (spend safety net)

Put a guard at the top of a scheduled or autonomous program. It makes one
blocking call to AgentPing, which checks your configured rules against your real
cumulative spend, and refuses to start the run if a rule is tripped (or if an
operator has paused the agent from the dashboard). Guard is on the Team and
Business plans.

```go
// Hard mode (default): a block returns a *agentping.Paused error.
v, err := agentping.GuardCheck(ctx, agentping.GuardRequest{
	CustomerRef: "acme-corp",
	Agent:       "nightly-summariser",
	Function:    "run",
})
var paused *agentping.Paused
if errors.As(err, &paused) {
	log.Printf("blocked by %v: %v", paused.Verdict.BlockedBy, paused.Verdict.Rules)
	return
}
_ = v
```

By default a guard fails closed: a block returns `*agentping.Paused`, and
`OnUnreachable` defaults to `"block"` so the program does not run unless the gate
confirms it is safe. That couples your scheduled runs to AgentPing's uptime; set
`OnUnreachable: "allow"` to run when the gate is unreachable. In soft mode
(`Mode: "soft"`) a block returns a nil error and you inspect `v.Blocked()`.

A guard at the top of a program protects against the next invocation starting,
not against one run that passes the check and then loops forever. Call
`GuardCheck` at several points to turn it into a series of gates. Thresholds are
set in USD.

## Auto-instrumentation

The Go SDK uses `http.RoundTripper` wrappers rather than monkey-patching.
Build a transport, hand it to the provider client. The transport
intercepts requests, captures token counts from the response body, and
emits an `llm_call` event onto the run you bind it to.

```go
import (
	"net/http"

	"github.com/agent-ping/agent-ping-go"
	"github.com/agent-ping/agent-ping-go/instrumentation"
)

run := agentping.NewRun("answer-bot")

// Anthropic
anthropicHTTP := instrumentation.AnthropicHTTPClient(http.DefaultTransport, run)

// OpenAI
openaiHTTP := instrumentation.OpenAIHTTPClient(http.DefaultTransport, run)

// Hand the *http.Client to the provider SDK's constructor option.
```

Streaming responses are wrapped transparently: chunks flow through to
your code without delay and the SDK emits a single `llm_call` event when
the stream closes, with accumulated token counts.

The SDK never sends `cost_usd` on `llm_call` events; the server computes
cost from the rate card.

Per-integration docs:
[Anthropic](https://agentping.io/docs/providers/anthropic) ·
[OpenAI](https://agentping.io/docs/providers/openai). Other providers
are on the roadmap; until then, wrap-and-emit recipes for Gemini,
Mistral, Cohere, and Bedrock work fine, see
[the docs](https://agentping.io/docs/sdks/go) for examples.

## Configuration

| Option | Default | Description |
| --- | --- | --- |
| `WithAPIKey` | `AGENTPING_API_KEY` env | `apk_<region>_<32 hex>` |
| `WithBaseURL` | `AGENTPING_BASE_URL` env, otherwise derived from the key's region: `https://{region}.ingest.agentping.io` | Override for staging or self-hosted |
| `WithControlURL` | `AGENTPING_CONTROL_URL` env, otherwise region-derived (EU: `https://agentping.io`) | Control plane for `GuardCheck` |
| `WithQueueSize` | 1000 | Max envelopes held in memory |
| `WithFlushInterval` | 2s | How often the background worker drains |
| `WithBatchSize` | 50 | Max events per HTTP POST |
| `WithHTTPClient` | `&http.Client{}` | Inject your own client (useful for tests) |

## Contract guarantees

- Telemetry never blocks user code. Every HTTP call runs in a single background goroutine.
- Every HTTP request carries a 2 second hard timeout.
- The local queue is bounded; on overflow the oldest envelope is dropped and `Status().DroppedCount` increments.
- The worker retries with exponential back-off up to five times, then drops. `Retry-After` is honoured on 429.
- One warning per error class is written to stderr for the life of the process.
- `Shutdown` drains the queue under the caller's context. If you skip it, in-flight events may be lost when the process exits.

## Status

```go
s := agentping.Status()
fmt.Println(s.QueueSize, s.DroppedCount, s.LastFlushAt, s.LastError)
```

## License

MIT.
