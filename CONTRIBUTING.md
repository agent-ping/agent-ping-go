# Contributing

Thanks for considering a contribution to the AgentPing Go SDK.

## Reporting a bug

Open a GitHub issue. Useful issues include:

- The version (`go list -m github.com/agent-ping/agent-ping-go`).
- Your Go version (`go version`) and OS.
- A minimal reproduction. Snip the surrounding application code; we
  only need the AgentPing calls and the provider client.
- What you expected to happen, what actually happened.
- The output of `agentping.Status()` if the bug is about telemetry not
  landing.

Security issues should not go on the public tracker. Email
`security@agentping.io` instead.

## Proposing a feature

Open an issue before opening a PR. Most feature work touches the wire
contract with the AgentPing ingest server, so we want to design the
interface together before you spend time implementing it.

If you're adding an `http.RoundTripper` wrapper for a provider we don't
cover yet, see [Instrumentation guidelines](#instrumentation-guidelines)
below.

## Local development

```bash
git clone https://github.com/agent-ping/agent-ping-go
cd agentping-go
go test ./...
```

Go 1.22 or later. The SDK has no external runtime dependencies; tests
use the standard library and `net/http/httptest`.

## Tests

```bash
go test ./...                    # full suite
go test ./...  -run TestRun      # filter by name
go test -race ./...              # race detector
go test -v ./...                 # verbose
```

We expect every PR to add or update tests. HTTP-touching tests use
`httptest.NewServer` and require no credentials or live endpoint.

## Style

- `gofmt` and `go vet` clean. Both run in CI.
- Public API documented with godoc comments. New exported identifiers
  must have at least a sentence of documentation.
- No em-dashes in source, godoc, comments, or commit messages. Use
  commas, semicolons, or sentence breaks. This is a project-wide rule.

## Instrumentation guidelines

When adding an `instrumentation.<Provider>HTTPClient` wrapper:

1. The wrapper returns an `*http.Client` configured with a custom
   `RoundTripper`. The caller passes that client to the provider
   SDK's constructor option.
2. Capture token counts from the response body, not from request
   parameters. Tokens reported pre-call are estimates and produce
   wrong spend numbers.
3. Streaming wrappers must preserve the original response body
   contract: chunks flow through to the caller without buffering,
   and the `llm_call` event is emitted once when the stream closes
   with accumulated counts.
4. Prompt-cache token attribution: split cached and uncached input
   tokens into distinct fields if the provider exposes them. The
   server rate card prices the two differently.
5. The wrapper must not block the caller. Event emission goes onto
   the SDK's background queue; nothing in the request path waits on
   AgentPing's network.

## Commit messages and PRs

- Subject in imperative mood (`add Bedrock InvokeModel coverage`, not
  `added Bedrock InvokeModel coverage`).
- One topic per PR. Bug fixes, refactors, and new instrumentation
  should not be bundled.
- If the PR changes the wire contract with ingest, link the matching
  issue in the AgentPing platform tracker.

## Releasing

Maintainers only. Release flow lives in
`agent-ping/LAUNCH.md` in the AgentPing platform monorepo. A Go tag is
immutable forever; treat publication as one-way.
