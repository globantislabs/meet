package audio

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
)

// OverflowPolicy selects what Push does when the queue is full.
//
// WHY UNBOUNDED QUEUES ARE FORBIDDEN in this pipeline: frames arrive in real
// time (e.g. 50 frames/s of 20 ms audio). If a downstream stage (Palabra
// session, LiveKit publish) slows down, an unbounded queue grows without
// limit — unbounded memory growth AND, worse, unbounded *latency*: by the time
// a backlog is drained the audio is minutes stale, which destroys the
// conversational experience. Translation audio is only useful while fresh, so
// the correct behavior under backpressure is to drop stale frames and let the
// metrics show it (queue_dropped_frames_total).
type OverflowPolicy int

const (
	// PolicyDropOldest evicts the oldest buffered frame to make room for the
	// incoming one. Preferred for audio: keeps latency bounded at the cost of
	// dropping the stalest content.
	PolicyDropOldest OverflowPolicy = iota
	// PolicyDropNewest discards the incoming frame when full (Push returns
	// ErrQueueFull).
	PolicyDropNewest
	// PolicyBlock makes Push wait until space is available. Intended for
	// control-plane queues; audio pipelines should use PolicyDropOldest.
	// NOTE: Push has no context parameter, so PolicyBlock can only be
	// unblocked by Close.
	PolicyBlock
)

// Queue errors.
var (
	// ErrQueueClosed is returned by Push/Pop after Close.
	ErrQueueClosed = errors.New("audio: queue closed")
	// ErrQueueFull is returned by Push under PolicyDropNewest when the frame
	// could not be enqueued.
	ErrQueueFull = errors.New("audio: queue full")
)

// Queue is a bounded, backpressured AudioFrame queue. The zero value is not
// usable; construct with NewQueue. Safe for concurrent use by one producer and
// one consumer (typical) or several (channel semantics apply).
type Queue struct {
	ch     chan AudioFrame
	policy OverflowPolicy
	done   chan struct{}

	closeOnce sync.Once
	dropped   atomic.Uint64
}

// NewQueue creates a queue holding at most capacity frames with the given
// overflow policy. Capacity <= 0 is treated as 1.
func NewQueue(capacity int, policy OverflowPolicy) *Queue {
	if capacity < 1 {
		capacity = 1
	}
	return &Queue{
		ch:     make(chan AudioFrame, capacity),
		policy: policy,
		done:   make(chan struct{}),
	}
}

// Push enqueues a frame, applying the overflow policy. Push is non-blocking
// for the drop policies (stale audio must never stall the producer). Under
// PolicyDropOldest a full queue evicts its oldest frame; under PolicyDropNewest
// it rejects the incoming frame (ErrQueueFull). PolicyBlock blocks until space
// frees up or the queue is closed.
func (q *Queue) Push(f AudioFrame) error {
	switch q.policy {
	case PolicyBlock:
		select {
		case q.ch <- f:
			return nil
		case <-q.done:
			return ErrQueueClosed
		}
	case PolicyDropNewest:
		select {
		case q.ch <- f:
			return nil
		default:
			q.dropped.Add(1)
			return ErrQueueFull
		}
	default: // PolicyDropOldest
		select {
		case q.ch <- f:
			return nil
		default:
		}
		// Full: evict exactly one stale frame, then retry. If the retry still
		// fails (a concurrent pusher won the freed slot) report full.
		select {
		case <-q.ch:
			q.dropped.Add(1)
		default:
		}
		select {
		case q.ch <- f:
			return nil
		default:
			q.dropped.Add(1)
			return ErrQueueFull
		}
	}
}

// Pop blocks until a frame is available, ctx is done, or the queue is closed
// and drained. After Close, buffered frames are still delivered (graceful
// drain) before ErrQueueClosed is returned.
func (q *Queue) Pop(ctx context.Context) (AudioFrame, error) {
	// Fast path.
	select {
	case f := <-q.ch:
		return f, nil
	default:
	}
	select {
	case f := <-q.ch:
		return f, nil
	case <-q.done:
		// Closed: drain remaining frames so nothing buffered is lost.
		select {
		case f := <-q.ch:
			return f, nil
		default:
			return AudioFrame{}, ErrQueueClosed
		}
	case <-ctx.Done():
		return AudioFrame{}, ctx.Err()
	}
}

// Close marks the queue closed. Push returns ErrQueueClosed afterwards;
// Pop drains buffered frames first. Close is idempotent.
func (q *Queue) Close() {
	q.closeOnce.Do(func() {
		close(q.done)
	})
}

// Len returns the number of currently buffered frames (approximate under
// concurrency; used for the audio_queue_depth gauge only).
func (q *Queue) Len() int {
	return len(q.ch)
}

// Dropped returns the cumulative number of frames discarded by the overflow
// policy (for the queue_dropped_frames_total counter).
func (q *Queue) Dropped() uint64 {
	return q.dropped.Load()
}
