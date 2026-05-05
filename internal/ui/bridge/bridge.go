// Package bridge is the canonical crossing point between domain
// channels and the GTK main thread. gotk4 widgets are not thread-safe;
// touching them from a non-main goroutine is undefined behaviour.
// PumpRun forwards channel items to sinks via glib.IdleAdd, ordering
// OnDone after all data pumps have queued their last items.
package bridge

import (
	"sync"

	"github.com/diamondburned/gotk4/pkg/glib/v2"

	"github.com/raspbeguy/terrain/internal/domain"
)

// RunSinks: nil fields drop their channel quietly so the worker
// doesn't block on a missing consumer.
type RunSinks struct {
	OnEvent func(domain.RunEvent)
	OnLog   func(domain.LogLine)
	// OnPlan fires once after a successful plan.
	OnPlan func(*domain.PlanResult)
	OnDone func(error)
}

// OnMainThread schedules fn on the GTK main thread. Use from background
// goroutines for one-shot results that don't fit the PumpRun shape.
func OnMainThread(fn func()) {
	glib.IdleAdd(fn)
}

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

	// Wait so events/logs/plan callbacks queue before OnDone (GLib
	// same-priority IdleAdd is FIFO, preserving emission order).
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
