// Package widgets holds reusable GTK composites that need too much logic to
// live in Blueprint. The log view, plan diff (M3), and state tree (M3) all
// land here.
package widgets

import (
	"html"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/domain"
)

// LogView is a read-only, monospaced log pane with autoscroll-to-bottom
// behaviour. Pairs with bridge.PumpRun on the consumer side: each LogLine
// arriving on the GTK main thread is appended via Append.
//
// Coloring uses Pango markup spans embedded in the buffer (red errors, amber
// warnings, dimmed debug) — gotk4's TextTag property surface is awkward, and
// markup gives equivalent results at a fraction of the code.
type LogView struct {
	scroller *gtk.ScrolledWindow
	view     *gtk.TextView
	buf      *gtk.TextBuffer

	// stickToBottom tracks whether the viewport sits at the tail. When true,
	// Append scrolls back to the bottom; when false (user pulled away), we
	// don't fight their scroll position.
	stickToBottom bool
}

// NewLogView builds an empty log view ready to embed.
func NewLogView() *LogView {
	view := gtk.NewTextView()
	view.SetEditable(false)
	view.SetCursorVisible(false)
	view.SetMonospace(true)
	view.SetWrapMode(gtk.WrapNone)
	view.SetLeftMargin(8)
	view.SetRightMargin(8)
	view.SetTopMargin(4)
	view.SetBottomMargin(4)
	view.AddCSSClass("log-view")

	scroller := gtk.NewScrolledWindow()
	scroller.SetHExpand(true)
	scroller.SetVExpand(true)
	scroller.SetChild(view)

	lv := &LogView{
		scroller:      scroller,
		view:          view,
		buf:           view.Buffer(),
		stickToBottom: true,
	}

	vadj := scroller.VAdjustment()
	vadj.ConnectValueChanged(func() {
		lv.stickToBottom = vadj.Value()+vadj.PageSize() >= vadj.Upper()-1
	})

	return lv
}

// Root returns the top-level widget for embedding.
func (lv *LogView) Root() *gtk.ScrolledWindow { return lv.scroller }

// Append adds one log line, formatting it based on the parsed JSON metadata
// when available. Must be called on the GTK main thread.
func (lv *LogView) Append(line domain.LogLine) {
	markup, ok := renderLine(line)
	if !ok {
		return
	}

	end := lv.buf.EndIter()
	lv.buf.InsertMarkup(end, markup+"\n")

	if lv.stickToBottom {
		mark := lv.buf.CreateMark("", lv.buf.EndIter(), false)
		lv.view.ScrollMarkOnscreen(mark)
		lv.buf.DeleteMark(mark)
	}
}

// Clear empties the buffer.
func (lv *LogView) Clear() {
	start, end := lv.buf.Bounds()
	lv.buf.Delete(start, end)
}

// renderLine formats a LogLine as Pango markup. Returns false for empty
// lines we want to skip entirely.
func renderLine(line domain.LogLine) (string, bool) {
	text := pickDisplay(line)
	if text == "" {
		return "", false
	}
	escaped := html.EscapeString(text)

	if line.Stream == domain.StreamStderr {
		return wrap(escaped, `foreground="#cccccc"`), true
	}
	if line.JSON != nil {
		level, _ := line.JSON["@level"].(string)
		switch strings.ToLower(level) {
		case "error":
			return wrap(escaped, `foreground="#e07070" weight="bold"`), true
		case "warn", "warning":
			return wrap(escaped, `foreground="#d8a050"`), true
		case "trace", "debug":
			return wrap(escaped, `foreground="#888888"`), true
		}
	}
	return escaped, true
}

func pickDisplay(line domain.LogLine) string {
	if line.JSON != nil {
		if msg, ok := line.JSON["@message"].(string); ok && msg != "" {
			return msg
		}
	}
	return line.Text
}

func wrap(escaped, spanAttrs string) string {
	return `<span ` + spanAttrs + `>` + escaped + `</span>`
}
