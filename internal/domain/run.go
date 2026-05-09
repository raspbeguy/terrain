package domain

import (
	"context"
	"fmt"
	"time"
)

// RunKind names what the run will do. Plan and apply are separate kinds
// (not phases of one run) because TFE/HCP tracks them separately and a
// user can plan many times before applying any. Destroy is a kind so the
// UI can warn loudly.
type RunKind string

const (
	RunKindPlan    RunKind = "plan"
	RunKindApply   RunKind = "apply"
	RunKindDestroy RunKind = "destroy"
)

// RunStatus is the superset of states across local + remote backends.
// Local emits only pending → planning → planned → applying → applied /
// errored / canceled; remote adds cost/policy phases.
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

// Active drives the spinner / cancel-button visibility.
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

// RunRequest is the input to Backend.StartRun. Fields beyond Kind +
// WorkspaceID are optional.
type RunRequest struct {
	WorkspaceID string
	Kind        RunKind
	Message     string
	Targets     []string
	Replaces    []string
	Destroy     bool
	Vars        map[string]string
	AutoApply   bool

	// PlanFile (local apply) and ParentRunID (remote apply) reference a
	// prior plan run. Each backend uses its own; the other is ignored.
	PlanFile    string
	ParentRunID string
}

// PlanResult is what RunStream.Plan() emits after a successful plan.
// Local populates File; remote populates RunID; both populate Parsed when
// JSON parsing succeeds.
type PlanResult struct {
	File   string
	RunID  string
	Parsed any   // *tfjson.Plan
	Err    error // non-nil if parsing failed; File / RunID are still usable
}

// Run is the metadata snapshot of a run. ID/WorkspaceID/Kind are
// immutable; Status/UpdatedAt change in place.
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
	// RunDir is the on-disk artifact dir; empty for remote backends that
	// fetch artifacts from their API instead.
	RunDir   string
	ExitCode int
	Error    error
}

type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// LogLine carries one line of process output. JSON is non-nil iff Text
// was a successfully-parsed `-json` ndjson line.
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

// RunStream is the live channel-set for one in-flight run. Implementations
// close all channels once the run is terminal; Done() closes LAST so a
// receiver waiting on Done can rely on Events/Logs/Plan being drained.
//
// Consumers MUST NOT call any GTK function from these channels; bridge
// is the only legal crossing point.
type RunStream interface {
	Events() <-chan RunEvent
	Logs() <-chan LogLine
	// Plan emits exactly once for plan/destroy kinds and is closed after.
	// For apply, Plan is closed without ever sending.
	Plan() <-chan *PlanResult
	// Done emits the terminal error (nil = clean) once and is then closed.
	Done() <-chan error
}

// CancelFunc cancels an in-flight run. Idempotent. A non-nil return means
// the cancel request itself failed (e.g. SIGINT delivery); the run still
// completes through Done() either way.
type CancelFunc func(ctx context.Context) error

func (r Run) String() string {
	return fmt.Sprintf("run[%s] ws=%s kind=%s status=%s", r.ID, r.WorkspaceID, r.Kind, r.Status)
}
