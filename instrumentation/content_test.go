package instrumentation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func anthropicJSONServer(t *testing.T, gotBody *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model": "claude-haiku-5-5",
			"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
			"content": []any{
				map[string]any{"type": "text", "text": "Route to billing."},
				map[string]any{"type": "tool_use", "name": "lookup"},
			},
		})
	}))
}

const anthropicRequest = `{"model":"claude-haiku-5-5","system":[{"type":"text","text":"Be terse."}],` +
	`"messages":[{"role":"user","content":[{"type":"text","text":"Refund request from Acme"},{"type":"image","source":{}}]},` +
	`{"role":"assistant","content":"Checking."}]}`

func TestContentIsNotSentByDefault(t *testing.T) {
	var seen string
	srv := anthropicJSONServer(t, &seen)
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewAnthropicTransport(http.DefaultTransport, run)}
	resp, err := client.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(anthropicRequest))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	d := run.snapshot()[0].data
	for _, key := range []string{"input", "output", "system_instructions", "ttft_ms"} {
		if _, ok := d[key]; ok {
			t.Errorf("%s must not be sent without WithContentCapture: %v", key, d)
		}
	}
	if seen != anthropicRequest {
		t.Errorf("request body must reach the provider untouched: %s", seen)
	}
}

func TestContentCaptureSendsPromptSystemAndOutputClipped(t *testing.T) {
	var seen string
	srv := anthropicJSONServer(t, &seen)
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewAnthropicTransport(http.DefaultTransport, run, WithContentCapture(12))}
	resp, err := client.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(anthropicRequest))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if seen != anthropicRequest {
		t.Fatalf("request body must be restored after reading the prompt: %s", seen)
	}
	d := run.snapshot()[0].data
	if d["system_instructions"] != "Be terse." {
		t.Errorf("system_instructions: %v", d["system_instructions"])
	}
	if d["output"] != "Route to bil..." {
		t.Errorf("output must be the text blocks clipped at 12: %v", d["output"])
	}
	input, ok := d["input"].([]map[string]any)
	if !ok || len(input) != 2 {
		t.Fatalf("input: %#v", d["input"])
	}
	if input[0]["role"] != "user" || input[0]["content"] != "Refund reque..." {
		t.Errorf("first message: %v", input[0])
	}
	if input[1]["role"] != "assistant" || input[1]["content"] != "Checking." {
		t.Errorf("second message: %v", input[1])
	}
}

func TestAnthropicStreamReportsTTFTAndCapturedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(c string) {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`{"type":"message_start","message":{"model":"claude-haiku-5-5","usage":{"input_tokens":8,"output_tokens":0}}}`)
		time.Sleep(20 * time.Millisecond)
		write(`{"type":"content_block_start","content_block":{"type":"text"}}`)
		write(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"Route "}}`)
		write(`{"type":"content_block_delta","delta":{"type":"text_delta","text":"to billing."}}`)
		write(`{"type":"message_delta","usage":{"output_tokens":4}}`)
	}))
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewAnthropicTransport(http.DefaultTransport, run, WithContentCapture(0))}
	resp, err := client.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	d := run.snapshot()[0].data
	ttft, ok := d["ttft_ms"].(int)
	if !ok {
		t.Fatalf("ttft_ms missing: %v", d)
	}
	if ttft < 15 || ttft > d["latency_ms"].(int) {
		t.Errorf("ttft_ms %d should sit between the first content chunk and the end of the stream (%v)", ttft, d["latency_ms"])
	}
	if d["output"] != "Route to billing." {
		t.Errorf("output: %v", d["output"])
	}
	if d["output_tokens"] != 4 {
		t.Errorf("output_tokens: %v", d["output_tokens"])
	}
}

func TestOpenAIChatStreamReportsTTFTAndCapturesDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range []string{
			`{"model":"gpt-5","choices":[{"delta":{"role":"assistant","content":""}}]}`,
			`{"model":"gpt-5","choices":[{"delta":{"content":"Route "}}]}`,
			`{"model":"gpt-5","choices":[{"delta":{"content":"to billing."},"finish_reason":"stop"}]}`,
			`{"model":"gpt-5","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":12}}`,
		} {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewOpenAITransport(http.DefaultTransport, run, WithContentCapture(0))}
	body := `{"model":"gpt-5","stream":true,"messages":[{"role":"system","content":"Be terse."},{"role":"user","content":"hi"}]}`
	resp, err := client.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	d := run.snapshot()[0].data
	if _, ok := d["ttft_ms"].(int); !ok {
		t.Fatalf("ttft_ms missing: %v", d)
	}
	if d["output"] != "Route to billing." {
		t.Errorf("output: %v", d["output"])
	}
	input := d["input"].([]map[string]any)
	if len(input) != 2 || input[0]["role"] != "system" || input[1]["content"] != "hi" {
		t.Errorf("input: %v", input)
	}
	if d["input_tokens"] != 30 || d["output_tokens"] != 12 {
		t.Errorf("usage: %v", d)
	}
}

func TestOpenAIResponsesCaptureInputInstructionsAndOutputText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-5","status":"completed","output_text":"Route to billing.","output":[],"usage":{"input_tokens":10,"output_tokens":5}}`))
	}))
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewOpenAITransport(http.DefaultTransport, run, WithContentCapture(0))}
	resp, err := client.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5","instructions":"Be terse.","input":"Refund request from Acme"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	d := run.snapshot()[0].data
	if d["system_instructions"] != "Be terse." || d["output"] != "Route to billing." {
		t.Errorf("content: %v", d)
	}
	input := d["input"].([]map[string]any)
	if len(input) != 1 || input[0]["role"] != "user" || input[0]["content"] != "Refund request from Acme" {
		t.Errorf("input: %v", input)
	}
	if _, ok := d["ttft_ms"]; ok {
		t.Errorf("ttft_ms is for streams only: %v", d)
	}
}
