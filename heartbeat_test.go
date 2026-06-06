package agentping

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHeartbeatPayload(t *testing.T) {
	var mu sync.Mutex
	var got recordedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		mu.Lock()
		got = recordedRequest{path: r.URL.Path, body: b}
		mu.Unlock()
		w.WriteHeader(202)
	}))
	defer srv.Close()
	t.Cleanup(clearStateForTest)

	_ = Init(
		WithAPIKey("apk_eu_00000000000000000000000000000000"),
		WithBaseURL(srv.URL),
		WithFlushInterval(30*time.Millisecond),
	)

	err := Heartbeat("daily-summary", HeartbeatStatusOK,
		WithCostUSD(0.084),
		WithDurationMs(12300),
		WithHeartbeatMetadata(map[string]any{"items": 47}),
	)
	if err != nil {
		t.Fatalf("heartbeat: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = Flush(ctx)

	mu.Lock()
	defer mu.Unlock()
	if got.path != "/v1/heartbeats" {
		t.Fatalf("expected /v1/heartbeats, got %s", got.path)
	}
	if got.body["agent"] != "daily-summary" {
		t.Errorf("missing agent: %+v", got.body)
	}
	if got.body["status"] != "ok" {
		t.Errorf("missing status: %+v", got.body)
	}
	if got.body["cost_usd"].(float64) != 0.084 {
		t.Errorf("cost_usd mismatch: %v", got.body["cost_usd"])
	}
	md, ok := got.body["metadata"].(map[string]any)
	if !ok || md["items"].(float64) != 47 {
		t.Errorf("metadata wrong: %+v", got.body["metadata"])
	}
	id, _ := got.body["id"].(string)
	if !isValidID(id) {
		t.Errorf("invalid heartbeat id: %s", id)
	}
}

func TestHeartbeatNotInitialised(t *testing.T) {
	clearStateForTest()
	if err := Heartbeat("x", HeartbeatStatusOK); err != ErrNotInitialized {
		t.Fatalf("expected ErrNotInitialized, got %v", err)
	}
}
