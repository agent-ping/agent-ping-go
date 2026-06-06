package instrumentation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type recordedEvent struct {
	eventType string
	data      map[string]any
}

type fakeRun struct {
	mu     sync.Mutex
	events []recordedEvent
}

func (f *fakeRun) Event(t string, d map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, recordedEvent{eventType: t, data: d})
	return nil
}

func (f *fakeRun) snapshot() []recordedEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]recordedEvent, len(f.events))
	copy(out, f.events)
	return out
}

func TestAnthropicTransportEmitsLLMCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/messages") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "msg_123",
			"model": "claude-sonnet-4-5",
			"usage": map[string]any{
				"input_tokens":                1024,
				"output_tokens":               312,
				"cache_read_input_tokens":     100,
				"cache_creation_input_tokens": 50,
			},
		})
	}))
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewAnthropicTransport(http.DefaultTransport, run)}

	resp, err := client.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	events := run.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	d := events[0].data
	if events[0].eventType != "llm_call" {
		t.Errorf("wrong event type: %s", events[0].eventType)
	}
	if d["provider"] != "anthropic" {
		t.Errorf("provider wrong: %v", d["provider"])
	}
	if d["model"] != "claude-sonnet-4-5" {
		t.Errorf("model wrong: %v", d["model"])
	}
	if d["input_tokens"] != 1024 {
		t.Errorf("input_tokens wrong: %v", d["input_tokens"])
	}
	if d["output_tokens"] != 312 {
		t.Errorf("output_tokens wrong: %v", d["output_tokens"])
	}
	if d["cached_input_tokens"] != 100 {
		t.Errorf("cached_input_tokens wrong: %v", d["cached_input_tokens"])
	}
	if d["cache_creation_input_tokens"] != 50 {
		t.Errorf("cache_creation_input_tokens wrong: %v", d["cache_creation_input_tokens"])
	}
	if _, ok := d["cost_usd"]; ok {
		t.Error("SDK must never send cost_usd on llm_call")
	}
	if _, ok := d["latency_ms"]; !ok {
		t.Error("latency_ms missing")
	}
}

func TestAnthropicTransportSkipsNonLLMPaths(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewAnthropicTransport(http.DefaultTransport, run)}
	resp, err := client.Get(srv.URL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if got := len(run.snapshot()); got != 0 {
		t.Fatalf("expected 0 events for non-LLM path, got %d", got)
	}
}

func TestAnthropicTransportStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		// Simulate SSE chunks from the Anthropic stream API.
		chunks := []string{
			`{"type":"message_start","message":{"model":"claude-sonnet-4-5","usage":{"input_tokens":120,"output_tokens":0}}}`,
			`{"type":"content_block_delta","delta":{"text":"hi"}}`,
			`{"type":"message_delta","usage":{"output_tokens":42}}`,
		}
		for _, c := range chunks {
			_, _ = w.Write([]byte("data: " + c + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewAnthropicTransport(http.DefaultTransport, run)}
	resp, err := client.Post(srv.URL+"/v1/messages", "application/json", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	events := run.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event from stream, got %d", len(events))
	}
	d := events[0].data
	if d["model"] != "claude-sonnet-4-5" {
		t.Errorf("model wrong: %v", d["model"])
	}
	if d["input_tokens"] != 120 {
		t.Errorf("input_tokens wrong: %v", d["input_tokens"])
	}
	if d["output_tokens"] != 42 {
		t.Errorf("output_tokens wrong: %v", d["output_tokens"])
	}
}
