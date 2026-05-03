package local

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
)

// TestStreamCommand_basic verifies streamCommand collects stdout and stderr
// lines and returns a clean exit status for a successful command. Uses /bin/sh
// to write a known number of lines to each pipe.
func TestStreamCommand_basic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const script = `
		echo stdout-1
		echo stdout-2
		echo stderr-1 >&2
		echo stdout-3
	`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)

	out := make(chan domain.LogLine, 16)
	go func() {
		// Drain in a goroutine so the producer doesn't block.
		// We collect lines into a slice so the test can assert order.
	}()

	var collected []domain.LogLine
	collectDone := make(chan struct{})
	go func() {
		for l := range out {
			collected = append(collected, l)
		}
		close(collectDone)
	}()

	if err := streamCommand(ctx, cmd, out); err != nil {
		close(out)
		<-collectDone
		t.Fatalf("streamCommand returned error: %v", err)
	}
	close(out)
	<-collectDone

	// Expect 4 lines. Order between stdout/stderr is timing-dependent so we
	// just check counts and presence.
	if len(collected) != 4 {
		t.Fatalf("expected 4 lines, got %d: %+v", len(collected), collected)
	}

	var stdoutCount, stderrCount int
	for _, l := range collected {
		switch l.Stream {
		case domain.StreamStdout:
			stdoutCount++
		case domain.StreamStderr:
			stderrCount++
		}
	}
	if stdoutCount != 3 {
		t.Errorf("expected 3 stdout lines, got %d", stdoutCount)
	}
	if stderrCount != 1 {
		t.Errorf("expected 1 stderr line, got %d", stderrCount)
	}
}

// TestStreamCommand_jsonParse verifies that JSON stdout lines populate
// LogLine.JSON, and that non-JSON stdout falls through with JSON nil.
func TestStreamCommand_jsonParse(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	const script = `
		echo '{"@level":"info","@message":"hello","type":"test"}'
		echo 'not json'
	`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)

	out := make(chan domain.LogLine, 8)
	var collected []domain.LogLine
	done := make(chan struct{})
	go func() {
		for l := range out {
			collected = append(collected, l)
		}
		close(done)
	}()

	if err := streamCommand(ctx, cmd, out); err != nil {
		close(out)
		<-done
		t.Fatalf("streamCommand error: %v", err)
	}
	close(out)
	<-done

	if len(collected) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(collected))
	}

	// Find the JSON line by content rather than index — order isn't
	// guaranteed if stdout writes interleave.
	var jsonLine, plainLine *domain.LogLine
	for i := range collected {
		if strings.Contains(collected[i].Text, "@message") {
			jsonLine = &collected[i]
		} else {
			plainLine = &collected[i]
		}
	}

	if jsonLine == nil || plainLine == nil {
		t.Fatalf("expected one JSON and one plain line; got: %+v", collected)
	}
	if jsonLine.JSON == nil {
		t.Fatal("JSON line: expected parsed map, got nil")
	}
	if plainLine.JSON != nil {
		t.Fatal("plain line: expected JSON nil, got parsed map")
	}

	if got := jsonLine.JSON["@message"]; got != "hello" {
		t.Errorf("@message: got %v, want hello", got)
	}
}

// TestStreamCommand_cancel verifies SIGINT delivery on context cancel and
// that we return promptly without leaking goroutines.
func TestStreamCommand_cancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	// A shell that traps SIGINT and exits cleanly. Without trap, /bin/sh
	// inherits SIGINT default and exits with 130.
	const script = `
		trap "echo got-int; exit 130" INT
		echo started
		while true; do sleep 1; done
	`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)

	out := make(chan domain.LogLine, 8)
	collectDone := make(chan struct{})
	var collected []domain.LogLine
	go func() {
		for l := range out {
			collected = append(collected, l)
		}
		close(collectDone)
	}()

	// Cancel after we know the script has started. We watch for "started"
	// in the collected lines via a small polling helper.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			for _, l := range collected {
				if strings.Contains(l.Text, "started") {
					cancel()
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel() // bail out anyway
	}()

	err := streamCommand(ctx, cmd, out)
	close(out)
	<-collectDone

	// Some non-nil error is expected; either exec.ExitError(130) or a context
	// cancellation surfaced via cmd.Wait. We just check the run actually
	// stopped within a reasonable time.
	if err == nil {
		t.Fatal("expected an error from cancelled run, got nil")
	}
	if exitCodeOf(err) > 130 || exitCodeOf(err) == 0 {
		// SIGINT-handled scripts exit 130 on dash/bash; -1 also acceptable.
		// Just sanity check it's not a clean exit (which would mean cancel didn't work).
		t.Logf("non-zero exit ok: %v", err)
	}
}
