package local

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
)

// graceWindow is how long we wait between SIGINT and SIGKILL when cancelling.
// 5s gives tofu/terraform time to release locks and write state cleanly;
// longer than that and the user starts wondering whether Cancel "did" anything.
const graceWindow = 5 * time.Second

// streamCommand starts cmd, drains stdout/stderr line-by-line into out,
// and waits for the process to exit. On ctx cancel, the command receives
// SIGINT; if it hasn't exited within graceWindow, exec.Cmd will SIGKILL it
// via WaitDelay.
//
// The caller owns the out channel and is responsible for closing it after
// streamCommand returns (we don't close inside because the caller may want
// to push synthetic events on the same channel before signaling completion).
//
// Return value:
//   - nil if cmd exited cleanly with status 0
//   - *exec.ExitError if cmd exited with a non-zero status (errored or canceled)
//   - other errors for setup failures (pipe creation, Start)
func streamCommand(ctx context.Context, cmd *exec.Cmd, out chan<- domain.LogLine) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	// Wire ctx → (chained pre-cancel hook) → SIGINT → (after grace) SIGKILL.
	// cmd.Cancel runs on its own goroutine when ctx is done; cmd.WaitDelay
	// caps how long Wait will hang after Cancel before forcibly killing the
	// process group. If the caller already installed a Cancel hook (e.g.
	// the container runtime's `podman kill --signal INT <name>`), we run
	// it first, then fall through to the SIGINT-to-wrapper-process belt-
	// and-suspenders signal — that way both signal paths are tried.
	priorCancel := cmd.Cancel
	cmd.Cancel = func() error {
		if priorCancel != nil {
			_ = priorCancel()
		}
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(syscall.SIGINT)
	}
	cmd.WaitDelay = graceWindow

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		scanLines(stdout, domain.StreamStdout, out)
	}()
	go func() {
		defer wg.Done()
		scanLines(stderr, domain.StreamStderr, out)
	}()

	// Wait for both readers to drain (process closes pipes when it exits).
	wg.Wait()

	// cmd.Wait reaps the process and reports exit status.
	return cmd.Wait()
}

// scanLines reads r line-by-line and pushes a LogLine for each. JSON parsing
// is attempted on stdout (where `-json` ndjson lives); failures fall through
// silently with line.JSON == nil so the UI can render the raw Text.
//
// We bump the scanner buffer to 1 MB because tofu plan -json's
// `planned_change` events for large resources can produce single lines well
// past the default 64 KB cap.
func scanLines(r io.Reader, stream domain.Stream, out chan<- domain.LogLine) {
	const maxLine = 1 << 20 // 1 MB
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), maxLine)

	for scanner.Scan() {
		text := scanner.Text()
		line := domain.LogLine{
			At:     time.Now(),
			Stream: stream,
			Text:   text,
		}
		if stream == domain.StreamStdout && len(text) > 0 && text[0] == '{' {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(text), &parsed); err == nil {
				line.JSON = parsed
			}
		}
		out <- line
	}
	// Scanner errors after EOF (broken pipe on cancellation, oversize line)
	// surface as a synthetic stderr line so the UI sees them. ErrClosedPipe
	// and io.EOF are not surfaced — those are normal termination.
	if err := scanner.Err(); err != nil &&
		!errors.Is(err, io.EOF) &&
		!errors.Is(err, io.ErrClosedPipe) {
		out <- domain.LogLine{
			At:     time.Now(),
			Stream: domain.StreamStderr,
			Text:   "[scanner] " + err.Error(),
		}
	}
}

// exitCodeOf extracts the process exit code from cmd.Wait's error. Returns 0
// if the run terminated cleanly, the actual exit code on a non-zero exit,
// and -1 on contexts where we can't determine it (signal kill).
func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}
