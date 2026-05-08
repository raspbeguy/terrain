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

// runStream's channels are owned by the worker goroutine; consumers
// must only read.
type runStream struct {
	events chan domain.RunEvent
	logs   chan domain.LogLine
	plan   chan *domain.PlanResult
	done   chan error
}

func newRunStream() *runStream {
	return &runStream{
		// Buffered so cancel/error events never block on a slow UI.
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

func (b *Backend) startRun(_ context.Context, req domain.RunRequest) (domain.Run, domain.RunStream, domain.CancelFunc, error) {
	wsCtx, wsCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wsCancel()
	ws, err := b.Workspace(wsCtx, req.WorkspaceID)
	if err != nil {
		return domain.Run{}, nil, nil, fmt.Errorf("resolve workspace: %w", err)
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

	// Detached context — runs outlive the GUI callback that started them.
	runCtx, cancelCtx := context.WithCancel(context.Background())
	stream := newRunStream()

	cancelFn := func(_ context.Context) error {
		cancelCtx()
		return nil
	}

	go runWorker(runCtx, b, run, ws, req, runDir, stream)

	return run, stream, cancelFn, nil
}

// runWorker owns one run: initial snapshot → subprocess → log tee →
// terminal events → history record → channel close. b is threaded
// through so the worker can call b.materialize to resolve sensitive +
// env-category vars from the keyring.
func runWorker(
	ctx context.Context,
	b *Backend,
	run domain.Run,
	ws domain.Workspace,
	req domain.RunRequest,
	runDir string,
	stream *runStream,
) {
	var (
		finalErr   error
		lastStatus = domain.StatusPending
		exitCode   int
	)

	// Apply persists a backref to the plan file it consumed so the runs
	// list can mark plan rows whose plan was applied.
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
			// Surface the drop in the log so the user notices; give up
			// silently if logs are also backpressured.
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

	// done must close AFTER events/logs/plan so a Done() consumer knows
	// every prior channel is drained.
	defer func() {
		recordHistory(run, runDir, lastStatus, finalErr, exitCode)
		go func() {
			refreshCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := b.RefreshWorkspaces(refreshCtx, ws.ProjectID); err != nil {
				slog.Debug("post-run workspace refresh", "ws", ws.ID, "err", err)
			}
		}()
		select {
		case stream.done <- finalErr:
		default:
		}
		close(stream.done)
	}()

	setStatus(domain.StatusPending, "queued")

	wsSettings, err := LoadWorkspaceSettings(b.id, ws.ID)
	if err != nil {
		finalErr = fmt.Errorf("load workspace settings: %w", err)
		setStatus(domain.StatusErrored, finalErr.Error())
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		return
	}

	// Resolve binary first because managed mode may download (slow).
	effectiveSource := wsSettings.BinarySource.Effective()
	managedEngine := wsSettings.EffectiveManagedEngine(b.defaults.Engine)
	managedVersion := wsSettings.ManagedVersion
	if effectiveSource == BinarySourceManaged && wsSettings.ManagedTrackLatest {
		setStatus(domain.StatusFetching,
			"checking latest "+managedEngine+" release")
		latest, lerr := LatestManagedVersion(ctx, managedEngine)
		switch {
		case lerr == nil:
			managedVersion = latest
		case wsSettings.ManagedVersion != "":
			slog.Warn("latest version lookup failed, falling back to pinned",
				"engine", managedEngine, "pinned", wsSettings.ManagedVersion, "err", lerr)
		default:
			finalErr = fmt.Errorf("resolve latest %s: %w", managedEngine, lerr)
			setStatus(domain.StatusErrored, finalErr.Error())
			close(stream.events)
			close(stream.logs)
			close(stream.plan)
			return
		}
	}
	if effectiveSource == BinarySourceManaged {
		setStatus(domain.StatusFetching,
			fmt.Sprintf("fetching %s %s", managedEngine, managedVersion))
	}
	bin, err := b.binaryResolver(wsSettings).Resolve(ctx, managedEngine, managedVersion)
	if err != nil {
		finalErr = fmt.Errorf("resolve binary: %w", err)
		setStatus(domain.StatusErrored, finalErr.Error())
		close(stream.events)
		close(stream.logs)
		close(stream.plan)
		return
	}

	// Apply binds to the producing plan's mode/image (read from its
	// request.txt) so toggling workspace settings mid-flow can't strand
	// a plan whose paths reference the original sandbox.
	rtOpts, err := b.resolveRuntimeOptions(ws.ID, run.ID, wsSettings)
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
		// Non-fatal: the live log channel still works, just no
		// persistence to disk.
		slog.Warn("open run log files", "ws", ws.ID, "err", logErr)
	}
	defer closeIfNonNil(stdoutLog)
	defer closeIfNonNil(stderrLog)

	// Sensitive + env vars resolve from the keyring into a per-run
	// var-file (deleted on exit so secrets don't linger in cache).
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

	// One tee goroutine drains all streamCommand calls (init + main) into
	// the same on-disk log files and live channel; closed after the last.
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

	// Image pre-pull (container mode only); host + bwrap return nil here.
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

	// Plan/destroy always init first; apply replays a saved plan that's
	// already been through init.
	if req.Kind == domain.RunKindPlan || req.Kind == domain.RunKindDestroy {
		setStatus(domain.StatusFetching,
			fmt.Sprintf("running `%s init -input=false`", bin.Name))
		initCmd := rt.Command(ctx, ws.WorkingDirectory,
			[]string{"NO_COLOR=1", "TF_WORKSPACE=" + ws.Name},
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

	extraEnv := append([]string{"NO_COLOR=1", "TF_WORKSPACE=" + ws.Name}, rv.envEntries()...)
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

// formatArgsForLog redacts `-var KEY=VAL` payloads before they hit the
// on-disk run history.
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

// buildCmdArgs returns the CLI args + the produced plan-file path
// (empty for apply, which consumes a prior plan rather than producing one).
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

// recordHistory is best-effort: called from a deferred shutdown path,
// so failures only get logged.
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

// isCancelError distinguishes "we cancelled it" from "tofu failed for
// its own reasons".
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

// runArtifactsDir returns the per-run dir under XDG_CACHE_HOME — runs
// are ephemeral; state versions live under XDG_DATA_HOME instead.
func runArtifactsDir(backendID string, ws domain.Workspace, runID string) (string, error) {
	cacheHome, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache dir: %w", err)
	}
	safeWS := strings.NewReplacer("/", "_", ":", "_").Replace(ws.ID)
	return filepath.Join(cacheHome, "terrain", backendID, safeWS, "runs", runID), nil
}

// newRunID returns a time-prefixed identifier so on-disk listing is
// chronological without a separate index.
func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return fmt.Sprintf("%d-%s", time.Now().Unix(), hex.EncodeToString(b[:]))
}

// openLogFiles returns both files non-nil on success and both nil on
// error; caller closes both.
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

// readRequestSnapshot recovers the run-mode + image fields from a prior
// run's request.txt. Missing fields default to subprocess + empty so
// pre-runtime-layer snapshots still load.
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
