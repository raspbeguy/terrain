package domain

import (
	"context"
	"fmt"
	"time"
)

// RunKind names what the run will do. We model "plan" and "apply" as separate
// kinds (not phases of a single run) because the user can plan many times
// before applying any of them, and TFE/HCP tracks them as distinct runs too.
// "destroy" is a terraform-level shortcut for `apply -destroy`; semantically a
// kind of apply, but kept separate so the UI can warn loudly.
type RunKind string

const (
	RunKindPlan    RunKind = "plan"
	RunKindApply   RunKind = "apply"
	RunKindDestroy RunKind = "destroy"
)

// RunStatus is the superset of states across local + remote (TFE/HCP/OTF)
// backends. Local only ever emits the subset:
//
//	pending → planning → planned → applying → applied / errored / canceled
//
// Remote backends additionally surface cost-estimating, policy-checking, etc.
// The UI renders only emitted phases, so a local run timeline omits the rows
// the local backend never reaches.
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

// Terminal reports whether this status represents a final state — no further
// events will be emitted.
func (s RunStatus) Terminal() bool {
	switch s {
	case StatusApplied, StatusErrored, StatusCanceled, StatusDiscarded:
		return true
	}
	return false
}

// Active reports whether a subprocess or remote operation is currently in
// flight for this status (drives the spinner / cancel-button visibility).
func (s RunStatus) Active() bool {
	switch s {
	case StatusFetching, StatusPlanning, StatusCostEstimating,
		StatusPolicyChecking, StatusApplying:
		return true
	}
	return false
}

// validTransitions encodes the legal outgoing arrows from each status. The
// runner uses this to reject invalid attempts; backends can extend it but
// not bypass it.
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

// CanTransitionTo reports whether moving from s to next is allowed. Terminal
// statuses always return false.
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

// RunRequest is what a caller passes to Backend.StartRun. Fields beyond Kind
// and WorkspaceID are optional.
type RunRequest struct {
	WorkspaceID string
	Kind        RunKind

	// Message is shown in run history (TFE-style "what's this run for?").
	Message string

	// Targets is `-target=<addr>` repeated. Empty means full plan.
	Targets []string
	// Replaces is `-replace=<addr>`.
	Replaces []string

	// Destroy maps to `-destroy` for plans (forming a destroy-plan).
	Destroy bool

	// Vars is workspace-level overrides written to terrain.auto.tfvars.json
	// for this run only.
	Vars map[string]string

	// AutoApply skips the manual-confirmation step for the plan. In M2 the
	// UI surfaces this as an explicit checkbox; default is false.
	AutoApply bool

	// PlanFile is the absolute path of a previously produced plan, used by
	// local apply runs. Required for local Kind == RunKindApply; ignored
	// by remote backends.
	PlanFile string

	// ParentRunID is the run identifier the apply should target, used by
	// remote backends (TFE/HCP/OTF) where a plan and its apply share one
	// run object. Required for remote Kind == RunKindApply; ignored by
	// local backends.
	ParentRunID string
}

// PlanResult is what RunStream.Plan() emits after a plan run finishes
// successfully. Local backends populate File (path to .tfplan); remote
// backends populate RunID (the TFE run that holds the plan). Both populate
// Parsed when JSON parsing succeeds. Consumers handle nil/zero fields per
// backend; the apply flow forwards both into RunRequest unchanged.
type PlanResult struct {
	File   string // local: path to plan.tfplan
	RunID  string // remote: TFE run ID holding the plan
	Parsed any    // *tfjson.Plan, populated by the runner after parsing
	Err    error  // non-nil if parsing failed; File / RunID are still usable
}

// Run is the metadata snapshot of a run as the runner emits it. Updated in
// place: ID/WorkspaceID/Kind are immutable; Status/UpdatedAt change.
type Run struct {
	ID          string
	WorkspaceID string
	BackendID   string
	Kind        RunKind
	Status      RunStatus
	Message     string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// PlanFile is set once a plan has been produced (non-empty plan).
	PlanFile string

	// RunDir is the directory containing persisted artifacts (stdout.log,
	// stderr.log, plan.tfplan, plan.json). Empty for backends that don't
	// persist artifacts on the local filesystem (remote backends fetch from
	// their API instead).
	RunDir string

	// ExitCode of the last subprocess. Zero when not yet finished or for
	// non-process backends (remote).
	ExitCode int

	// Error is the last terminal error. Nil when the run is mid-flight or
	// finished successfully.
	Error error
}

// Stream is one of "stdout" or "stderr" — used to tag log lines for
// formatting in the UI.
type Stream string

const (
	StreamStdout Stream = "stdout"
	StreamStderr Stream = "stderr"
)

// LogLine is one line of process output. JSON is non-nil when Text was a
// successful `-json` ndjson line we could parse; otherwise consumers should
// fall back to rendering Text raw (with optional ANSI colorization).
type LogLine struct {
	At     time.Time
	Stream Stream
	Text   string
	JSON   map[string]any // nil when not parseable as JSON
}

// RunEvent is a status or phase transition. The runner emits these on every
// state change so the UI timeline can record what happened and when.
type RunEvent struct {
	At      time.Time
	Status  RunStatus
	Message string
}

// RunStream is the live channel-set for one in-flight run. Implementations
// own the goroutines feeding these channels and close them once the run is
// terminal — Done() closes last, signalling "all other channels are drained,
// it's safe to stop reading."
//
// Consumers MUST NOT call any GTK function from these channels — the bridge
// package is the only legal crossing point.
type RunStream interface {
	// Events emits status/phase transitions. Closed on terminal status.
	Events() <-chan RunEvent

	// Logs emits stdout/stderr lines. Closed when the subprocess exits and
	// the readers drain.
	Logs() <-chan LogLine

	// Plan emits exactly once for plan kinds: a *PlanResult with the saved
	// plan-file path and (when available) the parsed *tfjson.Plan. Channel is
	// closed after the send. For apply runs, Plan is closed without ever
	// sending.
	Plan() <-chan *PlanResult

	// Done is closed when the run has reached a terminal status and ALL
	// other channels have been closed. The error indicates terminal status:
	// nil for clean success, non-nil otherwise (cancellation included).
	Done() <-chan error
}

// CancelFunc cancels an in-flight run. Idempotent. Returning an error
// indicates the cancellation request itself failed (e.g. couldn't deliver
// SIGINT); the run will still complete one way or another and Done() is
// the source of truth for final status.
type CancelFunc func(ctx context.Context) error

// String returns a human-readable rendering of a Run for logs/debugging.
func (r Run) String() string {
	return fmt.Sprintf("run[%s] ws=%s kind=%s status=%s", r.ID, r.WorkspaceID, r.Kind, r.Status)
}
