package runner

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkspaceLocks_TryAcquireBasic(t *testing.T) {
	t.Parallel()
	w := NewWorkspaceLocks()

	rel, ok := w.TryAcquire("ws-1")
	if !ok {
		t.Fatal("first TryAcquire should succeed")
	}

	if _, ok := w.TryAcquire("ws-1"); ok {
		t.Fatal("second TryAcquire on same ws should fail")
	}

	rel2, ok := w.TryAcquire("ws-2")
	if !ok {
		t.Fatal("TryAcquire on different ws should succeed")
	}
	rel2()

	rel()
	if _, ok := w.TryAcquire("ws-1"); !ok {
		t.Fatal("after release, TryAcquire should succeed again")
	}
}

func TestWorkspaceLocks_AcquireBlocks(t *testing.T) {
	t.Parallel()
	w := NewWorkspaceLocks()

	rel, ok := w.TryAcquire("ws-block")
	if !ok {
		t.Fatal()
	}

	acquired := make(chan struct{})
	go func() {
		rel2 := w.Acquire("ws-block")
		close(acquired)
		rel2()
	}()

	select {
	case <-acquired:
		t.Fatal("Acquire should have blocked while lock was held")
	case <-time.After(50 * time.Millisecond):
	}

	rel()

	select {
	case <-acquired:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Acquire didn't unblock after release")
	}
}

func TestWorkspaceLocks_ReleaseIsIdempotent(t *testing.T) {
	t.Parallel()
	w := NewWorkspaceLocks()

	rel, _ := w.TryAcquire("ws-r")
	rel()
	rel()
	rel()

	if _, ok := w.TryAcquire("ws-r"); !ok {
		t.Fatal("after multiple release calls, lock should be free")
	}
}

func TestWorkspaceLocks_ConcurrentDifferentWorkspaces(t *testing.T) {
	t.Parallel()
	w := NewWorkspaceLocks()

	var counter atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := string(rune('a' + i))
			rel := w.Acquire(id)
			defer rel()
			counter.Add(1)
		}()
	}
	wg.Wait()
	if counter.Load() != 16 {
		t.Fatalf("expected 16 acquisitions, got %d", counter.Load())
	}
}
