package agentping

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

const maxRetries = 5

type worker struct {
	queue         *boundedQueue
	client        *httpClient
	flushInterval time.Duration
	batchSize     int

	mu            sync.Mutex
	lastFlushAt   time.Time
	lastError     error
	retryAttempts int
	nextEligible  time.Time

	wakeCh chan struct{}
	stopCh chan struct{}
	doneWg sync.WaitGroup

	// sleep is the clock used for back-off waits. Override in tests for speed.
	sleep func(d time.Duration)
	now   func() time.Time
}

func newWorker(q *boundedQueue, c *httpClient, flushInterval time.Duration, batchSize int) *worker {
	if flushInterval <= 0 {
		flushInterval = 2 * time.Second
	}
	if batchSize <= 0 {
		batchSize = 50
	}
	return &worker{
		queue:         q,
		client:        c,
		flushInterval: flushInterval,
		batchSize:     batchSize,
		wakeCh:        make(chan struct{}, 1),
		stopCh:        make(chan struct{}),
		sleep:         time.Sleep,
		now:           time.Now,
	}
}

func (w *worker) start() {
	w.doneWg.Add(1)
	go w.loop()
}

func (w *worker) stop() {
	select {
	case <-w.stopCh:
		return
	default:
		close(w.stopCh)
	}
	w.doneWg.Wait()
}

// notifyEnqueued wakes the worker if the queue reached the batch threshold.
func (w *worker) notifyEnqueued() {
	if w.queue.size() < w.batchSize {
		return
	}
	select {
	case w.wakeCh <- struct{}{}:
	default:
	}
}

func (w *worker) loop() {
	defer w.doneWg.Done()
	timer := time.NewTimer(w.flushInterval)
	defer timer.Stop()
	for {
		select {
		case <-w.stopCh:
			return
		case <-timer.C:
			w.tick(context.Background())
			timer.Reset(w.flushInterval)
		case <-w.wakeCh:
			w.tick(context.Background())
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(w.flushInterval)
		}
	}
}

func (w *worker) tick(ctx context.Context) {
	w.mu.Lock()
	if !w.nextEligible.IsZero() && w.now().Before(w.nextEligible) {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	if w.queue.size() == 0 {
		return
	}
	w.flushOnce(ctx)
}

// flushAll drains the queue until empty or the context is done. Used by
// Flush and Shutdown for explicit blocking flushes.
func (w *worker) flushAll(ctx context.Context) error {
	for w.queue.size() > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		w.mu.Lock()
		wait := time.Duration(0)
		if !w.nextEligible.IsZero() {
			if d := time.Until(w.nextEligible); d > 0 {
				wait = d
			}
		}
		w.mu.Unlock()
		if wait > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		w.flushOnce(ctx)
	}
	return nil
}

func (w *worker) flushOnce(ctx context.Context) {
	batch := w.queue.drain(w.batchSize)
	if len(batch) == 0 {
		return
	}
	groups := groupEnvelopes(batch)

	for i, g := range groups {
		res := w.sendGroup(ctx, g)
		w.mu.Lock()
		w.lastFlushAt = w.now()
		w.mu.Unlock()

		if res.ok {
			w.mu.Lock()
			w.lastError = nil
			w.retryAttempts = 0
			w.nextEligible = time.Time{}
			w.mu.Unlock()
			continue
		}

		if res.errClass == errClassAuth || res.errClass == errClassClient {
			w.queue.incrementDropped(len(g))
			w.mu.Lock()
			if res.err != nil {
				w.lastError = res.err
			} else {
				w.lastError = fmt.Errorf("status=%d", res.status)
			}
			w.retryAttempts = 0
			w.mu.Unlock()
			continue
		}

		remaining := flatten(groups[i:])
		w.queue.unshift(remaining)
		w.scheduleBackoff(res)
		return
	}
}

func (w *worker) sendGroup(ctx context.Context, group []envelope) httpResult {
	if len(group) == 0 {
		return httpResult{ok: true, status: 202}
	}
	head := group[0]
	switch head.kind {
	case envRunStart:
		return w.client.post(ctx, "/v1/runs", head.body)
	case envRunFinish:
		return w.client.post(ctx, "/v1/runs/"+head.runID+"/finish", head.body)
	case envHeartbeat:
		return w.client.post(ctx, "/v1/heartbeats", head.body)
	case envRunEvent:
		events := make([]map[string]any, 0, len(group))
		for _, e := range group {
			events = append(events, e.event)
		}
		return w.client.post(ctx, "/v1/runs/"+head.runID+"/events", map[string]any{"events": events})
	}
	return httpResult{ok: true, status: 202}
}

func (w *worker) scheduleBackoff(res httpResult) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if res.err != nil {
		w.lastError = res.err
	} else {
		w.lastError = fmt.Errorf("status=%d", res.status)
	}
	w.retryAttempts++
	if w.retryAttempts > maxRetries {
		dropped := w.queue.drain(w.queue.size())
		w.queue.incrementDropped(len(dropped))
		w.retryAttempts = 0
		w.nextEligible = time.Time{}
		w.lastError = errors.New("agentping: gave up after max retries")
		return
	}
	var backoff time.Duration
	if res.retryAfter > 0 {
		backoff = res.retryAfter
	} else {
		backoff = time.Duration(500*pow2(w.retryAttempts-1)) * time.Millisecond
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
	w.nextEligible = w.now().Add(backoff)
}

func (w *worker) snapshot() (time.Time, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastFlushAt, w.lastError
}

func pow2(n int) int {
	if n < 0 {
		return 1
	}
	out := 1
	for i := 0; i < n; i++ {
		out *= 2
	}
	return out
}

// groupEnvelopes merges adjacent run_event envelopes for the same run into a
// single group so the worker can POST them in one batch request.
func groupEnvelopes(batch []envelope) [][]envelope {
	var groups [][]envelope
	var current []envelope
	var currentRun string
	for _, env := range batch {
		if env.kind == envRunEvent {
			if currentRun == env.runID && len(current) > 0 {
				current = append(current, env)
				continue
			}
			if len(current) > 0 {
				groups = append(groups, current)
			}
			current = []envelope{env}
			currentRun = env.runID
			continue
		}
		if len(current) > 0 {
			groups = append(groups, current)
			current = nil
			currentRun = ""
		}
		groups = append(groups, []envelope{env})
	}
	if len(current) > 0 {
		groups = append(groups, current)
	}
	return groups
}

func flatten(groups [][]envelope) []envelope {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	out := make([]envelope, 0, total)
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}
