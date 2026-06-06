// Package instrumentation provides http.RoundTripper wrappers that observe
// outbound calls to provider APIs (Anthropic, OpenAI) and emit llm_call events
// onto a bound agentping.Run.
//
// Go's HTTP transports compose cleanly, so instrumentation is a thin layer:
// you build a transport with NewAnthropicTransport or NewOpenAITransport, hand
// it to the provider client's http.Client, and every call is observed without
// any reflection or monkey-patching.
//
// For convenience, InstrumentAnthropic and InstrumentOpenAI take any client
// whose constructor accepts a custom *http.Client and return the same client
// re-wired with the instrumented transport.
package instrumentation
