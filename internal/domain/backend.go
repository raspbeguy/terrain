// Package domain holds the pure-Go types and interfaces that span the
// app. Nothing here may import gotk4 / glib / any UI library; the
// domain stays headless and unit-testable.
package domain

import "context"

// Capabilities is a feature bitmask. The UI queries it and hides sections
// rather than letting backends lie about features they can't support.
type Capabilities uint32

const (
	CapPlan Capabilities = 1 << iota
	CapApply
	// CapVarSets: TFE-style variable sets spanning workspaces. Local
	// backends emulate this with on-disk .tfvars files.
	CapVarSets
	CapState
	// CapPolicy: Sentinel/OPA checks (TFE/HCP only).
	CapPolicy
	// CapCostEst: cost estimation on plans (TFE/HCP only).
	CapCostEst
	CapVCS
	// CapRunQueue: backend has its own queue (remote only). Local
	// backends start runs immediately.
	CapRunQueue
)

func (c Capabilities) Has(want Capabilities) bool {
	return c&want == want
}

type BackendKind string

const (
	BackendKindLocal BackendKind = "local"
	BackendKindOTF   BackendKind = "otf"
	BackendKindHCP   BackendKind = "hcp"
	BackendKindTFE   BackendKind = "tfe"
)

// Backend is the architectural fulcrum: local + remote both implement
// it; the UI never branches on backend kind.
type Backend interface {
	ID() string
	Kind() BackendKind
	DisplayName() string
	Capabilities() Capabilities

	// Workspaces should be cheap; the sidebar may call it often.
	Workspaces(ctx context.Context) ([]Workspace, error)
	Workspace(ctx context.Context, id string) (Workspace, error)

	// StartRun begins a plan/apply/destroy. The returned CancelFunc is
	// safe to call after completion (no-op).
	StartRun(ctx context.Context, req RunRequest) (Run, RunStream, CancelFunc, error)

	// Close releases resources. Idempotent.
	Close() error
}

// WorkspaceStreamer lets the UI fill the sidebar page-by-page instead of
// waiting for a buffered List call. Mostly useful for remote orgs with
// hundreds of workspaces. Channel closes when paging is done.
type WorkspaceStreamer interface {
	StreamWorkspaces(ctx context.Context) <-chan WorkspaceStreamItem
}

// WorkspaceStreamItem carries either a page of workspaces or a fatal error.
// On error, the stream closes immediately after.
type WorkspaceStreamItem struct {
	Workspaces []Workspace
	Err        error
}
