package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/runner"
)

// runStream is the local-backend implementation of domain.RunStream.
// All channels are owned by the worker goroutine; consumers must only read.
type runStream struct {
	events chan domain.RunEvent
	logs   chan domain.LogLine
	plan   chan *domain.PlanResult
	done   chan error
}

func newRunStream() *runStream {
	return &runStream{
		// Buffered enough to absorb a normal run without backpressure on the
		// producer (cancel/error events should never block on a slow UI).
		events: make(chan domain.RunEvent, 16),
		logs:   make(chan domain.LogLine, 256),
		plan:   make(chan *domain.PlanResult, 1),
		done:   make(chan error, 1),
	}
}

func (s *runStream) Events() <-chan domain.RunEvent     { return s.events }
func (s *runStream) Logs() <-chan domain.LogLine        { return s.logs }
func (s *runStream) Plan() <-chan *domain.PlanResult    { return s.plan }
func (s *runStream) Done() <-chan error                 { return s.done }

// startRun is the real implementation of LocalBackend.StartRun. Spawns the
// subprocess, returns a stream the caller can drain on the UI thread (via
// the bridge package).
func (b *Backend) startRun(_ context.Context, req domain.RunRequest) (domain.Run, domain.RunStream, domain.CancelFunc, error) {
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	ws, err := b.Workspace(wsCtx, req.WorkspaceID)
	if err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("resolve workspace: %w", err)
	}

	bin, err := DetectBinary()
	if err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("detect tofu/terraform: %w", err)
	}

	runID := newRunID()
	runDir, err := runArtifactsDir(b.id, ws, runID)
	if err != nil {
		return domain.Run{}, nil, nil, err
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("create run dir %s: %w", runDir, err)
	}

	run := domain.Run{
		ID:          runID,
		WorkspaceID: ws.ID,
		BackendID:   b.id,
		Kind:        req.Kind,
		Status:      domain.StatusPending,
		Message:     req.Message,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	// Detached context — the run outlives whatever GUI callback kicked it
	// off. CancelFunc is what stops it.
	runCtx, cancelCtx := context.WithCancel(context.Background())
	stream := newRunStream()

	cancelFn := func(_ context.Context) error {
		cancelCtx()
		return nil
	}

	go runWorker(runCtx, b, run, ws, bin, req, runDir, stream)

	return run, stream, cancelFn, nil
}

// runWorker is the goroutine that owns one run. Orchestrates initial
// snapshot → subprocess → log tee → terminal events → history record →
// channel close.
//
// b is the parent Backend, threaded through so the worker can call
// b.materialize to resolve sensitive Terraform vars and env-category vars
// from the keyring at run time.
func runWorker(
	ctx context.Context,
	b *Backend,
	run domain.Run,
	ws domain.Workspace,
	bin BinaryInfo,
	req domain.RunRequest,
	runDir string,
	stream *runStream,
) {
	var (
		finalErr   error
		lastStatus = domain.StatusPending
		exitCode   int
	)

	// Apply runs persist a backref to the plan file they consumed; the runs
	// list uses it to mark plan rows whose plan was actually applied vs ones
	// that were just dry-run inspections. Plan/destroy runs set PlanFile
	// themselves after producing the file.
	if req.Kind == domain.RunKindApply {
		run.PlanFile = req.PlanFile
	}

	setStatus := func(s domain.RunStatus, msg string) {
		lastStatus = s
		ev := domain.RunEvent{At: time.Now(), Status: s, Message: msg}
		select {
		case stream.events <- ev:
		case <-time.After(2 * time.Second):
			slog.Warn("run event dropped (slow consumer)", "status", s, "msg", msg)
			// Best-effort: surface the drop in the log view so the user
			// notices instead of silently losing a status transition. If
			// the log channel is also backpressured, give up.
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

	// done is the signal channel for "everything else has drained";
	// must close after events/logs/plan.
	defer func() {
		// Persist the terminal snapshot to history before signalling done.
		recordHistory(run, runDir, lastStatus, finalErr, exitCode)
		select {
		case stream.done <- finalErr:
		default:
		}
		close(stream.done)
	}()

	setStatus(domain.StatusPending, "queued")

	// Resolve runtime mode + image. Apply runs read these from the
	// producing plan's snapshot — the user might have toggled the
	// workspace's settings between plan and apply, but the apply still
	// has to use the same mode/image because the plan file inside it
	// references the producing run's container paths.
	rtOpts, err := b.resolveRuntimeOptions(ws.ID, run.ID)
	if err != nil {
		finalErr = fmt.Errorf("resolve runtime: %w", err)
		setStatus(domain.StatusErrored, finalErr.Error())
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		return
	}
	if req.Kind == domain.RunKindApply && req.PlanFile != "" {
		producingRunDir := filepath.Dir(req.PlanFile)
		if priorMode, priorImage, perr := readRequestSnapshot(producingRunDir); perr == nil {
			rtOpts.RunMode = priorMode
			if priorImage != "" {
				rtOpts.Image = priorImage
			}
		}
	}
	rt, err := newRuntime(rtOpts)
	if err != nil {
		finalErr = fmt.Errorf("init runtime: %w", err)
		setStatus(domain.StatusErrored, finalErr.Error())
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		return
	}
	cancelName := "terrain-" + run.ID

	if err := writeRequestSnapshot(runDir, run, req, rtOpts.RunMode, rtOpts.Image); err != nil {
		finalErr = fmt.Errorf("snapshot request: %w", err)
		setStatus(domain.StatusErrored, finalErr.Error())
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		return
	}

	args, planFile, err := buildCmdArgs(req, runDir)
	if err != nil {
		finalErr = err
		setStatus(domain.StatusErrored, err.Error())
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		return
	}

	stdoutLog, stderrLog, logErr := openLogFiles(runDir)
	if logErr != nil {
		// Non-fatal: the live log channel still works, just no on-disk
		// persistence. Surface the warning so it's visible in --debug
		// output, then proceed.
		slog.Warn("open run log files", "ws", ws.ID, "err", logErr)
	}
	defer closeIfNonNil(stdoutLog)
	defer closeIfNonNil(stderrLog)

	// Resolve sensitive + env-category vars from the keyring. Sensitive
	// Terraform vars become a -var-file argument; env vars get appended to
	// cmd.Env. The temp var-file is deleted as soon as the subprocess exits
	// so resolved secrets don't linger in $XDG_CACHE_HOME.
	rv := b.materialize(ws)
	varFile, vferr := rv.writeVarFile(runDir)
	if vferr != nil {
		setStatus(domain.StatusErrored, vferr.Error())
		finalErr = vferr
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		return
	}
	if varFile != "" {
		args = append(args, "-var-file="+varFile)
		defer func() {
			_ = os.Remove(varFile)
		}()
	}

	// Single tee goroutine shared by the init pass and the main subprocess
	// so both streams land in the same on-disk log files and the same live
	// channel. Closed once after the LAST streamCommand returns.
	teedLogs := make(chan domain.LogLine, cap(stream.logs))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for line := range teedLogs {
			writeLogLine(stdoutLog, stderrLog, line)
			stream.logs <- line
		}
	}()

	// Image pre-pull (container mode only). Streams progress through the
	// same log pipeline so the user sees the pull happening rather than a
	// frozen "Fetching" status pill. Skipped silently when the image is
	// already local — both podman and docker short-circuit fast.
	if pullCmd := rt.PullCommand(ctx, rtOpts.Image); pullCmd != nil {
		setStatus(domain.StatusFetching,
			fmt.Sprintf("pulling image %s", rtOpts.Image))
		if pullErr := streamCommand(ctx, pullCmd, teedLogs); pullErr != nil {
			close(teedLogs)
			wg.Wait()
			close(stream.logs)
			finalErr = fmt.Errorf("image pull: %w", pullErr)
			setStatus(domain.StatusErrored, finalErr.Error())
			close(stream.events)
			close(stream.plan)
			return
		}
	}

	// Init phase: plan/destroy runs always do `tofu init -input=false` first.
	// Apply runs skip it because they replay a saved plan that's already
	// gone through init at plan time. Init output streams into the same log
	// pipeline so the user sees provider downloads / module fetches alongside
	// the eventual plan output.
	if req.Kind == domain.RunKindPlan || req.Kind == domain.RunKindDestroy {
		setStatus(domain.StatusFetching,
			fmt.Sprintf("running `%s init -input=false`", bin.Name))
		initCmd := rt.Command(ctx, ws.WorkingDirectory,
			[]string{"NO_COLOR=1"},
			bin.Path, []string{"init", "-input=false", "-no-color"}, cancelName+"-init")
		installRuntimeCancel(initCmd, rt, cancelName+"-init")
		if initErr := streamCommand(ctx, initCmd, teedLogs); initErr != nil {
			close(teedLogs)
			wg.Wait()
			close(stream.logs)
			switch {
			case isCancelError(ctx, initErr):
				setStatus(domain.StatusCanceled, "canceled by user")
				finalErr = context.Canceled
			default:
				exitCode = exitCodeOf(initErr)
				setStatus(domain.StatusErrored,
					fmt.Sprintf("init failed (exit %d): %s", exitCode, initErr.Error()))
				finalErr = initErr
			}
			close(stream.events)
			close(stream.plan)
			return
		}
	}

	switch req.Kind {
	case domain.RunKindPlan, domain.RunKindDestroy:
		setStatus(domain.StatusPlanning,
			fmt.Sprintf("running `%s %s`", bin.Name, formatArgsForLog(args)))
	case domain.RunKindApply:
		setStatus(domain.StatusApplying,
			fmt.Sprintf("running `%s %s`", bin.Name, formatArgsForLog(args)))
	}

	extraEnv := append([]string{"NO_COLOR=1"}, rv.envEntries()...)
	cmd := rt.Command(ctx, ws.WorkingDirectory, extraEnv, bin.Path, args, cancelName)
	installRuntimeCancel(cmd, rt, cancelName)

	cmdErr := streamCommand(ctx, cmd, teedLogs)
	close(teedLogs)
	wg.Wait()
	close(stream.logs)

	switch {
	case cmdErr == nil:
		switch req.Kind {
		case domain.RunKindPlan, domain.RunKindDestroy:
			run.PlanFile = planFile
			setStatus(domain.StatusPlanned, "plan succeeded")
			// Fire and forget the post-plan parse — runs on the worker
			// goroutine since we're between cmdErr and channel close.
			result := parsePlanFile(ctx, bin.Path, ws.WorkingDirectory, planFile)
			persistPlanJSON(runDir, result)
			stream.plan <- result
		case domain.RunKindApply:
			setStatus(domain.StatusApplied, "apply succeeded")
			// Persist a state snapshot for browsable history. Best-effort:
			// failures are logged, the run itself already succeeded.
			if err := b.snapshotState(ctx, ws, run.ID); err != nil {
				slog.Warn("state snapshot after apply", "ws", ws.ID, "err", err)
			}
		}
	case isCancelError(ctx, cmdErr):
		setStatus(domain.StatusCanceled, "canceled by user")
		finalErr = context.Canceled
	default:
		exitCode = exitCodeOf(cmdErr)
		setStatus(domain.StatusErrored,
			fmt.Sprintf("%s failed (exit %d): %s", req.Kind, exitCode, cmdErr.Error()))
		finalErr = cmdErr
	}

	close(stream.events)
	close(stream.plan)
}

// formatArgsForLog renders the cmd-args slice for the run-event message
// with sensitive values redacted. The two-arg form `-var KEY=VAL` becomes
// `-var KEY=<redacted>`; everything else is passed through. Run history is
// persisted to disk, so leaking secrets here would defeat the keyring +
// vars.auto.tfvars.json setup that protects them everywhere else.
func formatArgsForLog(args []string) string {
	out := make([]string, 0, len(args))
	maskNext := false
	for _, a := range args {
		if maskNext {
			maskNext = false
			if eq := strings.Index(a, "="); eq > 0 {
				out = append(out, a[:eq+1]+"<redacted>")
			} else {
				out = append(out, "<redacted>")
			}
			continue
		}
		if a == "-var" {
			maskNext = true
		}
		out = append(out, a)
	}
	return strings.Join(out, " ")
}

// buildCmdArgs assembles the CLI args for one run. planFile is the path of
// the produced plan (for plan/destroy kinds) — empty for apply.
func buildCmdArgs(req domain.RunRequest, runDir string) (args []string, planFile string, err error) {
	switch req.Kind {
	case domain.RunKindPlan:
		planFile = filepath.Join(runDir, "plan.tfplan")
		args = []string{"plan", "-json", "-input=false", "-out=" + planFile}
		if req.Destroy {
			args = append(args, "-destroy")
		}
	case domain.RunKindDestroy:
		planFile = filepath.Join(runDir, "plan.tfplan")
		args = []string{"plan", "-json", "-input=false", "-destroy", "-out=" + planFile}
	case domain.RunKindApply:
		if req.PlanFile == "" {
			return nil, "", errors.New("apply requires RunRequest.PlanFile from a previous plan run")
		}
		args = []string{"apply", "-json", "-input=false", req.PlanFile}
	default:
		return nil, "", fmt.Errorf("unsupported run kind %q", req.Kind)
	}

	for _, t := range req.Targets {
		args = append(args, "-target="+t)
	}
	for _, r := range req.Replaces {
		args = append(args, "-replace="+r)
	}
	for k, v := range req.Vars {
		args = append(args, "-var", k+"="+v)
	}
	return args, planFile, nil
}

// recordHistory persists a terminal run snapshot to the per-workspace ndjson
// history. Best-effort: failures are logged but don't surface to the caller
// because we're already in a deferred shutdown path.
func recordHistory(run domain.Run, runDir string, status domain.RunStatus, finalErr error, exitCode int) {
	h, err := runner.NewHistory(run.BackendID, run.WorkspaceID)
	if err != nil {
		slog.Warn("open run history", "err", err, "ws", run.WorkspaceID)
		return
	}
	entry := runner.HistoryEntry{
		ID:          run.ID,
		WorkspaceID: run.WorkspaceID,
		BackendID:   run.BackendID,
		Kind:        run.Kind,
		Status:      status,
		Message:     run.Message,
		CreatedAt:   run.CreatedAt,
		UpdatedAt:   time.Now(),
		PlanFile:    run.PlanFile,
		RunDir:      runDir,
		ExitCode:    exitCode,
	}
	if finalErr != nil {
		entry.ErrorMessage = finalErr.Error()
	}
	if err := h.Record(entry); err != nil {
		slog.Warn("record run history", "err", err, "id", run.ID)
	}
}

// isCancelError reports whether the command error is "we cancelled it"
// rather than "tofu failed for its own reasons."
func isCancelError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if ctx.Err() != nil {
		return true
	}
	return false
}

// runArtifactsDir returns the per-run artifact directory under
// XDG_CACHE_HOME (we'll move to XDG_DATA_HOME for state-version persistence
// in M3 — runs are ephemeral, state is not).
func runArtifactsDir(backendID string, ws domain.Workspace, runID string) (string, error) {
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache dir: %w", err)
	}
	safeWS := strings.NewReplacer("/", "_", ":", "_").Replace(ws.ID)
	return filepath.Join(cacheHome, "terrain", backendID, safeWS, "runs", runID), nil
}

// newRunID returns a sortable, opaque run identifier. Time-prefixed so
// on-disk listing reads chronological without an index file.
func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}

// openLogFiles creates the run's stdout/stderr log files. Returns the open
// file handles, or an error if either could not be created. The caller is
// responsible for closing both — even on partial success, both files are
// either both non-nil or both nil.
func openLogFiles(runDir string) (stdout, stderr *os.File, err error) {
	stdout, err = os.Create(filepath.Join(runDir, "stdout.log"))
	if err != nil {
		return nil, nil, fmt.Errorf("create stdout.log: %w", err)
	}
	stderr, err = os.Create(filepath.Join(runDir, "stderr.log"))
	if err != nil {
		stdout.Close()
		return nil, nil, fmt.Errorf("create stderr.log: %w", err)
	}
	return stdout, stderr, nil
}

func writeLogLine(stdout, stderr *os.File, line domain.LogLine) {
	var f *os.File
	switch line.Stream {
	case domain.StreamStdout:
		f = stdout
	case domain.StreamStderr:
		f = stderr
	}
	if f == nil {
		return
	}
	_, _ = f.WriteString(line.Text + "\n")
}

func closeIfNonNil(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

func writeRequestSnapshot(runDir string, run domain.Run, req domain.RunRequest, mode RunMode, image string) error {
	body := fmt.Sprintf(
		"id=%s\nworkspace=%s\nkind=%s\ncreated_at=%s\nmessage=%s\nrun_mode=%s\nimage=%s\n",
		run.ID, run.WorkspaceID, run.Kind, run.CreatedAt.Format(time.RFC3339), req.Message, mode, image,
	)
	return os.WriteFile(filepath.Join(runDir, "request.txt"), []byte(body), 0o644)
}

// readRequestSnapshot parses a previous run's request.txt to recover the
// run-mode + image it executed under. Used by apply to bind itself to the
// producing plan's mode (TFE-style: a run carries its own execution mode,
// independent of the workspace's current settings). Missing fields default
// to subprocess + empty image — preserves backward compatibility for run
// snapshots written before the runtime layer existed.
func readRequestSnapshot(runDir string) (mode RunMode, image string, err error) {
	data, err := os.ReadFile(filepath.Join(runDir, "request.txt"))
	if err != nil {
		return RunModeSubprocess, "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		eq := strings.Index(line, "=")
		if eq <= 0 {
			continue
		}
		key, val := line[:eq], line[eq+1:]
		switch key {
		case "run_mode":
			mode = RunMode(val)
		case "image":
			image = val
		}
	}
	if mode == RunModeUnset {
		mode = RunModeSubprocess
	}
	return mode, image, nil
}
