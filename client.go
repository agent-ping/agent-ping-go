package agentping

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultRequestTimeout = 2 * time.Second
const userAgent = "agentping-go/0.1"

type errorClass string

const (
	errClassAuth    errorClass = "auth_error"
	errClassNetwork errorClass = "network_error"
	errClassServer  errorClass = "server_error"
	errClassClient  errorClass = "client_error"
)

type httpResult struct {
	ok         bool
	status     int
	errClass   errorClass
	retryAfter time.Duration
	err        error
}

type httpClient struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	timeout    time.Duration
}

func newHTTPClient(apiKey, baseURL string, h *http.Client, timeout time.Duration) *httpClient {
	if timeout <= 0 {
		timeout = defaultRequestTimeout
	}
	if h == nil {
		h = &http.Client{}
	}
	return &httpClient{
		apiKey:     apiKey,
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: h,
		timeout:    timeout,
	}
}

// post sends a JSON-encoded payload to a path under the configured base URL.
// Every call carries an independent 2 second context timeout.
func (c *httpClient) post(ctx context.Context, path string, payload any) httpResult {
	buf, err := json.Marshal(payload)
	if err != nil {
		return httpResult{err: fmt.Errorf("marshal payload: %w", err), errClass: errClassClient}
	}

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.baseURL+path, bytes.NewReader(buf))
	if err != nil {
		return httpResult{err: fmt.Errorf("build request: %w", err), errClass: errClassClient}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		warnOnce("network_error", fmt.Sprintf("network error contacting AgentPing API: %v", err))
		return httpResult{err: err, errClass: errClassNetwork}
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return httpResult{ok: true, status: resp.StatusCode}
	}

	retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
	var class errorClass
	switch {
	case resp.StatusCode == 401 || resp.StatusCode == 403:
		class = errClassAuth
		warnOnce("auth_error", "API key rejected; check AGENTPING_API_KEY. Events will be dropped.")
	case resp.StatusCode == 429:
		class = errClassServer
	case resp.StatusCode == 404:
		// Run-start is persisted asynchronously, so an event or finish can
		// arrive before its run row exists. Treat 404 as transient and retry
		// with back-off; a genuinely missing run drops after maxRetries.
		class = errClassServer
	case resp.StatusCode >= 500:
		class = errClassServer
		warnOnce("server_error", fmt.Sprintf("AgentPing API returned %d; will retry with back-off.", resp.StatusCode))
	default:
		class = errClassClient
	}
	return httpResult{status: resp.StatusCode, errClass: class, retryAfter: retryAfter}
}

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	trimmed := strings.TrimSpace(header)
	if secs, err := strconv.Atoi(trimmed); err == nil {
		if secs < 0 {
			return 0
		}
		d := time.Duration(secs) * time.Second
		if d > 60*time.Second {
			d = 60 * time.Second
		}
		return d
	}
	if when, err := http.ParseTime(trimmed); err == nil {
		delta := time.Until(when)
		if delta <= 0 {
			return 0
		}
		if delta > 60*time.Second {
			delta = 60 * time.Second
		}
		return delta
	}
	return 0
}
