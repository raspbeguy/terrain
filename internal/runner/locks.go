package runner

import "sync"

// In-process only; cross-process safety relies on tofu's state lock.
type WorkspaceLocks struct {
	locks sync.Map
}

func NewWorkspaceLocks() *WorkspaceLocks { return &WorkspaceLocks{} }

func (w *WorkspaceLocks) TryAcquire(workspaceID string) (release func(), ok bool) {
	m := w.lockFor(workspaceID)
	if !m.TryLock() {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(m.Unlock) }, true
}

func (w *WorkspaceLocks) Acquire(workspaceID string) (release func()) {
	m := w.lockFor(workspaceID)
	m.Lock()
	var once sync.Once
	return func() { once.Do(m.Unlock) }
}

func (w *WorkspaceLocks) lockFor(workspaceID string) *sync.Mutex {
	v, _ := w.locks.LoadOrStore(workspaceID, &sync.Mutex{})
	return v.(*sync.Mutex)
}
