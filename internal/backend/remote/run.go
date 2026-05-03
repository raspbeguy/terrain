package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/hashicorp/go-tfe"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
)

// pollInterval is how often we re-fetch the run object from the API. Two
// seconds is a deliberate compromise: TFE serializes runs per workspace and
// transitions between phases take 5+ seconds in practice, so faster polling
// just burns API quota; much slower and the UI looks frozen between
// "planning" and "planned".
const pollInterval = 2 * time.Second

// maxConsecutiveErrors caps how many in-a-row poll failures the loop
// tolerates before giving up. Five 2-second polls = 10 s of consecutive
// failure — enough to ride out a brief network blip but short enough that
// a permanent failure (revoked token, deleted org) doesn't leave a run
// spinning forever.
const maxConsecutiveErrors = 5

// remoteStream is the API-driven counterpart of local.runStream. The shape
// matches so bridge.PumpRun and the run page work without branching on
// backend kind.
type remoteStream struct {
	events chan domain.RunEvent
	logs   chan domain.LogLine
	plan   chan *domain.PlanResult
	done   chan error
}

func newRemoteStream() *remoteStream {
	return &remoteStream{
		events: make(chan domain.RunEvent, 16),
		logs:   make(chan domain.LogLine, 256),
		plan:   make(chan *domain.PlanResult, 1),
		done:   make(chan error, 1),
	}
}

func (s *remoteStream) Events() <-chan domain.RunEvent  { return s.events }
func (s *remoteStream) Logs() <-chan domain.LogLine     { return s.logs }
func (s *remoteStream) Plan() <-chan *domain.PlanResult { return s.plan }
func (s *remoteStream) Done() <-chan error              { return s.done }

// StartRun is the API-driven counterpart of local.Backend.StartRun.
//
// For plan/destroy runs: creates a fresh TFE run (Runs.Create), polls until
// terminal, streams logs.
//
// For apply runs (Kind == RunKindApply with req.ParentRunID set): looks up
// the parent run, sends Runs.Apply to confirm it, then polls the SAME run.
// TFE models a plan and its apply as one run object — there's no second
// run-create.
//
// req.WorkspaceID is the TFE workspace ID directly (remote backends store it
// as-is in domain.Workspace.ID).
func (b *Backend) StartRun(parent context.Context, req domain.RunRequest) (
	domain.Run, domain.RunStream, domain.CancelFunc, error,
) {
	if req.Kind == domain.RunKindApply {
		return b.startApply(parent, req)
	}
	return b.startNewRun(parent, req)
}

// startNewRun handles plan/destroy: Runs.Create + poll.
func (b *Backend) startNewRun(parent context.Context, req domain.RunRequest) (
	domain.Run, domain.RunStream, domain.CancelFunc, error,
) {
	wsCtx, wsCancel := context.WithTimeout(parent, 10*time.Second)
	defer wsCancel()
	tfeWS, err := b.client.Workspaces.ReadByID(wsCtx, req.WorkspaceID)
	if err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("read workspace %s: %w", req.WorkspaceID, err)
	}

	opts := tfe.RunCreateOptions{
		Workspace: tfeWS,
		Message:   tfe.String(req.Message),
		IsDestroy: tfe.Bool(req.Destroy || req.Kind == domain.RunKindDestroy),
		AutoApply: tfe.Bool(req.AutoApply),
	}
	if len(req.Targets) > 0 {
		opts.TargetAddrs = req.Targets
	}
	if len(req.Replaces) > 0 {
		opts.ReplaceAddrs = req.Replaces
	}

	createCtx, createCancel := context.WithTimeout(parent, 30*time.Second)
	defer createCancel()
	tfeRun, err := b.client.Runs.Create(createCtx, opts)
	if err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("create run: %w", err)
	}

	run := domain.Run{
		ID:          tfeRun.ID,
		WorkspaceID: req.WorkspaceID,
		BackendID:   b.id,
		Kind:        req.Kind,
		Status:      domain.StatusPending,
		Message:     req.Message,
		CreatedAt:   tfeRun.CreatedAt,
		UpdatedAt:   tfeRun.CreatedAt,
	}

	runCtx, cancelCtx := context.WithCancel(context.Background())
	stream := newRemoteStream()

	cancelFn := func(callerCtx context.Context) error {
		// Best-effort API cancel; even if it fails, ctx cancel will stop our
		// polling loop within `pollInterval`. ForceCancel isn't called from
		// here — that's a destructive operation and should be a separate UI
		// action with confirmation.
		err := b.client.Runs.Cancel(callerCtx, tfeRun.ID, tfe.RunCancelOptions{})
		cancelCtx()
		if err != nil && !errors.Is(err, tfe.ErrResourceNotFound) {
			return fmt.Errorf("API cancel: %w", err)
		}
		return nil
	}

	go b.pollRun(runCtx, run, tfeRun, stream)

	return run, stream, cancelFn, nil
}

// startApply confirms a previously planned run and polls it through the
// applying → applied transition. The caller passes req.ParentRunID; the
// existing tfeRun is read so we can seed pollRun with current Plan/Apply
// relations.
func (b *Backend) startApply(parent context.Context, req domain.RunRequest) (
	domain.Run, domain.RunStream, domain.CancelFunc, error,
) {
	if req.ParentRunID == "" {
		return domain.Run{}, nil, nil, errors.New("remote apply requires ParentRunID")
	}

	readCtx, readCancel := context.WithTimeout(parent, 10*time.Second)
	defer readCancel()
	tfeRun, err := b.client.Runs.ReadWithOptions(readCtx, req.ParentRunID, &tfe.RunReadOptions{
		Include: []tfe.RunIncludeOpt{tfe.RunPlan, tfe.RunApply},
	})
	if err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("read parent run %s: %w", req.ParentRunID, err)
	}

	applyCtx, applyCancel := context.WithTimeout(parent, 30*time.Second)
	defer applyCancel()
	if err := b.client.Runs.Apply(applyCtx, req.ParentRunID, tfe.RunApplyOptions{
		Comment: tfe.String(req.Message),
	}); err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("confirm apply: %w", err)
	}

	run := domain.Run{
		ID:          tfeRun.ID,
		WorkspaceID: req.WorkspaceID,
		BackendID:   b.id,
		Kind:        domain.RunKindApply,
		Status:      domain.StatusConfirmed,
		Message:     req.Message,
		CreatedAt:   tfeRun.CreatedAt,
		UpdatedAt:   time.Now(),
	}

	runCtx, cancelCtx := context.WithCancel(context.Background())
	stream := newRemoteStream()

	cancelFn := func(callerCtx context.Context) error {
		err := b.client.Runs.Cancel(callerCtx, tfeRun.ID, tfe.RunCancelOptions{})
		cancelCtx()
		if err != nil && !errors.Is(err, tfe.ErrResourceNotFound) {
			return fmt.Errorf("API cancel: %w", err)
		}
		return nil
	}

	go b.pollRun(runCtx, run, tfeRun, stream)

	return run, stream, cancelFn, nil
}

// pollRun is the goroutine driving one remote run's lifecycle. Polls the
// API every pollInterval, transitions status, fans out log-streaming
// goroutines for plan/apply phases, and closes channels in safe order.
func (b *Backend) pollRun(ctx context.Context, run domain.Run, initial *tfe.Run, stream *remoteStream) {
	var (
		finalErr      error
		lastStatus    = domain.StatusPending
		planEmitted   bool
		// logsWG synchronises the plan/apply log goroutines so we don't
		// close stream.logs while they're still writing.
		logsWG sync.WaitGroup
	)

	setStatus := func(s domain.RunStatus, msg string) {
		if s == lastStatus {
			return
		}
		lastStatus = s
		ev := domain.RunEvent{At: time.Now(), Status: s, Message: msg}
		select {
		case stream.events <- ev:
		case <-time.After(2 * time.Second):
			slog.Warn("remote run event dropped (slow consumer)", "status", s)
			// Surface the drop in the log view too — see the matching
			// comment in local/run.go's setStatus.
			select {
			case stream.logs <- domain.LogLine{
				At:     time.Now(),
				Stream: domain.StreamStderr,
				Text:   fmt.Sprintf("[terrain] dropped status event: %s — %s", s, msg),
			}:
			default:
			}
		}
	}

	defer func() {
		logsWG.Wait()
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		select {
		case stream.done <- finalErr:
		default:
		}
		close(stream.done)
	}()

	setStatus(domain.StatusPending, "queued")

	// Track which sub-resources we've already attached log streams to.
	var planLogsStarted, applyLogsStarted bool

	startPlanLogs := func(planID string) {
		if planLogsStarted {
			return
		}
		planLogsStarted = true
		logsWG.Add(1)
		go func() {
			defer logsWG.Done()
			b.streamLogs(ctx, b.client.Plans.Logs, planID, stream)
		}()
	}
	startApplyLogs := func(applyID string) {
		if applyLogsStarted {
			return
		}
		applyLogsStarted = true
		logsWG.Add(1)
		go func() {
			defer logsWG.Done()
			b.streamLogs(ctx, b.client.Applies.Logs, applyID, stream)
		}()
	}

	// Seed log goroutines from the initial Run if Plan/Apply are already
	// linked at create time (rare, but cheap to check).
	if initial.Plan != nil {
		startPlanLogs(initial.Plan.ID)
	}
	if initial.Apply != nil {
		startApplyLogs(initial.Apply.ID)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	consecutiveErrors := 0

	for {
		select {
		case <-ctx.Done():
			setStatus(domain.StatusCanceled, "canceled")
			finalErr = ctx.Err()
			return
		case <-ticker.C:
		}

		readCtx, readCancel := context.WithTimeout(ctx, 10*time.Second)
		// Include Plan & Apply via Include so we get IDs in one round trip.
		tfeRun, err := b.client.Runs.ReadWithOptions(readCtx, run.ID, &tfe.RunReadOptions{
			Include: []tfe.RunIncludeOpt{tfe.RunPlan, tfe.RunApply},
		})
		readCancel()
		if err != nil {
			if ctx.Err() != nil {
				setStatus(domain.StatusCanceled, "canceled")
				finalErr = ctx.Err()
				return
			}
			// Run-not-found is terminal: the run was deleted or never
			// existed at the polled ID. No amount of retrying will help.
			if errors.Is(err, tfe.ErrResourceNotFound) {
				setStatus(domain.StatusErrored, "run not found by API: "+err.Error())
				finalErr = err
				return
			}
			// Transient or unclassified error — log, count, and retry.
			// After maxConsecutiveErrors we declare the run errored so a
			// permanent failure (token revoked, org deleted, networking
			// down) doesn't leave the loop spinning forever.
			consecutiveErrors++
			slog.Warn("remote run poll failed",
				"id", run.ID, "err", err, "consecutive", consecutiveErrors)
			if consecutiveErrors >= maxConsecutiveErrors {
				setStatus(domain.StatusErrored,
					fmt.Sprintf("API poll failed %d times consecutively: %s",
						consecutiveErrors, err))
				finalErr = err
				return
			}
			continue
		}
		consecutiveErrors = 0

		mapped, msg := mapStatus(tfeRun.Status)
		setStatus(mapped, msg)

		if tfeRun.Plan != nil {
			startPlanLogs(tfeRun.Plan.ID)
		}
		if tfeRun.Apply != nil {
			startApplyLogs(tfeRun.Apply.ID)
		}

		// Emit the plan diff once the plan is finalized (and only once).
		// JSONOutput is only available after the plan finishes and TFE
		// processes it — gating on StatusPlanned/PlannedAndFinished is the
		// safe boundary.
		if !planEmitted && tfeRun.Plan != nil &&
			(mapped == domain.StatusPlanned ||
				mapped == domain.StatusConfirmed ||
				mapped == domain.StatusApplying ||
				mapped == domain.StatusApplied) {
			planEmitted = true
			b.emitPlanResult(ctx, tfeRun.ID, tfeRun.Plan.ID, stream)
		}

		if mapped.Terminal() {
			return
		}
	}
}

// emitPlanResult fetches the structured plan JSON and pushes a PlanResult
// onto stream.plan. Best-effort: failures still emit a PlanResult with
// RunID set so the UI knows which run to apply, just without a parsed plan
// diff. Channel send is non-blocking-on-cancel.
func (b *Backend) emitPlanResult(ctx context.Context, runID, planID string, stream *remoteStream) {
	result := &domain.PlanResult{RunID: runID}

	fetchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	raw, err := b.client.Plans.ReadJSONOutput(fetchCtx, planID)
	if err != nil {
		result.Err = fmt.Errorf("read plan json: %w", err)
		slog.Warn("remote plan json fetch failed", "plan", planID, "err", err)
	} else {
		var plan tfjson.Plan
		if err := json.Unmarshal(raw, &plan); err != nil {
			result.Err = fmt.Errorf("decode plan json: %w", err)
			slog.Warn("remote plan json decode failed", "plan", planID, "err", err)
		} else {
			result.Parsed = &plan
		}
	}

	select {
	case stream.plan <- result:
	case <-ctx.Done():
	}
}

// streamLogs follows the API's log endpoint for a plan or apply. The API
// returns an io.Reader that blocks until the run finishes. We scan
// line-by-line, push each line through the LogLine channel, and exit when
// the reader hits EOF or ctx is done.
func (b *Backend) streamLogs(
	ctx context.Context,
	fetch func(context.Context, string) (io.Reader, error),
	id string,
	stream *remoteStream,
) {
	r, err := fetch(ctx, id)
	if err != nil {
		slog.Warn("remote logs fetch", "id", id, "err", err)
		return
	}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		// ctx may have been cancelled while we were blocked in Scan; check
		// before each push so we don't spam the channel post-cancel.
		if ctx.Err() != nil {
			return
		}
		line := domain.LogLine{
			At:     time.Now(),
			Stream: domain.StreamStdout,
			Text:   scanner.Text(),
		}
		select {
		case stream.logs <- line:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		slog.Warn("remote logs scan", "id", id, "err", err)
	}
}

// mapStatus translates a TFE run status into our domain superset. The
// secondary string is a free-form message for the run timeline (mostly for
// UI clarity; not all transitions need one).
func mapStatus(s tfe.RunStatus) (domain.RunStatus, string) {
	switch s {
	case tfe.RunPending:
		return domain.StatusPending, ""
	case tfe.RunFetching, tfe.RunFetchingCompleted:
		return domain.StatusFetching, ""
	case tfe.RunPlanQueued:
		return domain.StatusPending, "queued for plan"
	case tfe.RunPlanning:
		return domain.StatusPlanning, ""
	case tfe.RunPlanned, tfe.RunPlannedAndFinished, tfe.RunPlannedAndSaved:
		return domain.StatusPlanned, ""
	case tfe.RunCostEstimating:
		return domain.StatusCostEstimating, ""
	case tfe.RunPolicyChecking:
		return domain.StatusPolicyChecking, ""
	case tfe.RunConfirmed, tfe.RunApplyQueued:
		return domain.StatusConfirmed, ""
	case tfe.RunApplying:
		return domain.StatusApplying, ""
	case tfe.RunApplied:
		return domain.StatusApplied, ""
	case tfe.RunErrored:
		return domain.StatusErrored, "errored"
	case tfe.RunCanceled:
		return domain.StatusCanceled, "canceled"
	case tfe.RunDiscarded:
		return domain.StatusDiscarded, "discarded"
	}
	return domain.StatusPending, string(s)
}
