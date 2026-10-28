package instrumentation

import "net/http"

// OpenAIHTTPClient returns an *http.Client whose transport observes OpenAI
// API calls and emits llm_call events on the given run. Hand it to the OpenAI
// SDK's constructor option, for example:
//
//	client := openai.NewClient(option.WithHTTPClient(
//	    instrumentation.OpenAIHTTPClient(http.DefaultTransport, run),
//	))
func OpenAIHTTPClient(base http.RoundTripper, run EventEmitter, opts ...TransportOption) *http.Client {
	return &http.Client{Transport: NewOpenAITransport(base, run, opts...)}
}
