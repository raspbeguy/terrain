// Package bridge forwards domain channels to the GTK main thread via glib.IdleAdd.
package bridge

import (
	"sync"

	"github.com/diamondburned/gotk4/pkg/glib/v2"

	"github.com/raspbeguy/terrain/internal/domain"
)

// Nil fields drop their channel so the worker doesn't block on a missing consumer.
type RunSinks struct {
	OnEvent func(domain.RunEvent)
	OnLog   func(domain.LogLine)
	OnPlan  func(*domain.PlanResult)
	OnDone  func(error)
}

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

	// Queue events/logs/plan before OnDone; same-priority IdleAdd is FIFO.
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
