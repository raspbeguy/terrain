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

// SIGINT to SIGKILL delay: lets tofu release locks and write state.
const graceWindow = 5 * time.Second

// Caller owns and closes out; we may push synthetic events after return.
func streamCommand(ctx context.Context, cmd *exec.Cmd, out chan<- domain.LogLine) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

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

// 1 MB buffer: tofu plan -json planned_change can exceed 64 KB default.
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

// Returns -1 when the exit code is unknown (signal kill, non-ExitError).
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
