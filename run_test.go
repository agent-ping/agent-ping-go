package agentping

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewRunIDPopulatedSynchronously(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(202)
	}))
	defer srv.Close()
	t.Cleanup(clearStateForTest)

	if err := Init(
		WithAPIKey("apk_eu_00000000000000000000000000000000"),
		WithBaseURL(srv.URL),
	); err != nil {
		t.Fatalf("init: %v", err)
	}

	r := NewRun("agent-x", WithCustomerID("acme"))
	if r.ID == "" {
		t.Fatal("run id not populated")
	}
	if !strings.HasPrefix(r.ID, "run_eu_") {
		t.Fatalf("expected eu prefix, got %s", r.ID)
	}
	if !isValidID(r.ID) {
		t.Fatalf("invalid run id: %s", r.ID)
	}
}

func TestRunLifecycle(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		bodies = append(bodies, b)
		mu.Unlock()
		w.WriteHeader(202)
	}))
	defer srv.Close()
	t.Cleanup(clearStateForTest)

	if err := Init(
		WithAPIKey("apk_us_00000000000000000000000000000000"),
		WithBaseURL(srv.URL),
		WithFlushInterval(30*time.Millisecond),
		WithBatchSize(10),
	); err != nil {
		t.Fatalf("init: %v", err)
	}

	r := NewRun("triage", WithFeature("routing"), WithMetadata(map[string]any{"ticket": "T1"}))
	if err := r.Event("log", map[string]any{"msg": "classified"}); err != nil {
		t.Fatalf("event: %v", err)
	}
	if err := r.Event("llm_call", map[string]any{
		"provider": "anthropic", "model": "claude-sonnet-4-5",
		"input_tokens": 100, "output_tokens": 50, "latency_ms": 1234,
	}); err != nil {
		t.Fatalf("event: %v", err)
	}
	if err := r.Finish(FinishStatusSuccess, WithScores(map[string]any{"confidence": 0.9})); err != nil {
		t.Fatalf("finish: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(paths) < 3 {
		t.Fatalf("expected at least 3 calls (start, events, finish), got %d: %v", len(paths), paths)
	}
	if paths[0] != "/v1/runs" {
		t.Fatalf("expected start path /v1/runs, got %s", paths[0])
	}
	foundEvents, foundFinish := false, false
	for _, p := range paths[1:] {
		if strings.HasSuffix(p, "/events") {
			foundEvents = true
		}
		if strings.HasSuffix(p, "/finish") {
			foundFinish = true
		}
	}
	if !foundEvents {
		t.Errorf("expected an /events path: %v", paths)
	}
	if !foundFinish {
		t.Errorf("expected a /finish path: %v", paths)
	}

	// Verify the run-start body shape.
	start := bodies[0]
	if start["agent"] != "triage" {
		t.Errorf("agent missing or wrong: %v", start)
	}
	if start["feature"] != "routing" {
		t.Errorf("feature missing or wrong: %v", start)
	}
	if _, ok := start["started_at"].(string); !ok {
		t.Errorf("started_at missing: %v", start)
	}
}

func TestRunFinishIdempotent(t *testing.T) {
	var finishes int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/finish") {
			atomic.AddInt32(&finishes, 1)
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()
	t.Cleanup(clearStateForTest)

	_ = Init(WithAPIKey("apk_eu_00000000000000000000000000000000"), WithBaseURL(srv.URL),
		WithFlushInterval(30*time.Millisecond))

	r := NewRun("x")
	_ = r.Finish(FinishStatusSuccess)
	_ = r.Finish(FinishStatusFailed)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = Flush(ctx)

	if got := atomic.LoadInt32(&finishes); got != 1 {
		t.Fatalf("expected exactly 1 finish call, got %d", got)
	}
}

func TestEventBeforeInitReturnsNotInitialised(t *testing.T) {
	clearStateForTest()
	r := &Run{ID: "run_eu_00000000000000000000000000000000"}
	if err := r.Event("log", nil); err != ErrNotInitialized {
		t.Fatalf("expected ErrNotInitialized, got %v", err)
	}
}

func TestStatusUninitialised(t *testing.T) {
	clearStateForTest()
	s := Status()
	if s.Initialised {
		t.Fatal("expected not initialised")
	}
	if s.QueueSize != 0 || s.DroppedCount != 0 {
		t.Fatalf("expected zero values: %+v", s)
	}
}
