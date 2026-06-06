package agentping

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPClientPostSuccess(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer apk_eu_test" {
			t.Errorf("missing/incorrect Authorization header: %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("expected JSON content type, got %q", got)
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"accepted":1}`))
	}))
	defer srv.Close()

	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), 0)
	res := c.post(context.Background(), "/v1/runs", map[string]any{"hello": "world"})
	if !res.ok || res.status != 202 {
		t.Fatalf("expected ok 202, got ok=%v status=%d err=%v", res.ok, res.status, res.err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Fatalf("expected 1 server hit, got %d", hits)
	}
}

func TestHTTPClientPostAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
	}))
	defer srv.Close()
	resetWarningsForTest()
	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), 0)
	res := c.post(context.Background(), "/v1/runs", map[string]any{})
	if res.ok {
		t.Fatal("expected failure")
	}
	if res.errClass != errClassAuth {
		t.Fatalf("expected auth_error class, got %q", res.errClass)
	}
}

func TestHTTPClientPost429RetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(429)
	}))
	defer srv.Close()
	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), 0)
	res := c.post(context.Background(), "/v1/runs", map[string]any{})
	if res.ok {
		t.Fatal("expected failure")
	}
	if res.retryAfter != 3*time.Second {
		t.Fatalf("expected 3s retry-after, got %v", res.retryAfter)
	}
}

func TestHTTPClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(202)
	}))
	defer srv.Close()
	c := newHTTPClient("apk_eu_test", srv.URL, srv.Client(), 50*time.Millisecond)
	start := time.Now()
	res := c.post(context.Background(), "/v1/runs", map[string]any{})
	elapsed := time.Since(start)
	if res.ok {
		t.Fatal("expected timeout failure")
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("timeout did not fire in time: %v", elapsed)
	}
}

func TestParseRetryAfterSeconds(t *testing.T) {
	if d := parseRetryAfter("5"); d != 5*time.Second {
		t.Errorf("expected 5s, got %v", d)
	}
	if d := parseRetryAfter("0"); d != 0 {
		t.Errorf("expected 0, got %v", d)
	}
	if d := parseRetryAfter("bogus"); d != 0 {
		t.Errorf("expected 0 for bogus, got %v", d)
	}
}
