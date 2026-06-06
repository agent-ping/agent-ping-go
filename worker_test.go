package agentping

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type recordedRequest struct {
	path string
	body map[string]any
}

func newRecordingServer(t *testing.T, handler func(call int, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]recordedRequest, *sync.Mutex) {
	t.Helper()
	var calls []recordedRequest
	var mu sync.Mutex
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := int(atomic.AddInt32(&n, 1))
		var parsed map[string]any
		_ = json.NewDecoder(r.Body).Decode(&parsed)
		mu.Lock()
		calls = append(calls, recordedRequest{path: r.URL.Path, body: parsed})
		mu.Unlock()
		handler(call, w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &mu
}

func TestWorkerBatchesEvents(t *testing.T) {
	srv, calls, mu := newRecordingServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
	})
	q := newBoundedQueue(200)
	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), time.Second)
	w := newWorker(q, c, 50*time.Millisecond, 50)
	w.start()
	defer w.stop()

	runID := newID("run", "eu")
	for i := 0; i < 75; i++ {
		q.push(envelope{
			kind:  envRunEvent,
			runID: runID,
			event: map[string]any{"id": newID("evt", "eu"), "type": "log", "ts": "x", "data": map[string]any{"i": i}},
		})
	}
	w.notifyEnqueued()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(*calls)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(*calls) != 2 {
		t.Fatalf("expected 2 HTTP calls, got %d", len(*calls))
	}
	for i, c := range *calls {
		evts, ok := c.body["events"].([]any)
		if !ok {
			t.Fatalf("call %d missing events array: %+v", i, c.body)
		}
		if i == 0 && len(evts) != 50 {
			t.Errorf("first batch expected 50, got %d", len(evts))
		}
		if i == 1 && len(evts) != 25 {
			t.Errorf("second batch expected 25, got %d", len(evts))
		}
	}
}

func TestWorkerRetriesServerErrors(t *testing.T) {
	var attempt int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempt, 1)
		if n < 3 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()

	q := newBoundedQueue(50)
	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), time.Second)
	w := newWorker(q, c, 20*time.Millisecond, 10)
	w.start()
	defer w.stop()

	q.push(envelope{kind: envHeartbeat, body: map[string]any{"id": newID("run", "eu"), "agent": "x"}})
	w.notifyEnqueued()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&attempt) >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if atomic.LoadInt32(&attempt) < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", attempt)
	}
	if q.size() != 0 {
		t.Fatalf("expected queue drained after success, got %d", q.size())
	}
}

func TestWorkerDropsOnClientError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()

	q := newBoundedQueue(50)
	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), time.Second)
	w := newWorker(q, c, 20*time.Millisecond, 10)
	w.start()
	defer w.stop()

	q.push(envelope{kind: envHeartbeat, body: map[string]any{"id": newID("run", "eu"), "agent": "x"}})
	w.notifyEnqueued()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if q.droppedCount() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if q.droppedCount() != 1 {
		t.Fatalf("expected 1 dropped event, got %d", q.droppedCount())
	}
	if q.size() != 0 {
		t.Fatalf("expected empty queue, got %d", q.size())
	}
}

func TestWorker429Backoff(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			return
		}
		w.WriteHeader(202)
	}))
	defer srv.Close()

	q := newBoundedQueue(50)
	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), time.Second)
	w := newWorker(q, c, 20*time.Millisecond, 10)
	w.start()
	defer w.stop()

	q.push(envelope{kind: envHeartbeat, body: map[string]any{"id": newID("run", "eu"), "agent": "x"}})
	w.notifyEnqueued()

	start := time.Now()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if q.size() == 0 && atomic.LoadInt32(&attempts) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if q.size() != 0 {
		t.Fatalf("queue not drained: %d", q.size())
	}
	elapsed := time.Since(start)
	if elapsed < 900*time.Millisecond {
		t.Fatalf("worker did not honour Retry-After: elapsed=%v", elapsed)
	}
}

func TestGroupEnvelopes(t *testing.T) {
	batch := []envelope{
		{kind: envRunStart, runID: "r1", body: map[string]any{}},
		{kind: envRunEvent, runID: "r1", event: map[string]any{"i": 1}},
		{kind: envRunEvent, runID: "r1", event: map[string]any{"i": 2}},
		{kind: envRunEvent, runID: "r2", event: map[string]any{"i": 3}},
		{kind: envRunFinish, runID: "r1", body: map[string]any{}},
	}
	groups := groupEnvelopes(batch)
	if len(groups) != 4 {
		t.Fatalf("expected 4 groups, got %d", len(groups))
	}
	if len(groups[1]) != 2 {
		t.Fatalf("expected two events in second group, got %d", len(groups[1]))
	}
}
