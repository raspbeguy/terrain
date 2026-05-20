package local

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/raspbeguy/terrain/internal/domain"
)

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

func TestStreamCommand_cancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())

	const script = `
		trap "echo got-int; exit 130" INT
		echo started
		while true; do sleep 1; done
	`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)

	out := make(chan domain.LogLine, 8)
	collectDone := make(chan struct{})
	started := make(chan struct{})
	var collected []domain.LogLine
	go func() {
		sawStart := false
		for l := range out {
			collected = append(collected, l)
			if !sawStart && strings.Contains(l.Text, "started") {
				sawStart = true
				close(started)
			}
		}
		close(collectDone)
	}()

	go func() {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
		}
		cancel()
	}()

	err := streamCommand(ctx, cmd, out)
	close(out)
	<-collectDone

	if err == nil {
		t.Fatal("expected an error from cancelled run, got nil")
	}
	if exitCodeOf(err) > 130 || exitCodeOf(err) == 0 {
		t.Logf("non-zero exit ok: %v", err)
	}
}
