package agentping

import (
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	defaultRegion        = "eu"
	defaultQueueSize     = 1000
	defaultFlushInterval = 2 * time.Second
	defaultBatchSize     = 50
)

// knownRegions enumerates the data residencies the platform supports
// today. An unknown region falls back to the EU host; the edge returns
// 401 region_mismatch in that case which surfaces the misconfiguration
// clearly.
var knownRegions = map[string]struct{}{
	"eu": {},
	"us": {},
}

// defaultBaseURLForRegion returns the regional ingest hostname for the
// SDK to call (spec §4.1, §2.9). Falls back to EU for unknown regions.
func defaultBaseURLForRegion(region string) string {
	if _, ok := knownRegions[region]; !ok {
		region = defaultRegion
	}
	return "https://" + region + ".ingest.agentping.io"
}

// defaultControlURLForRegion returns the control-plane hostname for the SDK
// to call (guard-checks-spec). The guard gate is served by the control plane,
// not the ingest edge, so it has its own base URL. EU is served at the apex;
// other regions are subdomained.
func defaultControlURLForRegion(region string) string {
	if _, ok := knownRegions[region]; !ok {
		region = defaultRegion
	}
	if region == "eu" {
		return "https://agentping.io"
	}
	return "https://" + region + ".agentping.io"
}

type sdkState struct {
	apiKey        string
	baseURL       string
	controlURL    string
	region        string
	queue         *boundedQueue
	worker        *worker
	client        *httpClient
	controlClient *httpClient
	flushInterval time.Duration
	batchSize     int
	version       string
}

var (
	stateMu   sync.RWMutex
	current   *sdkState
	defaultRT = http.DefaultTransport
)

func setState(s *sdkState) {
	stateMu.Lock()
	defer stateMu.Unlock()
	if current != nil && current.worker != nil {
		current.worker.stop()
	}
	current = s
}

func getState() *sdkState {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return current
}

func clearStateForTest() {
	stateMu.Lock()
	defer stateMu.Unlock()
	if current != nil && current.worker != nil {
		current.worker.stop()
	}
	current = nil
}

func envOrEmpty(key string) string {
	return os.Getenv(key)
}
