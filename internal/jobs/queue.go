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

// Entry bundles the in-memory payload with the per-project run options the
// handler resolved at enqueue time. Both travel together so the worker can
// stay free of any store dependency — all the per-project knobs it needs
// are already on the entry by the time it claims the job.
type Entry struct {
	Payload analysis.IssuePayload
	Options analysis.RunOptions
}

// Queue is the in-memory payload bus between handler and workers.
type Queue struct {
	mu       sync.Mutex
	entries  map[int64]Entry
	wake     chan struct{}
}

// New builds an empty queue with a buffered wake channel.
func New() *Queue {
	return &Queue{
		entries: make(map[int64]Entry),
		wake:    make(chan struct{}, 1),
	}
}

// Put stashes the entry under the given job id and signals one waiter.
// Non-blocking: the wake channel is buffered (capacity 1) so the signal
// coalesces if a worker is already awake.
func (q *Queue) Put(jobID int64, e Entry) {
	q.mu.Lock()
	q.entries[jobID] = e
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// Take retrieves and removes the entry for a job. Returns ok=false when
// nothing is stashed (worker handles this as an orphaned job).
func (q *Queue) Take(jobID int64) (Entry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.entries[jobID]
	if ok {
		delete(q.entries, jobID)
	}
	return e, ok
}

// Wake returns the channel workers listen on to wake up early. A poll-tick
// in the worker covers the case where Put-then-restart leaves a queued row
// without a wake signal.
func (q *Queue) Wake() <-chan struct{} {
	return q.wake
}
