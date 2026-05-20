// Package domain: pure-Go types and interfaces; no gotk4/UI imports.
package domain

import "context"

type Capabilities uint32

const (
	CapPlan Capabilities = 1 << iota
	CapApply
	CapVarSets
	CapState
	CapPolicy
	CapCostEst
	CapVCS
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

type Backend interface {
	ID() string
	Kind() BackendKind
	DisplayName() string
	Capabilities() Capabilities

	Workspaces(ctx context.Context) ([]Workspace, error)
	Workspace(ctx context.Context, id string) (Workspace, error)

	StartRun(ctx context.Context, req RunRequest) (Run, RunStream, CancelFunc, error)

	Close() error
}

type WorkspaceStreamer interface {
	StreamWorkspaces(ctx context.Context) <-chan WorkspaceStreamItem
}

type WorkspaceStreamItem struct {
	Workspaces []Workspace
	Err        error
}
