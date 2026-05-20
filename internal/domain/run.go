package domain

import (
	"context"
	"fmt"
	"time"
)

type RunKind string

const (
	RunKindPlan    RunKind = "plan"
	RunKindApply   RunKind = "apply"
	RunKindDestroy RunKind = "destroy"
)

type RunStatus string

const (
	StatusPending        RunStatus = "pending"
	StatusFetching       RunStatus = "fetching"
	StatusPlanning       RunStatus = "planning"
	StatusPlanned        RunStatus = "planned"
	StatusCostEstimating RunStatus = "cost_estimating"
	StatusPolicyChecking RunStatus = "policy_checking"
	StatusConfirmed      RunStatus = "confirmed"
	StatusApplying       RunStatus = "applying"
	StatusApplied        RunStatus = "applied"
	StatusErrored        RunStatus = "errored"
	StatusCanceled       RunStatus = "canceled"
	StatusDiscarded      RunStatus = "discarded"
)

func (s RunStatus) Terminal() bool {
	switch s {
	case StatusApplied, StatusErrored, StatusCanceled, StatusDiscarded:
		return true
	}
	return false
}

func (s RunStatus) Active() bool {
	switch s {
	case StatusFetching, StatusPlanning, StatusCostEstimating,
		StatusPolicyChecking, StatusApplying:
		return true
	}
	return false
}

var validTransitions = map[RunStatus]map[RunStatus]bool{
	StatusPending:        toSet(StatusFetching, StatusPlanning, StatusErrored, StatusCanceled),
	StatusFetching:       toSet(StatusPlanning, StatusErrored, StatusCanceled),
	StatusPlanning:       toSet(StatusPlanned, StatusErrored, StatusCanceled),
	StatusPlanned:        toSet(StatusCostEstimating, StatusPolicyChecking, StatusConfirmed, StatusApplying, StatusDiscarded, StatusErrored),
	StatusCostEstimating: toSet(StatusPolicyChecking, StatusConfirmed, StatusErrored),
	StatusPolicyChecking: toSet(StatusConfirmed, StatusErrored, StatusDiscarded),
	StatusConfirmed:      toSet(StatusApplying, StatusErrored, StatusCanceled),
	StatusApplying:       toSet(StatusApplied, StatusErrored, StatusCanceled),
}

func (s RunStatus) CanTransitionTo(next RunStatus) bool {
	if s.Terminal() {
		return false
	}
	return validTransitions[s][next]
}

func toSet(values ...RunStatus) map[RunStatus]bool {
	out := make(map[RunStatus]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

type RunRequest struct {
	WorkspaceID string
	Kind        RunKind
	Message     string
	Targets     []string
	Replaces    []string
	Destroy     bool
	Vars        map[string]string
	AutoApply   bool

	PlanFile    string
	ParentRunID string
}

type PlanResult struct {
	File   string
	RunID  string
	Parsed any
	Err    error
}

type Run struct {
	ID          string
	WorkspaceID string
	BackendID   string
	Kind        RunKind
	Status      RunStatus
	Message     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	PlanFile    string
	RunDir      string
	ExitCode    int
	Error       error
}

type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

type LogLine struct {
	At     time.Time
	Stream Stream
	Text   string
	JSON   map[string]any
}

type RunEvent struct {
	At      time.Time
	Status  RunStatus
	Message string
}

// Done() closes last so receivers can rely on other channels being drained.
type RunStream interface {
	Events() <-chan RunEvent
	Logs() <-chan LogLine
	Plan() <-chan *PlanResult
	Done() <-chan error
}

type CancelFunc func(ctx context.Context) error

func (r Run) String() string {
	return fmt.Sprintf("run[%s] ws=%s kind=%s status=%s", r.ID, r.WorkspaceID, r.Kind, r.Status)
}
