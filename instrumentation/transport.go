package instrumentation

import (
	"bufio"
	"bytes"
	"encoding/json"
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
	content  contentOptions

	// nowFn is overridable for tests.
	nowFn func() time.Time
}

// TransportOption configures an instrumenting transport.
type TransportOption func(*instrumentingTransport)

// contentOptions says whether prompt and completion text ride along on
// llm_call events, and how long each piece may be.
type contentOptions struct {
	capture  bool
	maxChars int
}

// DefaultContentMaxChars caps each captured prompt or completion piece.
const DefaultContentMaxChars = 4000

// WithContentCapture sends the prompt and completion on llm_call events:
// `input` as a list of {role, content} messages, `output` as text and
// `system_instructions`, each clipped at maxChars (DefaultContentMaxChars when
// maxChars <= 0). Off by default: turn it on only when prompts may leave your
// infrastructure. The run page renders them as role-labelled messages.
func WithContentCapture(maxChars int) TransportOption {
	if maxChars <= 0 {
		maxChars = DefaultContentMaxChars
	}
	return func(t *instrumentingTransport) { t.content = contentOptions{capture: true, maxChars: maxChars} }
}

// NewAnthropicTransport returns an http.RoundTripper that observes Anthropic
// responses and emits llm_call events on the given run. A nil run disables
// event emission while leaving the transport functional.
func NewAnthropicTransport(base http.RoundTripper, run EventEmitter, opts ...TransportOption) http.RoundTripper {
	return newTransport(base, ProviderAnthropic, run, opts)
}

// NewOpenAITransport returns an http.RoundTripper that observes OpenAI
// responses and emits llm_call events on the given run.
func NewOpenAITransport(base http.RoundTripper, run EventEmitter, opts ...TransportOption) http.RoundTripper {
	return newTransport(base, ProviderOpenAI, run, opts)
}

func newTransport(base http.RoundTripper, provider Provider, run EventEmitter, opts []TransportOption) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	t := &instrumentingTransport{base: base, provider: provider, run: run, nowFn: time.Now}
	for _, o := range opts {
		o(t)
	}
	return t
}

func (t *instrumentingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := t.nowFn()
	var prompt promptFields
	if t.run != nil && t.content.capture && t.isLLMCall(req) {
		prompt = t.readPrompt(req)
	}
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
		resp.Body = newStreamingBody(resp.Body, t, start, prompt)
		return resp, nil
	}
	resp.Body = newBufferingBody(resp.Body, t, start, prompt)
	return resp, nil
}

// promptFields is what capture read from the request: the messages and the
// system prompt, already rendered for the wire.
type promptFields struct {
	input  []map[string]any
	system string
}

// readPrompt copies the request body, restores it for the base transport and
// pulls the prompt out of it. A body that cannot be read leaves the request
// untouched and the event without a prompt.
func (t *instrumentingTransport) readPrompt(req *http.Request) promptFields {
	if req.Body == nil || req.Body == http.NoBody {
		return promptFields{}
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(raw))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(raw)), nil }
	if err != nil {
		return promptFields{}
	}
	body, ok := jsonutil.Decode(bytes.NewReader(raw)).(map[string]any)
	if !ok {
		return promptFields{}
	}
	var fields promptFields
	switch t.provider {
	case ProviderAnthropic:
		fields.input = renderMessages(body["messages"], "user", t.content.maxChars)
		fields.system = clip(messageText(body["system"]), t.content.maxChars)
	case ProviderOpenAI:
		if messages, ok := body["messages"]; ok {
			fields.input = renderMessages(messages, "user", t.content.maxChars)
		} else {
			fields.input = renderMessages(body["input"], "user", t.content.maxChars)
		}
		fields.system = clip(messageText(body["instructions"]), t.content.maxChars)
	}
	return fields
}

// putContent adds the captured prompt and completion to an event.
func (t *instrumentingTransport) putContent(data map[string]any, prompt promptFields, output string) {
	if !t.content.capture {
		return
	}
	if len(prompt.input) > 0 {
		data["input"] = prompt.input
	}
	if prompt.system != "" {
		data["system_instructions"] = prompt.system
	}
	if output != "" {
		data["output"] = clip(output, t.content.maxChars)
	}
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

func (t *instrumentingTransport) emitFromBody(buf []byte, start time.Time, prompt promptFields) {
	body := jsonutil.Decode(bytes.NewReader(buf))
	data := map[string]any{
		"provider":   string(t.provider),
		"latency_ms": int(t.nowFn().Sub(start) / time.Millisecond),
	}
	model, _ := jsonutil.GetString(body, "model")
	if model != "" {
		data["model"] = model
	}
	var output string
	switch t.provider {
	case ProviderAnthropic:
		fillAnthropic(data, body)
		output = anthropicText(body)
	case ProviderOpenAI:
		fillOpenAI(data, body)
		output = openAIText(body)
	}
	t.putContent(data, prompt, output)
	_ = t.run.Event("llm_call", data)
}

// anthropicText joins the text blocks of a Messages response.
func anthropicText(body any) string {
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	blocks, _ := m["content"].([]any)
	var sb strings.Builder
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok || block["type"] != "text" {
			continue
		}
		if text, ok := block["text"].(string); ok {
			sb.WriteString(text)
		}
	}
	return sb.String()
}

// openAIText is the assistant text of a Chat Completions or Responses body.
func openAIText(body any) string {
	if text, ok := jsonutil.GetString(body, "output_text"); ok {
		return text
	}
	if text, ok := jsonutil.GetString(body, "choices", "0", "message", "content"); ok {
		return text
	}
	m, ok := body.(map[string]any)
	if !ok {
		return ""
	}
	if choices, ok := m["choices"].([]any); ok && len(choices) > 0 {
		if text, ok := jsonutil.GetString(choices[0], "message", "content"); ok {
			return text
		}
	}
	// Responses API: output[].content[].text for message items.
	var sb strings.Builder
	items, _ := m["output"].([]any)
	for _, it := range items {
		item, ok := it.(map[string]any)
		if !ok || item["type"] != "message" {
			continue
		}
		parts, _ := item["content"].([]any)
		for _, p := range parts {
			if text, ok := jsonutil.GetString(p, "text"); ok {
				sb.WriteString(text)
			}
		}
	}
	return sb.String()
}

// messageText flattens a message's content: a string as is, a list of parts
// joined, with non-text parts (images, tool calls, tool results) named in
// brackets.
func messageText(content any) string {
	switch c := content.(type) {
	case nil:
		return ""
	case string:
		return c
	case []any:
		pieces := make([]string, 0, len(c))
		for _, part := range c {
			switch p := part.(type) {
			case string:
				pieces = append(pieces, p)
			case map[string]any:
				if text, ok := p["text"].(string); ok {
					pieces = append(pieces, text)
					continue
				}
				if kind, ok := p["type"].(string); ok {
					name, _ := firstString(p, "name", "tool_name", "id")
					if name != "" {
						pieces = append(pieces, "["+kind+" "+name+"]")
					} else {
						pieces = append(pieces, "["+kind+"]")
					}
					continue
				}
				pieces = append(pieces, compactJSON(p))
			default:
				pieces = append(pieces, compactJSON(p))
			}
		}
		return strings.Join(pieces, "\n")
	default:
		return compactJSON(c)
	}
}

// renderMessages turns a provider's message list (or a bare prompt string)
// into the wire's [{role, content}], each clipped.
func renderMessages(messages any, defaultRole string, maxChars int) []map[string]any {
	switch m := messages.(type) {
	case nil:
		return nil
	case string:
		return []map[string]any{{"role": defaultRole, "content": clip(m, maxChars)}}
	case []any:
		out := make([]map[string]any, 0, len(m))
		for _, entry := range m {
			switch e := entry.(type) {
			case string:
				out = append(out, map[string]any{"role": defaultRole, "content": clip(e, maxChars)})
			case map[string]any:
				role := defaultRole
				if r, ok := e["role"].(string); ok && r != "" {
					role = r
				}
				content, ok := e["content"]
				if !ok {
					content = e["parts"]
				}
				out = append(out, map[string]any{"role": role, "content": clip(messageText(content), maxChars)})
			}
		}
		return out
	default:
		return nil
	}
}

func clip(text string, maxChars int) string {
	if maxChars > 0 && len(text) > maxChars {
		return text[:maxChars] + "..."
	}
	return text
}

func compactJSON(v any) string {
	var sb strings.Builder
	if err := json.NewEncoder(&sb).Encode(v); err != nil {
		return ""
	}
	return clip(strings.TrimSpace(sb.String()), 200)
}

func firstString(m map[string]any, keys ...string) (string, bool) {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
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
	src    io.ReadCloser
	buf    bytes.Buffer
	tee    io.Reader
	t      *instrumentingTransport
	start  time.Time
	prompt promptFields
	once   sync.Once
}

func newBufferingBody(src io.ReadCloser, t *instrumentingTransport, start time.Time, prompt promptFields) *bufferingBody {
	b := &bufferingBody{src: src, t: t, start: start, prompt: prompt}
	b.tee = io.TeeReader(src, &b.buf)
	return b
}

func (b *bufferingBody) Read(p []byte) (int, error) {
	return b.tee.Read(p)
}

func (b *bufferingBody) Close() error {
	b.once.Do(func() {
		b.t.emitFromBody(b.buf.Bytes(), b.start, b.prompt)
	})
	return b.src.Close()
}

type streamingBody struct {
	src      io.ReadCloser
	t        *instrumentingTransport
	start    time.Time
	leftover []byte
	usage    streamUsage
	prompt   promptFields
	// firstContent is when the first content chunk (text or a tool call)
	// arrived; zero until then. Its distance from start is ttft_ms.
	firstContent time.Time
	text         strings.Builder
	once         sync.Once
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

func newStreamingBody(src io.ReadCloser, t *instrumentingTransport, start time.Time, prompt promptFields) *streamingBody {
	return &streamingBody{src: src, t: t, start: start, prompt: prompt}
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
		s.observeContent(body)
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

// observeContent marks the first content chunk for ttft_ms and, when capture
// is on, collects the assistant text as it streams.
func (s *streamingBody) observeContent(body any) {
	m, ok := body.(map[string]any)
	if !ok {
		return
	}
	var text string
	var content bool
	switch s.t.provider {
	case ProviderAnthropic:
		kind, _ := m["type"].(string)
		content = kind == "content_block_start" || kind == "content_block_delta"
		if kind == "content_block_delta" {
			if delta, ok := m["delta"].(map[string]any); ok {
				if dt, _ := delta["type"].(string); dt == "" || dt == "text_delta" {
					text, _ = delta["text"].(string)
				}
			}
		}
	case ProviderOpenAI:
		if kind, ok := m["type"].(string); ok && kind != "" {
			// Responses API events.
			content = strings.HasSuffix(kind, ".delta") || kind == "response.output_item.added"
			if kind == "response.output_text.delta" {
				text, _ = m["delta"].(string)
			}
		} else if choices, ok := m["choices"].([]any); ok && len(choices) > 0 {
			if delta, ok := choices[0].(map[string]any)["delta"].(map[string]any); ok {
				text, _ = delta["content"].(string)
				if calls, ok := delta["tool_calls"].([]any); ok && len(calls) > 0 {
					content = true
				}
			}
		}
	}
	if text != "" {
		content = true
		if s.t.content.capture {
			s.text.WriteString(text)
		}
	}
	if content && s.firstContent.IsZero() {
		s.firstContent = s.t.nowFn()
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
		if !s.firstContent.IsZero() {
			ttft := int(s.firstContent.Sub(s.start) / time.Millisecond)
			if ttft < 0 {
				ttft = 0
			}
			data["ttft_ms"] = ttft
		}
		s.t.putContent(data, s.prompt, s.text.String())
		_ = s.t.run.Event("llm_call", data)
	})
	return s.src.Close()
}
