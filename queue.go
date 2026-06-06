package agentping

import "sync"

type envelopeKind int

const (
	envRunStart envelopeKind = iota
	envRunEvent
	envRunFinish
	envHeartbeat
)

// envelope is a single queued payload destined for the API.
type envelope struct {
	kind  envelopeKind
	runID string
	body  map[string]any
	event map[string]any
}

// boundedQueue is a FIFO queue with a fixed capacity. When full, pushing a new
// item drops the oldest one and increments the dropped counter. All operations
// are safe for concurrent use.
type boundedQueue struct {
	mu       sync.Mutex
	items    []envelope
	capacity int
	dropped  uint64
}

func newBoundedQueue(capacity int) *boundedQueue {
	if capacity <= 0 {
		capacity = 1
	}
	return &boundedQueue{
		items:    make([]envelope, 0, capacity),
		capacity: capacity,
	}
}

// push appends an item, dropping the oldest entry if the queue is full.
// Never blocks; never returns an error.
func (q *boundedQueue) push(item envelope) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= q.capacity {
		q.items = q.items[1:]
		q.dropped++
	}
	q.items = append(q.items, item)
}

// drain removes and returns up to n items from the front of the queue.
func (q *boundedQueue) drain(n int) []envelope {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n <= 0 || len(q.items) == 0 {
		return nil
	}
	if n > len(q.items) {
		n = len(q.items)
	}
	out := make([]envelope, n)
	copy(out, q.items[:n])
	q.items = q.items[n:]
	return out
}

// unshift puts items back at the front. If the queue would overflow, the
// oldest pushed-back items get dropped first (counted as drops).
func (q *boundedQueue) unshift(items []envelope) {
	if len(items) == 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	room := q.capacity - len(q.items)
	if len(items) <= room {
		q.items = append(items, q.items...)
		return
	}
	keep := items[len(items)-room:]
	q.dropped += uint64(len(items) - len(keep))
	q.items = append(keep, q.items...)
}

func (q *boundedQueue) size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *boundedQueue) droppedCount() uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.dropped
}

func (q *boundedQueue) incrementDropped(n int) {
	if n <= 0 {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.dropped += uint64(n)
}
