// Package widgets holds GTK composites with too much logic for Blueprint.
package widgets

import (
	"html"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"github.com/raspbeguy/terrain/internal/domain"
)

// LogView: read-only monospaced log pane that autoscrolls when the
// viewport is at the tail. Coloring uses Pango markup (TextTag would work
// but is awkward in gotk4).
type LogView struct {
	scroller      *gtk.ScrolledWindow
	view          *gtk.TextView
	buf           *gtk.TextBuffer
	stickToBottom bool
}

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

func (lv *LogView) Root() *gtk.ScrolledWindow { return lv.scroller }

// Append must be called on the GTK main thread.
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

func (lv *LogView) Clear() {
	start, end := lv.buf.Bounds()
	lv.buf.Delete(start, end)
}

// renderLine returns false for lines that should be skipped.
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
