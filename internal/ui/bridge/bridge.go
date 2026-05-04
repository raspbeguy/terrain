// Package bridge is the SOLE crossing point between domain channels and
// the GTK main thread. Domain code (internal/domain, internal/backend/...,
// internal/runner/...) emits events on Go channels in arbitrary goroutines.
// Widgets MUST NOT be touched from those goroutines — gotk4 widgets are not
// thread-safe and stray calls cause undefined behaviour (segfaults, not
// panics).
//
// Pump* functions in this package consume the domain channels in background
// goroutines and forward each item to user-supplied callbacks via
// glib.IdleAdd, which schedules the callback on the GTK main thread.
//
// The OnDone callback is fired LAST — after the data pumps have queued all
// their remaining IdleAdd calls. Because GLib's IdleAdd at equal priority
// is FIFO, this guarantees the user-visible callback order matches the
// emission order from the worker side: every event/log/plan callback fires
// before OnDone.
//
// Linter rule: this should be the only package importing both
// `github.com/raspbeguy/terrain/internal/domain` and
// `github.com/diamondburned/gotk4/pkg/glib/v2`. The one sanctioned
// exception lives in `internal/ui/dialogs/addremote_idle.go` (one-shot
// async UI update from the Add Remote Backend's Test Connection flow);
// new call sites need the same justification or extend bridge instead.
package bridge

import (
	"sync"

	"github.com/diamondburned/gotk4/pkg/glib/v2"

	"github.com/raspbeguy/terrain/internal/domain"
)

// RunSinks bundles the UI-side callbacks for a run. All fields are optional;
// nil sinks cause their channel to be drained-and-dropped, which keeps the
// worker from blocking on a backpressured consumer.
type RunSinks struct {
	OnEvent func(domain.RunEvent)
	OnLog   func(domain.LogLine)
	OnPlan  func(*domain.PlanResult) // nil-safe; called once after a successful plan
	OnDone  func(error)
}

// OnMainThread schedules fn to run on the GTK main thread. Use from a
// background goroutine when you have a one-shot result (e.g. an async
// backend fetch returning a workspace list) that doesn't fit the streaming
// PumpRun shape. Centralizing the call here keeps the "no glib import
// outside bridge" architectural rule grep-provable.
func OnMainThread(fn func()) {
	glib.IdleAdd(fn)
}

// PumpRun starts goroutines that drain stream's channels, forwarding items
// to sinks on the GTK main thread via glib.IdleAdd. Returns immediately.
//
// Ordering guarantee: when stream.Done() yields a value (worker is
// terminal), the data-pump goroutines may still be flushing their last
// items into glib.IdleAdd. PumpRun arranges for OnDone to run AFTER all
// data IdleAdd calls have been queued, so on the GTK main thread the
// last user-visible event fires before OnDone.
func PumpRun(stream domain.RunStream, sinks RunSinks) {
	if stream == nil {
		return
	}
	var wg sync.WaitGroup
	wg.Add(3)
	go pumpEvents(stream.Events(), sinks.OnEvent, &wg)
	go pumpLogs(stream.Logs(), sinks.OnLog, &wg)
	go pumpPlan(stream.Plan(), sinks.OnPlan, &wg)
	go pumpDone(stream.Done(), sinks.OnDone, &wg)
}

func pumpEvents(in <-chan domain.RunEvent, sink func(domain.RunEvent), wg *sync.WaitGroup) {
	defer wg.Done()
	if sink == nil {
		drainEvents(in)
		return
	}
	for ev := range in {
		ev := ev
		glib.IdleAdd(func() { sink(ev) })
	}
}

func pumpLogs(in <-chan domain.LogLine, sink func(domain.LogLine), wg *sync.WaitGroup) {
	defer wg.Done()
	if sink == nil {
		drainLogs(in)
		return
	}
	for line := range in {
		line := line
		glib.IdleAdd(func() { sink(line) })
	}
}

func pumpPlan(in <-chan *domain.PlanResult, sink func(*domain.PlanResult), wg *sync.WaitGroup) {
	defer wg.Done()
	if sink == nil {
		for range in {
		}
		return
	}
	for r := range in {
		r := r
		glib.IdleAdd(func() { sink(r) })
	}
}

func pumpDone(in <-chan error, sink func(error), wg *sync.WaitGroup) {
	err, ok := <-in
	if !ok {
		err = nil
	}

	// Wait for the three data pumps to finish queueing their last IdleAdd
	// calls before we queue OnDone. With GLib's same-priority FIFO, this
	// keeps the visible ordering correct: events/logs/plan callbacks fire
	// before OnDone.
	wg.Wait()

	if sink == nil {
		return
	}
	glib.IdleAdd(func() { sink(err) })
}

func drainEvents(c <-chan domain.RunEvent) {
	for range c {
	}
}

func drainLogs(c <-chan domain.LogLine) {
	for range c {
	}
}
