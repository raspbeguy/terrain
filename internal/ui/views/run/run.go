// Package run owns the run detail view (log + Cancel/Apply/Discard/Back).
package run

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	tfjson "github.com/hashicorp/terraform-json"

	"github.com/raspbeguy/terrain/internal/domain"
	"github.com/raspbeguy/terrain/internal/ui/bridge"
	"github.com/raspbeguy/terrain/internal/ui/uihelpers"
	"github.com/raspbeguy/terrain/internal/ui/widgets"
)

type tfjsonPlan = tfjson.Plan

const uiResource = "/io/github/raspbeguy/Terrain/run-detail.ui"

// Page is reused across runs; Start() rebinds and rewires the buttons.
type Page struct {
	root       *gtk.Box
	cancelBtn  *gtk.Button
	applyBtn   *gtk.Button
	discardBtn *gtk.Button
	backBtn    *gtk.Button

	log  *widgets.LogView
	plan *widgets.PlanDiff

	cancelFunc domain.CancelFunc
	onApply    func(*domain.PlanResult)
	onDiscard  func()
	onBack     func()
	onStatus   func(domain.RunStatus, string)

	// latestPlan is captured so onApply can forward the plan file path.
	latestPlan *domain.PlanResult
}

func New() *Page {
	builder := gtk.NewBuilderFromResource(uiResource)
	p := &Page{
		root:       uihelpers.MustCast[*gtk.Box](builder, "run_detail_root"),
		cancelBtn:  uihelpers.MustCast[*gtk.Button](builder, "run_cancel_button"),
		applyBtn:   uihelpers.MustCast[*gtk.Button](builder, "run_apply_button"),
		discardBtn: uihelpers.MustCast[*gtk.Button](builder, "run_discard_button"),
		backBtn:    uihelpers.MustCast[*gtk.Button](builder, "run_back_button"),
		log:        widgets.NewLogView(),
		plan:       widgets.NewPlanDiff(),
	}

	embedInBin(builder, "run_log_container", p.log.Root())
	embedInBin(builder, "run_plan_container", p.plan.Root())

	p.cancelBtn.ConnectClicked(func() { p.requestCancel() })
	p.applyBtn.ConnectClicked(func() {
		if p.onApply != nil && p.latestPlan != nil {
			p.onApply(p.latestPlan)
		}
	})
	p.discardBtn.ConnectClicked(func() {
		if p.onDiscard != nil {
			p.onDiscard()
		}
	})
	p.backBtn.ConnectClicked(func() {
		if p.onBack != nil {
			p.onBack()
		}
	})
	return p
}

func (p *Page) Root() *gtk.Box { return p.root }

func (p *Page) SetOnBack(fn func()) { p.onBack = fn }

func (p *Page) SetOnStatus(fn func(domain.RunStatus, string)) { p.onStatus = fn }

// Start replaces any previous run binding; safe to call repeatedly.
func (p *Page) Start(
	run domain.Run,
	stream domain.RunStream,
	cancel domain.CancelFunc,
	onApply func(*domain.PlanResult),
	onDiscard func(),
) {
	p.log.Clear()
	p.cancelFunc = cancel
	p.onApply = onApply
	p.onDiscard = onDiscard
	p.latestPlan = nil

	if p.onStatus != nil {
		p.onStatus(run.Status, string(run.Kind)+" · "+truncateRunID(run.ID))
	}

	p.cancelBtn.SetVisible(true)
	p.applyBtn.SetVisible(false)
	p.discardBtn.SetVisible(false)

	bridge.PumpRun(stream, bridge.RunSinks{
		OnEvent: p.handleEvent,
		OnLog:   p.log.Append,
		OnPlan:  p.handlePlan,
		OnDone:  p.handleDone,
	})
}

func (p *Page) handlePlan(r *domain.PlanResult) {
	p.latestPlan = r
	if r == nil {
		return
	}
	if plan, ok := r.Parsed.(*tfjsonPlan); ok {
		p.plan.Bind(plan)
	}
}

func (p *Page) handleEvent(ev domain.RunEvent) {
	slog.Debug("run event", "status", ev.Status, "msg", ev.Message)
	if p.onStatus != nil {
		p.onStatus(ev.Status, ev.Message)
	}

	active := ev.Status.Active() || ev.Status == domain.StatusPending
	p.cancelBtn.SetVisible(active)

	planned := ev.Status == domain.StatusPlanned
	p.applyBtn.SetVisible(planned)
	p.discardBtn.SetVisible(planned)
}

func (p *Page) handleDone(err error) {
	p.cancelBtn.SetVisible(false)
	if err != nil {
		slog.Info("run finished", "err", err)
	}
}

func (p *Page) requestCancel() {
	if p.cancelFunc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.cancelFunc(ctx); err != nil {
		slog.Error("cancel run", "err", err)
	}
}

// LoadHistory replays a finished run from disk artifacts (stdout.log /
// stderr.log / plan.json). Read-only — only Back stays visible. Requires
// r.RunDir; remote backends that don't persist locally aren't replayable.
func (p *Page) LoadHistory(r domain.Run) {
	p.log.Clear()
	p.plan.Bind(nil)
	p.cancelFunc = nil
	p.onApply = nil
	p.latestPlan = nil

	if p.onStatus != nil {
		p.onStatus(r.Status, string(r.Kind)+" · "+truncateRunID(r.ID))
	}

	p.cancelBtn.SetVisible(false)
	p.applyBtn.SetVisible(false)
	p.discardBtn.SetVisible(false)

	if r.RunDir == "" {
		p.log.Append(domain.LogLine{
			At:     time.Now(),
			Stream: domain.StreamStdout,
			Text:   "(no persisted artifacts for this run)",
		})
		return
	}

	if err := p.loadLogFiles(r.RunDir); err != nil {
		slog.Warn("replay logs", "dir", r.RunDir, "err", err)
	}
	p.loadPlanJSON(r.RunDir)
}

// loadLogFiles concatenates stdout then stderr; we don't store per-line
// timestamps, so true interleaving isn't possible.
func (p *Page) loadLogFiles(runDir string) error {
	stdoutPath := filepath.Join(runDir, "stdout.log")
	stderrPath := filepath.Join(runDir, "stderr.log")

	stdoutErr := streamLogFile(stdoutPath, domain.StreamStdout, p.log)
	stderrErr := streamLogFile(stderrPath, domain.StreamStderr, p.log)

	switch {
	case stdoutErr != nil && stderrErr != nil:
		return errors.Join(stdoutErr, stderrErr)
	case stdoutErr != nil:
		return stdoutErr
	case stderrErr != nil:
		return stderrErr
	}
	return nil
}

// streamLogFile treats a missing file as empty (a crashed-early plan
// may not have stderr.log).
func streamLogFile(path string, stream domain.Stream, log *widgets.LogView) error {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	for scanner.Scan() {
		text := scanner.Text()
		line := domain.LogLine{
			Stream: stream,
			Text:   text,
		}
		// JSON-parse stdout so @level styling carries through.
		if stream == domain.StreamStdout && len(text) > 0 && text[0] == '{' {
			var parsed map[string]any
			if err := json.Unmarshal([]byte(text), &parsed); err == nil {
				line.JSON = parsed
			}
		}
		log.Append(line)
	}
	return scanner.Err()
}

// loadPlanJSON treats a missing plan.json as expected (apply runs).
func (p *Page) loadPlanJSON(runDir string) {
	data, err := os.ReadFile(filepath.Join(runDir, "plan.json"))
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			slog.Warn("read plan.json", "dir", runDir, "err", err)
		}
		return
	}
	var plan tfjson.Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		slog.Warn("decode plan.json", "dir", runDir, "err", err)
		return
	}
	p.plan.Bind(&plan)
}

func truncateRunID(id string) string {
	const n = 12
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// embedInBin sets the child of an Adw.Bin (or any widget exposing
// SetChild(gtk.Widgetter)) without importing the adw package — keeps run.go
// from depending on libadwaita just to stuff a child widget.
func embedInBin(b *gtk.Builder, id string, child gtk.Widgetter) {
	obj := b.GetObject(id)
	if obj == nil {
		panic("builder: missing object id " + id)
	}
	setter, ok := obj.Cast().(interface{ SetChild(gtk.Widgetter) })
	if !ok {
		panic("builder: " + id + " has no SetChild")
	}
	setter.SetChild(child)
}
