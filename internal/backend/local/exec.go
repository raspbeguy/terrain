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

// graceWindow is the SIGINT→SIGKILL delay; long enough for tofu to
// release locks and write state, short enough that Cancel feels alive.
const graceWindow = 5 * time.Second

// streamCommand drains stdout/stderr into out and waits for cmd to
// exit. Caller owns + closes out (we may push synthetic events after
// streamCommand returns).
//
// Return: nil on exit 0; *exec.ExitError for non-zero / cancel; other
// errors for setup failures.
func streamCommand(ctx context.Context, cmd *exec.Cmd, out chan<- domain.LogLine) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	// Cancel chain: prior hook (e.g. runtime.Cancel for container kill)
	// runs first, then SIGINT to the wrapper. WaitDelay caps Wait at
	// graceWindow before exec.Cmd issues SIGKILL.
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

	wg.Wait()
	return cmd.Wait()
}

// scanLines: 1 MB buffer because tofu plan -json's planned_change for
// large resources can blow past the 64 KB default.
func scanLines(r io.Reader, stream domain.Stream, out chan<- domain.LogLine) {
	const maxLine = 1 << 20
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
	// Scanner errors that aren't normal termination (oversize line,
	// post-cancel pipe error other than EOF/ClosedPipe) surface to the
	// log so the UI can show them.
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

// exitCodeOf returns -1 when the exit code is unknown (signal kill,
// non-ExitError failures).
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
