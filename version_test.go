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

func runStartBodies(t *testing.T) (*httptest.Server, func() []map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var starts []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		if r.URL.Path == "/v1/runs" {
			mu.Lock()
			starts = append(starts, b)
			mu.Unlock()
		}
		w.WriteHeader(202)
	}))
	return srv, func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := make([]map[string]any, len(starts))
		copy(out, starts)
		return out
	}
}

func TestRunVersionDefaultsFromInitAndARunCanOverrideIt(t *testing.T) {
	srv, starts := runStartBodies(t)
	defer srv.Close()
	t.Cleanup(clearStateForTest)

	if err := Init(
		WithAPIKey("apk_eu_00000000000000000000000000000000"),
		WithBaseURL(srv.URL),
		WithFlushInterval(10*time.Millisecond),
		WithDefaultVersion(" 2026.10.27 "),
	); err != nil {
		t.Fatal(err)
	}

	byDefault := NewRun("triage")
	override := NewRun("triage", WithVersion("prompt-v7"))
	_ = byDefault.Finish(FinishStatusSuccess)
	_ = override.Finish(FinishStatusSuccess)
	_ = Flush(flushContext())

	got := map[string]any{}
	for _, b := range starts() {
		got[b["id"].(string)] = b["version"]
	}
	if got[byDefault.ID] != "2026.10.27" {
		t.Errorf("default version: %v", got[byDefault.ID])
	}
	if got[override.ID] != "prompt-v7" {
		t.Errorf("override: %v", got[override.ID])
	}
}

func TestRunVersionComesFromTheEnvironmentAndIsOmittedWhenUnset(t *testing.T) {
	srv, starts := runStartBodies(t)
	defer srv.Close()
	t.Cleanup(clearStateForTest)

	t.Setenv("AGENTPING_VERSION", "git-abc123")
	if err := Init(WithAPIKey("apk_eu_00000000000000000000000000000000"), WithBaseURL(srv.URL), WithFlushInterval(10*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	fromEnv := NewRun("triage")
	_ = fromEnv.Finish(FinishStatusSuccess)
	_ = Flush(flushContext())
	clearStateForTest()

	t.Setenv("AGENTPING_VERSION", "")
	if err := Init(WithAPIKey("apk_eu_00000000000000000000000000000000"), WithBaseURL(srv.URL), WithFlushInterval(10*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	unset := NewRun("triage")
	_ = unset.Finish(FinishStatusSuccess)
	_ = Flush(flushContext())

	for _, b := range starts() {
		switch b["id"] {
		case fromEnv.ID:
			if b["version"] != "git-abc123" {
				t.Errorf("env version: %v", b["version"])
			}
		case unset.ID:
			if _, ok := b["version"]; ok {
				t.Errorf("version must be omitted when nothing sets it: %v", b)
			}
		}
	}
}

func flushContext() context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	_ = cancel
	return ctx
}
