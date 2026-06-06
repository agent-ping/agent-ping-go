package agentping

import "testing"

func TestQueueDropOldest(t *testing.T) {
	q := newBoundedQueue(1000)
	for i := 0; i < 1001; i++ {
		q.push(envelope{kind: envRunEvent, runID: "run_eu_00000000000000000000000000000000",
			event: map[string]any{"n": i}})
	}
	if q.size() != 1000 {
		t.Fatalf("expected 1000 items, got %d", q.size())
	}
	if q.droppedCount() != 1 {
		t.Fatalf("expected 1 drop, got %d", q.droppedCount())
	}
	first := q.drain(1)
	if first[0].event["n"].(int) != 1 {
		t.Fatalf("expected oldest dropped, head=%v", first[0].event["n"])
	}
}

func TestQueueDrainCount(t *testing.T) {
	q := newBoundedQueue(10)
	for i := 0; i < 5; i++ {
		q.push(envelope{kind: envHeartbeat, body: map[string]any{"i": i}})
	}
	got := q.drain(3)
	if len(got) != 3 {
		t.Fatalf("expected 3 drained, got %d", len(got))
	}
	if q.size() != 2 {
		t.Fatalf("expected 2 remaining, got %d", q.size())
	}
}

func TestQueueUnshift(t *testing.T) {
	q := newBoundedQueue(5)
	for i := 0; i < 3; i++ {
		q.push(envelope{kind: envHeartbeat, body: map[string]any{"i": i}})
	}
	q.unshift([]envelope{
		{kind: envHeartbeat, body: map[string]any{"i": -1}},
	})
	if q.size() != 4 {
		t.Fatalf("expected size 4, got %d", q.size())
	}
	got := q.drain(1)
	if got[0].body["i"].(int) != -1 {
		t.Fatalf("unshift did not put item at front")
	}
}

func TestQueueUnshiftOverflow(t *testing.T) {
	q := newBoundedQueue(3)
	for i := 0; i < 3; i++ {
		q.push(envelope{kind: envHeartbeat, body: map[string]any{"i": i}})
	}
	q.unshift([]envelope{
		{kind: envHeartbeat, body: map[string]any{"i": -2}},
		{kind: envHeartbeat, body: map[string]any{"i": -1}},
	})
	// Queue already full; both unshifted items should be counted as drops.
	if q.droppedCount() != 2 {
		t.Fatalf("expected 2 drops, got %d", q.droppedCount())
	}
}
