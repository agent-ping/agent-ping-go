package agentping

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
)

// recorder captures the bodies the SDK posts, by path.
type recorder struct {
	mu     sync.Mutex
	paths  []string
	bodies []map[string]any
}

func (rec *recorder) forPath(path string) []map[string]any {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []map[string]any
	for i, p := range rec.paths {
		if p == path {
			out = append(out, rec.bodies[i])
		}
	}

	return out
}

func recordingServer(t *testing.T) *recorder {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		rec.mu.Lock()
		rec.paths = append(rec.paths, r.URL.Path)
		rec.bodies = append(rec.bodies, b)
		rec.mu.Unlock()
		w.WriteHeader(202)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(clearStateForTest)
	if err := Init(
		WithAPIKey("apk_eu_00000000000000000000000000000000"),
		WithBaseURL(srv.URL),
	); err != nil {
		t.Fatalf("init: %v", err)
	}

	return rec
}

// Every turn rebuilds the conversation from the session id alone, as a stateless
// handler would across separate requests. They must land on one run, and
// started_at must be identical each time or the server rejects the later ones.
func TestConversation_TurnsCollectOnOneRun(t *testing.T) {
	calls := recordingServer(t)

	session := NewSessionID()
	for i := 0; i < 3; i++ {
		chat := Conversation("support-chat", session)
		chat.Event("llm_call", map[string]any{
			"provider": "anthropic", "model": "claude-sonnet-4-6",
			"input_tokens": 900 * (i + 1), "output_tokens": 40,
		})
	}
	if err := Flush(context.Background()); err != nil {
		t.Fatal(err)
	}

	starts := calls.forPath("/v1/runs")
	if len(starts) == 0 {
		t.Fatal("no run starts recorded")
	}
	ids, startedAts := map[string]bool{}, map[string]bool{}
	for _, b := range starts {
		ids[b["id"].(string)] = true
		startedAts[b["started_at"].(string)] = true
	}
	if len(ids) != 1 {
		t.Errorf("run ids = %d, want 1: three turns, one conversation", len(ids))
	}
	if got := "run_eu_" + session; !ids[got] {
		t.Errorf("run id not derived from the session id; got %v, want %s", ids, got)
	}
	if len(startedAts) != 1 {
		t.Errorf("started_at values = %d, want 1: a mismatch is a 409", len(startedAts))
	}
	if secs, ok := starts[0]["idle_timeout_seconds"]; !ok || secs.(float64) != 900 {
		t.Errorf("idle_timeout_seconds = %v, want 900", secs)
	}
	if st := starts[0]["idle_timeout_status"]; st != "success" {
		t.Errorf("idle_timeout_status = %v, want success: leaving a chat is not a failure", st)
	}
}

// An ordinary run stays out of the sweep entirely.
func TestNewRun_HasNoIdleTimeoutByDefault(t *testing.T) {
	calls := recordingServer(t)

	NewRun("nightly-report")
	if err := Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	starts := calls.forPath("/v1/runs")
	if len(starts) == 0 {
		t.Fatal("no run starts recorded")
	}
	if _, ok := starts[0]["idle_timeout_seconds"]; ok {
		t.Error("an ordinary run should not opt into the idle sweep")
	}
}

// A malformed session id degrades to a normal run rather than dropping telemetry.
func TestConversation_BadSessionIDStillReports(t *testing.T) {
	calls := recordingServer(t)

	Conversation("support-chat", "not-a-session")
	if err := Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	starts := calls.forPath("/v1/runs")
	if len(starts) != 1 {
		t.Fatalf("run starts = %d, want 1: a bad id must not lose the run", len(starts))
	}
	if !regexp.MustCompile(`^run_[a-z]{2}_[0-9a-f]{32}$`).MatchString(starts[0]["id"].(string)) {
		t.Errorf("id = %v, want a freshly minted run id", starts[0]["id"])
	}
}
