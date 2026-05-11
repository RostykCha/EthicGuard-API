// Package jobs holds the in-process bridge between the HTTP handler that
// receives an analysis request and the worker pool that runs the LLM call.
//
// Why an in-memory map: the zero-retention rule forbids persisting the Jira
// issue payload to Postgres. So the only ways to get the payload from the
// request to the worker are (a) keep it in process memory, or (b) re-fetch
// from Jira from the worker (the API doesn't have OAuth credentials to do
// that). We pick (a) and accept the trade-off: if the API restarts between
// enqueue and pickup, the worker marks the job failed with code "orphaned"
// and the Forge trigger retries on the next AC edit. For a single-instance
// MVP this is fine; horizontal scale-out would need a real broker.
package jobs

import (
	"sync"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
)

// Queue is the in-memory payload bus between handler and workers.
type Queue struct {
	mu       sync.Mutex
	payloads map[int64]analysis.IssuePayload
	wake     chan struct{}
}

// New builds an empty queue with a buffered wake channel.
func New() *Queue {
	return &Queue{
		payloads: make(map[int64]analysis.IssuePayload),
		wake:     make(chan struct{}, 1),
	}
}

// Put stashes the payload under the given job id and signals one waiter.
// Non-blocking: the wake channel is buffered (capacity 1) so the signal
// coalesces if a worker is already awake.
func (q *Queue) Put(jobID int64, p analysis.IssuePayload) {
	q.mu.Lock()
	q.payloads[jobID] = p
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Take retrieves and removes the payload for a job. Returns ok=false when
// nothing is stashed (worker handles this as an orphaned job).
func (q *Queue) Take(jobID int64) (analysis.IssuePayload, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	p, ok := q.payloads[jobID]
	if ok {
		delete(q.payloads, jobID)
	}
	return p, ok
}

// Wake returns the channel workers listen on to wake up early. A poll-tick
// in the worker covers the case where Put-then-restart leaves a queued row
// without a wake signal.
func (q *Queue) Wake() <-chan struct{} {
	return q.wake
}
