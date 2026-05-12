package jobs

import (
	"testing"
	"time"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
)

func makeEntry(key string) Entry {
	return Entry{
		Payload: analysis.IssuePayload{Key: key},
		Options: analysis.RunOptions{},
	}
}

func TestNew_EmptyAndNonNil(t *testing.T) {
	q := New()
	if q == nil {
		t.Fatal("New returned nil")
	}
	if _, ok := q.Take(1); ok {
		t.Errorf("fresh queue returned an entry for unknown jobID")
	}
}

func TestPutThenTake(t *testing.T) {
	q := New()
	q.Put(42, makeEntry("KAN-1"))

	got, ok := q.Take(42)
	if !ok {
		t.Fatal("Take returned ok=false after Put")
	}
	if got.Payload.Key != "KAN-1" {
		t.Errorf("payload.Key = %q, want KAN-1", got.Payload.Key)
	}

	// Take is destructive — second call must miss.
	if _, ok := q.Take(42); ok {
		t.Error("Take returned ok=true twice for the same jobID")
	}
}

func TestTake_WrongJobID(t *testing.T) {
	q := New()
	q.Put(1, makeEntry("KAN-1"))
	if _, ok := q.Take(2); ok {
		t.Error("Take(2) returned ok=true when only jobID 1 was Put")
	}
	// The original entry is still retrievable.
	if _, ok := q.Take(1); !ok {
		t.Error("Take(1) missed after wrong Take(2)")
	}
}

func TestWake_FiresOnPut(t *testing.T) {
	q := New()
	q.Put(1, makeEntry("KAN-1"))

	select {
	case <-q.Wake():
		// Expected.
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Wake did not fire after Put")
	}
}

func TestWake_CoalescesMultiplePuts(t *testing.T) {
	q := New()
	q.Put(1, makeEntry("KAN-1"))
	q.Put(2, makeEntry("KAN-2"))
	q.Put(3, makeEntry("KAN-3"))

	// First receive sees the (one) buffered signal.
	select {
	case <-q.Wake():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Wake did not fire after Put burst")
	}

	// Second receive must NOT fire — the buffered channel coalesces.
	select {
	case <-q.Wake():
		t.Fatal("Wake fired twice for one drain cycle (buffer should be 1)")
	case <-time.After(50 * time.Millisecond):
		// Expected: no signal.
	}
}

func TestWake_FiresAfterDrain(t *testing.T) {
	q := New()
	q.Put(1, makeEntry("KAN-1"))
	<-q.Wake() // drain first signal

	// Subsequent Put after drain must re-fire.
	q.Put(2, makeEntry("KAN-2"))
	select {
	case <-q.Wake():
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Wake did not fire after a second Put following a drain")
	}
}

func TestConcurrentPutsAndTakes(t *testing.T) {
	q := New()
	const n = 100
	done := make(chan struct{}, n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			q.Put(int64(i), makeEntry("K"))
			done <- struct{}{}
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}

	// All entries should be takeable. Counting is enough — exact ordering
	// isn't a guarantee of the queue.
	taken := 0
	for i := 0; i < n; i++ {
		if _, ok := q.Take(int64(i)); ok {
			taken++
		}
	}
	if taken != n {
		t.Errorf("Take recovered %d of %d entries", taken, n)
	}
}
