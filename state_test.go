package agentping

import "testing"

// Regional default-URL contract. When Init is called without an explicit
// base URL, the SDK derives the host from the API key's region segment
// (spec §4.1, §2.9). Pinned here so we don't regress to the retired
// shared api.agentping.io host.
func TestDefaultBaseURLForRegion_KnownRegions(t *testing.T) {
	cases := map[string]string{
		"eu": "https://eu.ingest.agentping.io",
		"us": "https://us.ingest.agentping.io",
	}
	for region, want := range cases {
		if got := defaultBaseURLForRegion(region); got != want {
			t.Errorf("region=%q: got %q want %q", region, got, want)
		}
	}
}

func TestDefaultBaseURLForRegion_UnknownFallsBackToEU(t *testing.T) {
	if got := defaultBaseURLForRegion("zz"); got != "https://eu.ingest.agentping.io" {
		t.Errorf("unknown region should fall back to EU; got %q", got)
	}
	if got := defaultBaseURLForRegion(""); got != "https://eu.ingest.agentping.io" {
		t.Errorf("empty region should fall back to EU; got %q", got)
	}
}

func TestInit_EUKeyPicksEUHost(t *testing.T) {
	t.Cleanup(clearStateForTest)
	t.Setenv("AGENTPING_BASE_URL", "")
	if err := Init(WithAPIKey("apk_eu_" + repeat("a", 32))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	s := getState()
	if s.baseURL != "https://eu.ingest.agentping.io" {
		t.Errorf("eu key picked baseURL=%q want https://eu.ingest.agentping.io", s.baseURL)
	}
}

func TestInit_USKeyPicksUSHost(t *testing.T) {
	t.Cleanup(clearStateForTest)
	t.Setenv("AGENTPING_BASE_URL", "")
	if err := Init(WithAPIKey("apk_us_" + repeat("f", 32))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	s := getState()
	if s.baseURL != "https://us.ingest.agentping.io" {
		t.Errorf("us key picked baseURL=%q want https://us.ingest.agentping.io", s.baseURL)
	}
}

func TestInit_ExplicitBaseURLOverridesRegionDefault(t *testing.T) {
	t.Cleanup(clearStateForTest)
	t.Setenv("AGENTPING_BASE_URL", "")
	if err := Init(
		WithAPIKey("apk_us_"+repeat("f", 32)),
		WithBaseURL("https://api.test"),
	); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if s := getState(); s.baseURL != "https://api.test" {
		t.Errorf("explicit baseURL ignored; got %q", s.baseURL)
	}
}

func TestInit_EnvVarOverridesRegionDefault(t *testing.T) {
	t.Cleanup(clearStateForTest)
	t.Setenv("AGENTPING_BASE_URL", "https://env.example")
	if err := Init(WithAPIKey("apk_us_" + repeat("f", 32))); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if s := getState(); s.baseURL != "https://env.example" {
		t.Errorf("env var ignored; got %q", s.baseURL)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, n*len(s))
	for i := 0; i < n; i++ {
		copy(out[i*len(s):], s)
	}
	return string(out)
}
