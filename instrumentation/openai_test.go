package instrumentation

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenAITransportEmitsLLMCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/chat/completions") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl_123",
			"model": "gpt-4.1-mini",
			"usage": map[string]any{
				"prompt_tokens":     800,
				"completion_tokens": 200,
				"prompt_tokens_details": map[string]any{
					"cached_tokens": 60,
				},
			},
		})
	}))
	defer srv.Close()

	run := &fakeRun{}
	client := &http.Client{Transport: NewOpenAITransport(http.DefaultTransport, run)}
	resp, err := client.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	events := run.snapshot()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	d := events[0].data
	if d["provider"] != "openai" {
		t.Errorf("provider wrong: %v", d["provider"])
	}
	if d["model"] != "gpt-4.1-mini" {
		t.Errorf("model wrong: %v", d["model"])
	}
	if d["input_tokens"] != 800 {
		t.Errorf("input_tokens wrong: %v", d["input_tokens"])
	}
	if d["output_tokens"] != 200 {
		t.Errorf("output_tokens wrong: %v", d["output_tokens"])
	}
	if d["cached_input_tokens"] != 60 {
		t.Errorf("cached_input_tokens wrong: %v", d["cached_input_tokens"])
	}
	if _, ok := d["cost_usd"]; ok {
		t.Error("SDK must never send cost_usd on llm_call")
	}
}

func TestOpenAIHTTPClientHelper(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4.1-mini","usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	run := &fakeRun{}
	c := OpenAIHTTPClient(http.DefaultTransport, run)
	resp, err := c.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if len(run.snapshot()) != 1 {
		t.Fatalf("expected event emitted via helper")
	}
}
