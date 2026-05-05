package runner

import "sync"

// WorkspaceLocks serializes run starts and variable edits per workspace.
// In-process only; cross-process safety relies on tofu's own state lock.
type WorkspaceLocks struct {
	locks sync.Map
}

func NewWorkspaceLocks() *WorkspaceLocks { return &WorkspaceLocks{} }

// TryAcquire is non-blocking; the release fn is idempotent.
func (w *WorkspaceLocks) TryAcquire(workspaceID string) (release func(), ok bool) {
	m := w.lockFor(workspaceID)
	if !m.TryLock() {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(m.Unlock) }, true
}

// Acquire blocks. Use for short critical sections only — runs can take
// minutes and should use TryAcquire instead.
func (w *WorkspaceLocks) Acquire(workspaceID string) (release func()) {
	m := w.lockFor(workspaceID)
	m.Lock()
	var once sync.Once
	return func() { once.Do(m.Unlock) }
}

// IsHeld is racy by definition; advisory only.
func (w *WorkspaceLocks) IsHeld(workspaceID string) bool {
	m := w.lockFor(workspaceID)
	if m.TryLock() {
		m.Unlock()
		return false
	}
	return true
}

func (w *WorkspaceLocks) lockFor(workspaceID string) *sync.Mutex {
	v, _ := w.locks.LoadOrStore(workspaceID, &sync.Mutex{})
	return v.(*sync.Mutex)
}
