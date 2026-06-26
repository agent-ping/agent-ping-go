package agentping

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func verdictJSON(decision string) string {
	blockedBy := "[]"
	gck := "null"
	if decision == "block" {
		blockedBy = `["grl_eu_x"]`
		gck = `"gck_eu_1"`
	}
	return `{"decision":"` + decision + `","checked_at":"2026-06-15T09:14:22Z","blocked_by":` +
		blockedBy + `,"guard_check_id":` + gck + `,"rules":[],"stale_as_of":"2026-06-15T09:13:50Z","active":true,"inactive_reason":null}`
}

func initGuard(t *testing.T, srv *httptest.Server) {
	t.Helper()
	resetWarningsForTest()
	t.Cleanup(clearStateForTest)
	if err := Init(WithAPIKey("apk_eu_test"), WithBaseURL(srv.URL), WithControlURL(srv.URL), WithHTTPClient(srv.Client())); err != nil {
		t.Fatalf("init: %v", err)
	}
}

func TestGuardAllowReturnsVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, verdictJSON("allow"))
	}))
	defer srv.Close()
	initGuard(t, srv)

	v, err := GuardCheck(context.Background(), GuardRequest{Agent: "nightly"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.Blocked() {
		t.Fatalf("expected allow, got block")
	}
}

func TestGuardHardModeReturnsPausedOnBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, verdictJSON("block"))
	}))
	defer srv.Close()
	initGuard(t, srv)

	_, err := GuardCheck(context.Background(), GuardRequest{Agent: "nightly"})
	var p *Paused
	if !errors.As(err, &p) {
		t.Fatalf("expected *Paused, got %v", err)
	}
	if !p.Verdict.Blocked() {
		t.Fatalf("verdict not blocked")
	}
}

func TestGuardSoftModeReturnsBlockVerdict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, verdictJSON("block"))
	}))
	defer srv.Close()
	initGuard(t, srv)

	v, err := GuardCheck(context.Background(), GuardRequest{Agent: "nightly", Mode: "soft"})
	if err != nil {
		t.Fatalf("soft mode should not error, got %v", err)
	}
	if !v.Blocked() {
		t.Fatalf("expected blocked verdict")
	}
}

func TestGuardTargetsControlPlaneWithDimensions(t *testing.T) {
	var gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		_, _ = io.WriteString(w, verdictJSON("allow"))
	}))
	defer srv.Close()
	initGuard(t, srv)

	_, err := GuardCheck(context.Background(), GuardRequest{CustomerRef: "cus_abc", Agent: "nightly", Function: "run"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/v1/guard/check" {
		t.Fatalf("expected /v1/guard/check, got %q", gotPath)
	}
	for _, want := range []string{`"customer_ref":"cus_abc"`, `"agent":"nightly"`, `"function":"run"`} {
		if !strings.Contains(gotBody, want) {
			t.Fatalf("body missing %s: %s", want, gotBody)
		}
	}
}

func TestGuardInactivePlanWarnsAndAllows(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"decision":"allow","blocked_by":[],"rules":[],"active":false,"inactive_reason":"tier","guard_check_id":null,"stale_as_of":null}`)
	}))
	defer srv.Close()
	initGuard(t, srv)
	var buf bytes.Buffer
	setWarnOutput(&buf)
	defer setWarnOutput(nil)

	v, err := GuardCheck(context.Background(), GuardRequest{Agent: "nightly"})
	if err != nil {
		t.Fatalf("inactive should not error, got %v", err)
	}
	if v.Active {
		t.Fatalf("expected active=false")
	}
	if !strings.Contains(strings.ToLower(buf.String()), "unguarded") {
		t.Fatalf("expected an unguarded warning, got %q", buf.String())
	}
}

func TestGuardUnreachableFailsClosedByDefault(t *testing.T) {
	resetWarningsForTest()
	t.Cleanup(clearStateForTest)
	// A control URL that refuses connections.
	if err := Init(WithAPIKey("apk_eu_test"), WithControlURL("http://127.0.0.1:1")); err != nil {
		t.Fatalf("init: %v", err)
	}
	_, err := GuardCheck(context.Background(), GuardRequest{Agent: "nightly"})
	var p *Paused
	if !errors.As(err, &p) {
		t.Fatalf("expected *Paused on unreachable fail-closed, got %v", err)
	}
}

func TestGuardUnreachableCanFailOpen(t *testing.T) {
	resetWarningsForTest()
	t.Cleanup(clearStateForTest)
	if err := Init(WithAPIKey("apk_eu_test"), WithControlURL("http://127.0.0.1:1")); err != nil {
		t.Fatalf("init: %v", err)
	}
	v, err := GuardCheck(context.Background(), GuardRequest{Agent: "nightly", OnUnreachable: "allow"})
	if err != nil {
		t.Fatalf("fail-open should not error, got %v", err)
	}
	if v.Blocked() {
		t.Fatalf("expected allow")
	}
}

func TestGuard429LeansAllow(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"error":"rate_limited"}`)
	}))
	defer srv.Close()
	initGuard(t, srv)

	v, err := GuardCheck(context.Background(), GuardRequest{Agent: "nightly", OnUnreachable: "block"})
	if err != nil {
		t.Fatalf("a 429 must not error, got %v", err)
	}
	if v.Blocked() {
		t.Fatalf("a 429 is our condition, not a block")
	}
	if hits != 2 {
		t.Fatalf("expected one retry (2 hits), got %d", hits)
	}
}
