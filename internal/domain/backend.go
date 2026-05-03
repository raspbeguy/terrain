// Package domain holds the pure-Go types and interfaces that span the rest of
// the application. Nothing in this package may import gotk4, glib, or any
// other UI-side library — the domain must remain headless and unit-testable.
package domain

import "context"

// Capabilities is a bitmask declaring which features a Backend supports. The
// UI queries Capabilities and hides/grays sections rather than letting the
// backend lie about supporting something it can't.
type Capabilities uint32

const (
	// CapPlan: the backend can produce terraform/tofu plans.
	CapPlan Capabilities = 1 << iota
	// CapApply: the backend can apply a previously produced plan.
	CapApply
	// CapVarSets: the backend exposes variable sets that span multiple
	// workspaces (TFE-style). Local backends emulate this with on-disk
	// .tfvars files.
	CapVarSets
	// CapState: the backend can return current state and history.
	CapState
	// CapPolicy: Sentinel/OPA policy checks run as part of the run pipeline
	// (TFE/HCP only).
	CapPolicy
	// CapCostEst: cost estimation runs on plans (TFE/HCP only).
	CapCostEst
	// CapVCS: VCS-driven runs are managed by the backend.
	CapVCS
	// CapRunQueue: the backend has its own run queue (remote only). Local
	// backends always start runs immediately.
	CapRunQueue
)

// Has reports whether the bitmask contains every flag in want.
func (c Capabilities) Has(want Capabilities) bool {
	return c&want == want
}

// BackendKind names the family of a backend. Used by the UI to pick icons,
// help text, and capability defaults.
type BackendKind string

const (
	BackendKindLocal BackendKind = "local"
	BackendKindOTF   BackendKind = "otf"
	BackendKindHCP   BackendKind = "hcp"
	BackendKindTFE   BackendKind = "tfe"
)

// Backend abstracts the source of workspaces, runs, variables, and state.
// Implementations live in internal/backend/{local,remote}; both must satisfy
// this same interface so the UI never branches on backend kind.
//
// M2 lights up StartRun/CancelRun; Variables/State methods land in M3.
type Backend interface {
	// ID returns a stable identifier for this backend instance, unique across
	// the registry. Used as a key in URIs and on-disk storage paths.
	ID() string

	// Kind returns the backend family, used to pick icons and copy.
	Kind() BackendKind

	// DisplayName is the user-facing label.
	DisplayName() string

	// Capabilities is the static feature bitmask for this backend.
	Capabilities() Capabilities

	// Workspaces returns the workspaces this backend currently knows about.
	// Implementations should be cheap (cached) — the UI may call this on every
	// sidebar rebuild.
	Workspaces(ctx context.Context) ([]Workspace, error)

	// Workspace returns one workspace by ID. Returns ErrNotFound if missing.
	Workspace(ctx context.Context, id string) (Workspace, error)

	// StartRun begins a plan/apply/destroy operation. The Run snapshot is
	// returned synchronously (with status pending/planning depending on the
	// backend) along with a RunStream that emits live events, log lines, and
	// the final parsed plan. The CancelFunc cancels the in-flight run; it is
	// safe to call after the run completes (no-op).
	StartRun(ctx context.Context, req RunRequest) (Run, RunStream, CancelFunc, error)

	// Close releases any resources (open files, HTTP clients, etc.). Safe to
	// call multiple times.
	Close() error
}
