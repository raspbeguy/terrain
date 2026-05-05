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

// pollInterval balances API quota against UI responsiveness — TFE phase
// transitions take ~5s, so faster polls just burn quota.
const pollInterval = 2 * time.Second

// maxConsecutiveErrors ≈ 10s of poll failure; enough to ride a network
// blip but short enough that a permanent failure (revoked token,
// deleted org) doesn't spin forever.
const maxConsecutiveErrors = 5

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

// StartRun routes plan/destroy through Runs.Create; apply confirms +
// polls the parent run (TFE folds plan + apply into one run object).
func (b *Backend) StartRun(parent context.Context, req domain.RunRequest) (
	domain.Run, domain.RunStream, domain.CancelFunc, error,
) {
	if req.Kind == domain.RunKindApply {
		return b.startApply(parent, req)
	}
	return b.startNewRun(parent, req)
}

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
		// Soft cancel only; ForceCancel needs explicit user confirmation
		// elsewhere. ctx cancel stops the polling loop within pollInterval
		// even if the API call fails.
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

// pollRun drives one remote run's lifecycle, transitioning status and
// fanning out log streams for plan/apply phases.
func (b *Backend) pollRun(ctx context.Context, run domain.Run, initial *tfe.Run, stream *remoteStream) {
	var (
		finalErr    error
		lastStatus  = domain.StatusPending
		planEmitted bool
		// logsWG ensures plan/apply log goroutines finish before
		// stream.logs closes.
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

	// Seed log goroutines if Plan/Apply are linked at create time.
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
		// Include Plan & Apply so we get their IDs in one round-trip.
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
			// Run-not-found is terminal — no retry helps.
			if errors.Is(err, tfe.ErrResourceNotFound) {
				setStatus(domain.StatusErrored, "run not found by API: "+err.Error())
				finalErr = err
				return
			}
			// Transient errors get retried up to maxConsecutiveErrors;
			// after that we declare the run errored so a permanent
			// failure doesn't leave the loop spinning.
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

		// JSONOutput is only available after the plan finishes — gate
		// on StatusPlanned and beyond.
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

// emitPlanResult always emits a PlanResult (even when JSON fetch fails)
// so the UI has the RunID to apply.
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

// streamLogs scans the long-poll log reader (blocks until the
// plan/apply finishes) into the LogLine channel.
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
		// ctx may have been cancelled while Scan was blocked.
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
