# Changelog

All notable changes to the AgentPing Go SDK are documented here. The
format is based on [Keep a Changelog](https://keepachangelog.com/), and
this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `WithDefaultVersion` on Init and `WithVersion` on NewRun label every run with
  the build or prompt version that produced it (`AGENTPING_VERSION` also
  works). The dashboard's Spend and Verify charts mark where each new version
  was first seen.
- Streamed Anthropic and OpenAI responses report `ttft_ms`, the time from the
  request to the first content chunk, charted on Pulse as time to first token.
- `instrumentation.WithContentCapture(maxChars)` on the transports sends the
  prompt and completion on `llm_call` events: `input` as a list of
  {role, content} messages, `output` as text and `system_instructions`. Off by
  default; each piece is clipped at the cap (4000 when zero). The request body
  is read and restored, so the provider sees it unchanged.
- Events carry `turn`, and `Conversation` models a chat session as one run
  (from the previous unreleased commits).

## [v0.1.0] - 2026-05-17

Initial public release.

### Added

- Core run lifecycle. `agentping.Init(opts...)`, `agentping.NewRun(name,
  opts...)`, `run.Event(type, payload)`, `run.Finish(status, opts...)`.
- Client-generated UUIDv7 run IDs. `run.ID` is populated synchronously
  before any network call.
- `agentping.Heartbeat(agent, status, opts...)` for cron-shaped jobs.
- Bounded background queue (default 1000), drop-oldest on overflow,
  exposed via `agentping.Status()`.
- `agentping.Shutdown(ctx)` drains the queue under the caller's
  context. If skipped, in-flight events may be lost on process exit.
- Region-aware default base URL. `apk_eu_*` keys route to
  `https://eu.ingest.agentping.io`; `apk_us_*` keys route to
  `https://us.ingest.agentping.io`. Override via `AGENTPING_BASE_URL`
  or `WithBaseURL`.

### Auto-instrumentation

- `instrumentation.AnthropicHTTPClient(transport, run)` and
  `instrumentation.OpenAIHTTPClient(transport, run)` return an
  `*http.Client` that intercepts provider traffic and emits an
  `llm_call` event onto the bound run. Streaming responses are
  wrapped transparently; the event fires once when the stream closes
  with accumulated token counts.

### Distribution

- Module path is `github.com/agent-ping/agent-ping-go`. Stdlib only;
  no external runtime dependencies.
- Go 1.22 or later.

## Notes on stability

The 0.x line is pre-1.0. Public API may change before 1.0.0. We do not
break the wire format between SDK and ingest without a version bump and
a migration note here.

[Unreleased]: https://github.com/agent-ping/agent-ping-go/compare/v0.1.0...HEAD
[v0.1.0]: https://github.com/agent-ping/agent-ping-go/releases/tag/v0.1.0
