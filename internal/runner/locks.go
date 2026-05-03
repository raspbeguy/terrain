package runner

import "sync"

// WorkspaceLocks provides per-workspace mutual exclusion. Used by the UI to
// serialize:
//
//   - Concurrent run starts on the same workspace (prevents two
//     `tofu plan` invocations racing on terraform.tfstate.lock + the
//     backend's per-workspace artifact directory).
//   - Variable edits with run materialization (which reads
//     terraform.tfvars and the keyring index file).
//
// The lock is in-process only; cross-process safety relies on tofu's own
// state-lock during plan/apply. A second app instance will discover an
// existing state lock and bail out cleanly with terraform's own error,
// which is the right behaviour.
type WorkspaceLocks struct {
	locks sync.Map // workspaceID (string) → *sync.Mutex
}

// NewWorkspaceLocks returns a fresh lock registry. One per process is
// enough — the App owns it and threads it into the window + backends.
func NewWorkspaceLocks() *WorkspaceLocks { return &WorkspaceLocks{} }

// TryAcquire attempts to acquire the lock for workspaceID without blocking.
// Returns a release function and true on success; nil + false when another
// caller already holds the lock. The release function is safe to call
// multiple times — second + subsequent calls are no-ops.
//
// Use TryAcquire from UI button handlers where blocking would freeze the
// main loop.
func (w *WorkspaceLocks) TryAcquire(workspaceID string) (release func(), ok bool) {
	m := w.lockFor(workspaceID)
	if !m.TryLock() {
		return nil, false
	}
	var once sync.Once
	return func() { once.Do(m.Unlock) }, true
}

// Acquire blocks until the lock for workspaceID is available, then returns
// a release function. Idempotent like TryAcquire's release.
//
// Use Acquire for short, predictable critical sections (a single hclwrite
// round-trip on terraform.tfvars). Don't use it for run lifecycles — runs
// can take minutes and would hang the caller.
func (w *WorkspaceLocks) Acquire(workspaceID string) (release func()) {
	m := w.lockFor(workspaceID)
	m.Lock()
	var once sync.Once
	return func() { once.Do(m.Unlock) }
}

// IsHeld reports whether the lock for workspaceID is currently held. Note
// the result is racy by definition — use it only for advisory UI hints, not
// for correctness decisions.
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
