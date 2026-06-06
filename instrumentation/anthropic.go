package instrumentation

import "net/http"

// AnthropicHTTPClient returns an *http.Client whose transport observes
// Anthropic API calls and emits llm_call events on the given run. Hand it to
// the Anthropic SDK's constructor option, for example:
//
//	client := anthropic.NewClient(option.WithHTTPClient(
//	    instrumentation.AnthropicHTTPClient(http.DefaultTransport, run),
//	))
//
// A nil run leaves the client functional but disables event emission, which is
// useful for code paths where instrumentation is conditional.
func AnthropicHTTPClient(base http.RoundTripper, run EventEmitter) *http.Client {
	return &http.Client{Transport: NewAnthropicTransport(base, run)}
}
