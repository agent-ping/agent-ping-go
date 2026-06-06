package instrumentation

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/agent-ping/agent-ping-go/internal/jsonutil"
)

// EventEmitter is implemented by *agentping.Run. Defined locally so this
// package does not pull in the public package and create a cycle.
type EventEmitter interface {
	Event(eventType string, data map[string]any) error
}

// Provider identifies which response shape the transport should look for.
type Provider string

const (
	// ProviderAnthropic targets the Anthropic Messages API.
	ProviderAnthropic Provider = "anthropic"
	// ProviderOpenAI targets the OpenAI Chat Completions API.
	ProviderOpenAI Provider = "openai"
)

type instrumentingTransport struct {
	base     http.RoundTripper
	provider Provider
	run      EventEmitter

	// nowFn is overridable for tests.
	nowFn func() time.Time
}

// NewAnthropicTransport returns an http.RoundTripper that observes Anthropic
// responses and emits llm_call events on the given run. A nil run disables
// event emission while leaving the transport functional.
func NewAnthropicTransport(base http.RoundTripper, run EventEmitter) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &instrumentingTransport{base: base, provider: ProviderAnthropic, run: run, nowFn: time.Now}
}

// NewOpenAITransport returns an http.RoundTripper that observes OpenAI
// responses and emits llm_call events on the given run.
func NewOpenAITransport(base http.RoundTripper, run EventEmitter) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &instrumentingTransport{base: base, provider: ProviderOpenAI, run: run, nowFn: time.Now}
}

func (t *instrumentingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := t.nowFn()
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return resp, err
	}
	if t.run == nil || resp == nil {
		return resp, nil
	}
	if !t.isLLMCall(req) {
		return resp, nil
	}
	if isEventStream(resp) {
		resp.Body = newStreamingBody(resp.Body, t, start)
		return resp, nil
	}
	resp.Body = newBufferingBody(resp.Body, t, start)
	return resp, nil
}

func (t *instrumentingTransport) isLLMCall(req *http.Request) bool {
	path := req.URL.Path
	switch t.provider {
	case ProviderAnthropic:
		return strings.Contains(path, "/messages")
	case ProviderOpenAI:
		return strings.Contains(path, "/chat/completions") || strings.Contains(path, "/responses")
	}
	return false
}

func isEventStream(resp *http.Response) bool {
	ct := resp.Header.Get("Content-Type")
	return strings.Contains(strings.ToLower(ct), "text/event-stream")
}

func (t *instrumentingTransport) emitFromBody(buf []byte, start time.Time) {
	body := jsonutil.Decode(bytes.NewReader(buf))
	data := map[string]any{
		"provider":   string(t.provider),
		"latency_ms": int(t.nowFn().Sub(start) / time.Millisecond),
	}
	model, _ := jsonutil.GetString(body, "model")
	if model != "" {
		data["model"] = model
	}
	switch t.provider {
	case ProviderAnthropic:
		fillAnthropic(data, body)
	case ProviderOpenAI:
		fillOpenAI(data, body)
	}
	_ = t.run.Event("llm_call", data)
}

func fillAnthropic(data map[string]any, body any) {
	if v, ok := jsonutil.GetInt(body, "usage", "input_tokens"); ok {
		data["input_tokens"] = v
	}
	if v, ok := jsonutil.GetInt(body, "usage", "output_tokens"); ok {
		data["output_tokens"] = v
	}
	if v, ok := jsonutil.GetInt(body, "usage", "cache_read_input_tokens"); ok {
		data["cached_input_tokens"] = v
	}
	if v, ok := jsonutil.GetInt(body, "usage", "cache_creation_input_tokens"); ok {
		data["cache_creation_input_tokens"] = v
	}
}

func fillOpenAI(data map[string]any, body any) {
	if v, ok := jsonutil.GetInt(body, "usage", "prompt_tokens"); ok {
		data["input_tokens"] = v
	}
	if v, ok := jsonutil.GetInt(body, "usage", "completion_tokens"); ok {
		data["output_tokens"] = v
	}
	if v, ok := jsonutil.GetInt(body, "usage", "prompt_tokens_details", "cached_tokens"); ok {
		data["cached_input_tokens"] = v
	}
}

type bufferingBody struct {
	src   io.ReadCloser
	buf   bytes.Buffer
	tee   io.Reader
	t     *instrumentingTransport
	start time.Time
	once  sync.Once
}

func newBufferingBody(src io.ReadCloser, t *instrumentingTransport, start time.Time) *bufferingBody {
	b := &bufferingBody{src: src, t: t, start: start}
	b.tee = io.TeeReader(src, &b.buf)
	return b
}

func (b *bufferingBody) Read(p []byte) (int, error) {
	return b.tee.Read(p)
}

func (b *bufferingBody) Close() error {
	b.once.Do(func() {
		b.t.emitFromBody(b.buf.Bytes(), b.start)
	})
	return b.src.Close()
}

type streamingBody struct {
	src      io.ReadCloser
	t        *instrumentingTransport
	start    time.Time
	leftover []byte
	usage    streamUsage
	once     sync.Once
}

type streamUsage struct {
	model                    string
	inputTokens              int
	outputTokens             int
	cachedInputTokens        int
	cacheCreationInputTokens int
	haveInput                bool
	haveOutput               bool
	haveCached               bool
	haveCacheCreation        bool
}

func newStreamingBody(src io.ReadCloser, t *instrumentingTransport, start time.Time) *streamingBody {
	return &streamingBody{src: src, t: t, start: start}
}

func (s *streamingBody) Read(p []byte) (int, error) {
	n, err := s.src.Read(p)
	if n > 0 {
		s.consume(p[:n])
	}
	return n, err
}

func (s *streamingBody) consume(chunk []byte) {
	combined := append(s.leftover, chunk...)
	scanner := bufio.NewScanner(bytes.NewReader(combined))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	last := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		last += len(line) + 1
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(line[len("data:"):])
		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		body := jsonutil.Decode(bytes.NewReader(payload))
		s.mergeUsage(body)
	}
	if last > len(combined) {
		last = len(combined)
	}
	s.leftover = append(s.leftover[:0], combined[last:]...)
}

func (s *streamingBody) mergeUsage(body any) {
	if m, ok := body.(map[string]any); ok {
		if model, ok := m["model"].(string); ok && model != "" {
			s.usage.model = model
		}
		if msg, ok := m["message"].(map[string]any); ok {
			if model, ok := msg["model"].(string); ok && model != "" {
				s.usage.model = model
			}
			if u, ok := msg["usage"].(map[string]any); ok {
				s.mergeUsageMap(u)
			}
		}
	}
	switch s.t.provider {
	case ProviderAnthropic:
		if v, ok := jsonutil.GetInt(body, "usage", "input_tokens"); ok {
			s.usage.inputTokens = v
			s.usage.haveInput = true
		}
		if v, ok := jsonutil.GetInt(body, "usage", "output_tokens"); ok {
			s.usage.outputTokens += v
			s.usage.haveOutput = true
		}
		if v, ok := jsonutil.GetInt(body, "usage", "cache_read_input_tokens"); ok {
			s.usage.cachedInputTokens = v
			s.usage.haveCached = true
		}
		if v, ok := jsonutil.GetInt(body, "usage", "cache_creation_input_tokens"); ok {
			s.usage.cacheCreationInputTokens = v
			s.usage.haveCacheCreation = true
		}
	case ProviderOpenAI:
		if v, ok := jsonutil.GetInt(body, "usage", "prompt_tokens"); ok {
			s.usage.inputTokens = v
			s.usage.haveInput = true
		}
		if v, ok := jsonutil.GetInt(body, "usage", "completion_tokens"); ok {
			s.usage.outputTokens = v
			s.usage.haveOutput = true
		}
		if v, ok := jsonutil.GetInt(body, "usage", "prompt_tokens_details", "cached_tokens"); ok {
			s.usage.cachedInputTokens = v
			s.usage.haveCached = true
		}
	}
}

func (s *streamingBody) mergeUsageMap(u map[string]any) {
	switch s.t.provider {
	case ProviderAnthropic:
		if v, ok := jsonutil.GetInt(u, "input_tokens"); ok {
			s.usage.inputTokens = v
			s.usage.haveInput = true
		}
		if v, ok := jsonutil.GetInt(u, "output_tokens"); ok {
			s.usage.outputTokens += v
			s.usage.haveOutput = true
		}
		if v, ok := jsonutil.GetInt(u, "cache_read_input_tokens"); ok {
			s.usage.cachedInputTokens = v
			s.usage.haveCached = true
		}
		if v, ok := jsonutil.GetInt(u, "cache_creation_input_tokens"); ok {
			s.usage.cacheCreationInputTokens = v
			s.usage.haveCacheCreation = true
		}
	}
}

func (s *streamingBody) Close() error {
	s.once.Do(func() {
		data := map[string]any{
			"provider":   string(s.t.provider),
			"latency_ms": int(s.t.nowFn().Sub(s.start) / time.Millisecond),
		}
		if s.usage.model != "" {
			data["model"] = s.usage.model
		}
		if s.usage.haveInput {
			data["input_tokens"] = s.usage.inputTokens
		}
		if s.usage.haveOutput {
			data["output_tokens"] = s.usage.outputTokens
		}
		if s.usage.haveCached {
			data["cached_input_tokens"] = s.usage.cachedInputTokens
		}
		if s.usage.haveCacheCreation {
			data["cache_creation_input_tokens"] = s.usage.cacheCreationInputTokens
		}
		_ = s.t.run.Event("llm_call", data)
	})
	return s.src.Close()
}
