// Package terminalpane provides a tview widget that maintains a real
// terminal-emulator surface for ANSI bytes arriving on a channel.
//
// Unlike streampane (a log viewer that strips ANSI sequences and renders the
// trailing lines as plain text), TerminalPane feeds inbound bytes into a
// VT100-compatible emulator (charmbracelet/x/vt) and paints the resulting
// cell grid directly to a tcell.Screen. Cursor positioning, screen clears,
// SGR colors, UTF-8 multi-byte glyphs, and the rest of the standard VT
// repertoire are handled natively — full-screen-refresh-style emitters
// (tview, ncurses-likes) render correctly without confetti.
//
// Plugin views (PR 8) mount this widget. The public API is intentionally
// shaped to mirror streampane so the swap in plugin_views.go is mechanical.
package terminalpane

import (
	"image/color"
	"io"
	"sync"
	"sync/atomic"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	xvt "github.com/charmbracelet/x/vt"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/anutron/argus-sdk/keyenc"
	"github.com/anutron/argus-sdk/theme"
	"github.com/anutron/argus-sdk/widget"
)

// Default emulator dimensions until Draw or Resize provides a real size.
const (
	defaultCols = 80
	defaultRows = 24
	minCols     = 2
	minRows     = 2
)

// TerminalPane renders an ANSI byte stream through a VT emulator.
type TerminalPane struct {
	*tview.Box

	mu    sync.Mutex
	title string

	emu  *xvt.SafeEmulator
	cols int
	rows int

	// scrollOffset is how many lines the view is scrolled up into the
	// emulator's scrollback history. 0 means live (bottom). Guarded by mu.
	scrollOffset int
	// anchorSBLen is ScrollbackLen at the moment scrollOffset was last set.
	// While scrolled, new output landing in scrollback grows the effective
	// offset by the delta so the viewed content stays pinned (anchor-lock).
	// Guarded by mu.
	anchorSBLen int

	// cursorHidden tracks the emulator's DECTCEM cursor-hide state. Updated
	// via the CursorVisibility callback; read in paint(). Guarded by mu.
	cursorHidden bool

	touched uint64 // accessed via sync/atomic

	source    <-chan []byte
	inputBack chan<- []byte

	closeOnce sync.Once
	closeCh   chan struct{}
	done      chan struct{}

	// OnNeedRedraw, when set, fires once per non-empty inbound chunk so the
	// surrounding app can queue a redraw. Safe to leave nil.
	OnNeedRedraw func()
}

// New constructs a TerminalPane that consumes ANSI bytes from source.
//
// The emulator starts at 80x24; Draw and Resize adopt the real dimensions
// once they become known. The consumer goroutine exits when source is
// closed or Close is called.
func New(source <-chan []byte) *TerminalPane {
	tp := &TerminalPane{
		Box:     tview.NewBox(),
		cols:    defaultCols,
		rows:    defaultRows,
		source:  source,
		closeCh: make(chan struct{}),
		done:    make(chan struct{}),
	}
	tp.emu = newDrainedEmulator(tp.cols, tp.rows)
	tp.emu.SetCallbacks(xvt.Callbacks{
		CursorVisibility: func(visible bool) {
			tp.mu.Lock()
			tp.cursorHidden = !visible
			tp.mu.Unlock()
		},
	})

	// Intercept ED2 (ESC[2J) while in alt-screen to capture each rendered
	// frame to the main scrollback. x/vt routes scrollback pushes to the
	// active screen's own buffer; the alt screen's buffer is never visible
	// via ScrollbackLen/ScrollbackCellAt. Bubble Tea fires ED2 before every
	// full render, so each frame is captured as the operator scrolls back.
	//
	// Uses eRaw.* (raw Emulator) rather than tp.emu.* (SafeEmulator) —
	// SafeEmulator.Write holds mu.Lock; re-entering via CellAt would deadlock.
	// Returns false so the default ED2 handler (screen clear) also runs.
	eRaw := tp.emu.Emulator
	tp.emu.RegisterCsiHandler('J', func(params ansi.Params) bool {
		n, _, _ := params.Param(0, 0)
		if n != 2 || !eRaw.IsAltScreen() {
			return false
		}
		mainSB := eRaw.Scrollback()
		if mainSB == nil {
			return false
		}
		w, h := eRaw.Width(), eRaw.Height()
		for y := 0; y < h; y++ {
			hasContent := false
			for x := 0; x < w; x++ {
				cell := eRaw.CellAt(x, y)
				if cell != nil && cell.Content != "" && cell.Content != " " {
					hasContent = true
					break
				}
			}
			if !hasContent {
				continue
			}
			line := make(uv.Line, w)
			for x := 0; x < w; x++ {
				if cell := eRaw.CellAt(x, y); cell != nil {
					line[x] = *cell
				}
			}
			mainSB.Push(line)
		}
		return false
	})

	go tp.consume()
	return tp
}

// newDrainedEmulator creates an x/vt SafeEmulator with a goroutine draining
// the response pipe. x/vt uses io.Pipe internally — when the emulator parses
// terminal query sequences (DA1, DA2, DSR, etc.) it writes responses to its
// internal pipe, which blocks Write indefinitely without a reader.
func newDrainedEmulator(cols, rows int) *xvt.SafeEmulator {
	emu := xvt.NewSafeEmulator(cols, rows)
	go io.Copy(io.Discard, emu) //nolint:errcheck
	return emu
}

// SetTitle sets the title rendered in the top border.
func (tp *TerminalPane) SetTitle(t string) {
	tp.mu.Lock()
	tp.title = t
	tp.mu.Unlock()
}

// SetInputBack wires the channel that receives keystrokes and pasted text
// when the pane is focused. Pass nil to disable input forwarding.
func (tp *TerminalPane) SetInputBack(ch chan<- []byte) {
	tp.mu.Lock()
	tp.inputBack = ch
	tp.mu.Unlock()
}

// Touched returns a monotonic counter that increments every time a new
// non-empty chunk arrives from the source. Callers compare against a
// previous value to detect undrawn content.
func (tp *TerminalPane) Touched() uint64 {
	return atomic.LoadUint64(&tp.touched)
}

// Close stops the consumer goroutine. Safe to call multiple times.
func (tp *TerminalPane) Close() {
	tp.closeOnce.Do(func() { close(tp.closeCh) })
}

// Resize sets the emulator surface dimensions explicitly. Draw also auto-
// resizes when the inner rect changes, so callers don't need to invoke this
// for every layout shuffle — it's exposed so plugin_views can pre-size the
// emulator on activation before the first frame arrives.
func (tp *TerminalPane) Resize(cols, rows int) {
	if cols < minCols {
		cols = minCols
	}
	if rows < minRows {
		rows = minRows
	}
	tp.mu.Lock()
	defer tp.mu.Unlock()
	if tp.cols == cols && tp.rows == rows {
		return
	}
	tp.cols = cols
	tp.rows = rows
	tp.emu.Resize(cols, rows)
}

// ScrollBy adjusts the scrollback view offset by delta lines. Positive
// delta scrolls up into history; negative scrolls back toward live. The
// effective offset clamps to [0, ScrollbackLen] — scrolling past either
// end is a no-op beyond the boundary. Scrolling down past zero returns
// the pane to live and clears the anchor.
func (tp *TerminalPane) ScrollBy(delta int) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	off := tp.effectiveOffsetLocked() + delta
	if tp.emu != nil {
		if maxOff := tp.emu.ScrollbackLen(); off > maxOff {
			off = maxOff
		}
	} else {
		off = 0
	}
	if off <= 0 {
		tp.scrollOffset = 0
		tp.anchorSBLen = 0
		return
	}
	tp.scrollOffset = off
	tp.anchorSBLen = tp.emu.ScrollbackLen()
}

// ScrollOffset returns the current scrollback view offset, including any
// anchor-lock growth from output that arrived while scrolled. 0 means the
// pane is live (pinned to the bottom of output).
func (tp *TerminalPane) ScrollOffset() int {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.effectiveOffsetLocked()
}

// ResetScroll returns the pane to the live view.
func (tp *TerminalPane) ResetScroll() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	tp.scrollOffset = 0
	tp.anchorSBLen = 0
}

// effectiveOffsetLocked returns the scroll offset adjusted for output that
// landed in scrollback since the offset was last set (anchor-lock), clamped
// to the available history — the buffer may have trimmed at capacity since
// the anchor was recorded. Callers must hold tp.mu.
func (tp *TerminalPane) effectiveOffsetLocked() int {
	if tp.scrollOffset == 0 || tp.emu == nil {
		return 0
	}
	sbLen := tp.emu.ScrollbackLen()
	off := tp.scrollOffset + (sbLen - tp.anchorSBLen)
	if off > sbLen {
		off = sbLen
	}
	if off < 0 {
		off = 0
	}
	return off
}

// PTYSize returns the emulator's current cols/rows. Useful in tests.
func (tp *TerminalPane) PTYSize() (int, int) {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	return tp.cols, tp.rows
}

func (tp *TerminalPane) consume() {
	defer close(tp.done)
	for {
		select {
		case <-tp.closeCh:
			return
		case chunk, ok := <-tp.source:
			if !ok {
				return
			}
			if len(chunk) == 0 {
				continue
			}
			tp.feed(chunk)
			atomic.AddUint64(&tp.touched, 1)
			if tp.OnNeedRedraw != nil {
				tp.OnNeedRedraw()
			}
		}
	}
}

func (tp *TerminalPane) feed(b []byte) {
	tp.mu.Lock()
	emu := tp.emu
	tp.mu.Unlock()
	if emu == nil {
		return
	}
	_, _ = emu.Write(b)
}

// Draw paints the emulator surface onto screen inside a bordered panel.
func (tp *TerminalPane) Draw(screen tcell.Screen) {
	tp.DrawForSubclass(screen, tp)
	x, y, w, h := tp.GetRect()
	if w <= 0 || h <= 0 {
		return
	}

	tp.mu.Lock()
	title := tp.title
	tp.mu.Unlock()

	style := theme.StyleDimmed
	if tp.HasFocus() {
		style = tcell.StyleDefault
	}
	inner := widget.DrawBorderedPanel(screen, x, y, w, h, title, style)
	if inner.W <= 0 || inner.H <= 0 {
		return
	}

	// Adopt the inner rect as the emulator surface size. We do this on every
	// Draw so a Flex layout shuffle just-works without a separate resize RPC.
	if inner.W >= minCols && inner.H >= minRows {
		tp.Resize(inner.W, inner.H)
	}

	tp.paint(screen, inner.X, inner.Y, inner.W, inner.H)
}

// paint walks the emulator's surface and writes each cell to tcell. At
// scroll offset 0 the live main screen paints directly; while scrolled the
// visible window composes from scrollback history plus the live screen.
// After painting cells, places the hardware cursor when the pane has focus,
// DECTCEM is on, and the view is live (not scrolled); hides it otherwise.
func (tp *TerminalPane) paint(screen tcell.Screen, x, y, w, h int) {
	tp.mu.Lock()
	emu := tp.emu
	cols := tp.cols
	rows := tp.rows
	offset := tp.effectiveOffsetLocked()
	cursorHidden := tp.cursorHidden
	tp.mu.Unlock()
	if emu == nil {
		return
	}

	renderCols := min(cols, w)
	renderRows := min(rows, h)

	if offset > 0 {
		tp.paintScrolled(screen, emu, x, y, w, renderCols, renderRows, rows, offset)
		screen.HideCursor()
		return
	}

	for row := 0; row < renderRows; row++ {
		for col := 0; col < renderCols; col++ {
			ch, st := cellRuneStyle(emu.CellAt(col, row))
			screen.SetContent(x+col, y+row, ch, nil, st)
		}
	}

	if tp.HasFocus() && !cursorHidden {
		pos := emu.CursorPosition()
		cx, cy := pos.X, pos.Y
		if cx >= 0 && cx < renderCols && cy >= 0 && cy < renderRows {
			// Redraw the cursor cell with reverse-video so argus's plugin-view
			// host — which paints plugin cells but drops ShowCursor() for plugin
			// views — renders a visible cursor. The rune is preserved; only the
			// style is toggled. ShowCursor is kept for terminals that do handle it.
			ch, st := cellRuneStyle(emu.CellAt(cx, cy))
			screen.SetContent(x+cx, y+cy, ch, nil, st.Reverse(true))
			screen.ShowCursor(x+cx, y+cy)
			return
		}
	}
	screen.HideCursor()
}

// paintScrolled composes the visible window from the combined buffer —
// scrollback lines (index 0 = oldest) followed by the live screen rows.
// With total = ScrollbackLen + live rows, the window covers combined lines
// [total − offset − renderRows, total − offset). A [SCROLL] badge on the
// top content row marks scroll mode (mirrors argus's task terminal).
func (tp *TerminalPane) paintScrolled(screen tcell.Screen, emu *xvt.SafeEmulator, x, y, w, renderCols, renderRows, rows, offset int) {
	sbLen := emu.ScrollbackLen()
	if offset > sbLen {
		// The buffer may have trimmed at capacity since the offset was set.
		offset = sbLen
	}
	total := sbLen + rows
	end := total - offset // exclusive
	start := end - renderRows
	if start < 0 {
		start = 0
	}

	for screenRow := 0; screenRow < renderRows; screenRow++ {
		lineIdx := start + screenRow
		if lineIdx >= end {
			break
		}
		for col := 0; col < renderCols; col++ {
			var cell *uv.Cell
			if lineIdx < sbLen {
				cell = emu.ScrollbackCellAt(col, lineIdx)
			} else {
				cell = emu.CellAt(col, lineIdx-sbLen)
			}
			ch, st := cellRuneStyle(cell)
			screen.SetContent(x+col, y+screenRow, ch, nil, st)
		}
	}

	// Scroll-mode badge, centered on the top content row.
	const indicator = "   [SCROLL]   "
	badgeStyle := tcell.StyleDefault.Foreground(tcell.PaletteColor(214)).Bold(true)
	midX := x + (w-len(indicator))/2
	if midX < x {
		midX = x
	}
	for i, r := range indicator {
		if midX+i < x+w {
			screen.SetContent(midX+i, y, r, nil, badgeStyle)
		}
	}
}

// cellRuneStyle converts an emulator cell to the rune and style painted to
// tcell. Nil and empty cells render as a default-styled blank.
func cellRuneStyle(cell *uv.Cell) (rune, tcell.Style) {
	ch := ' '
	st := tcell.StyleDefault
	if cell != nil {
		if cell.Content != "" {
			rs := []rune(cell.Content)
			if len(rs) > 0 {
				ch = rs[0]
			}
		}
		st = uvCellToTcellStyle(cell)
	}
	return ch, st
}

// PasteHandler forwards pasted text to the configured InputBack channel.
// Without an input-back channel the handler is a non-blocking no-op.
func (tp *TerminalPane) PasteHandler() func(pastedText string, setFocus func(p tview.Primitive)) {
	return tp.WrapPasteHandler(func(pastedText string, _ func(p tview.Primitive)) {
		tp.send([]byte(pastedText))
	})
}

// InputHandler routes runes / mapped keys to the InputBack channel. Returns
// nil when no input-back channel is configured, leaving the widget read-only.
func (tp *TerminalPane) InputHandler() func(event *tcell.EventKey, setFocus func(p tview.Primitive)) {
	tp.mu.Lock()
	hasBack := tp.inputBack != nil
	tp.mu.Unlock()
	if !hasBack {
		return nil
	}
	return tp.WrapInputHandler(func(event *tcell.EventKey, _ func(p tview.Primitive)) {
		tp.send(eventBytes(event))
	})
}

// send writes b to the input-back channel without blocking. If the channel is
// full, the bytes are dropped — matches streampane / PTY writer behavior.
func (tp *TerminalPane) send(b []byte) {
	if len(b) == 0 {
		return
	}
	tp.mu.Lock()
	ch := tp.inputBack
	tp.mu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- b:
	default:
	}
}

// eventBytes maps a tcell key event to the bytes a remote plugin expects.
// It delegates to the shared keyenc.Encode — the single source of truth for
// key encoding. The prior local allowlist here silently dropped arrows and
// every modifier combo, so a plugin could never bind Ctrl/Alt/Shift+arrow;
// keyenc forwards them.
func eventBytes(ev *tcell.EventKey) []byte {
	return keyenc.Encode(ev)
}

// uvCellToTcellStyle converts an ultraviolet cell's style to a tcell.Style.
// Covers fg/bg, the common SGR attributes, and underline styles. Mirrors
// internal/tui/terminal/UvCellToTcellStyle without the OSC-8 hyperlink path
// (plugin views are not link-clickable today).
func uvCellToTcellStyle(cell *uv.Cell) tcell.Style {
	if cell == nil {
		return tcell.StyleDefault
	}
	st := tcell.StyleDefault.
		Foreground(uvColorToTcell(cell.Style.Fg)).
		Background(uvColorToTcell(cell.Style.Bg))

	a := cell.Style.Attrs
	if a&uv.AttrBold != 0 {
		st = st.Bold(true)
	}
	if a&uv.AttrFaint != 0 {
		st = st.Dim(true)
	}
	if a&uv.AttrItalic != 0 {
		st = st.Italic(true)
	}
	if a&uv.AttrBlink != 0 {
		st = st.Blink(true)
	}
	if a&uv.AttrReverse != 0 {
		st = st.Reverse(true)
	}
	if a&uv.AttrStrikethrough != 0 {
		st = st.StrikeThrough(true)
	}
	if ul := cell.Style.Underline; ul != 0 {
		var ulStyle tcell.UnderlineStyle
		switch ul {
		case ansi.UnderlineSingle:
			ulStyle = tcell.UnderlineStyleSolid
		case ansi.UnderlineDouble:
			ulStyle = tcell.UnderlineStyleDouble
		case ansi.UnderlineCurly:
			ulStyle = tcell.UnderlineStyleCurly
		case ansi.UnderlineDotted:
			ulStyle = tcell.UnderlineStyleDotted
		case ansi.UnderlineDashed:
			ulStyle = tcell.UnderlineStyleDashed
		default:
			ulStyle = tcell.UnderlineStyleSolid
		}
		if cell.Style.UnderlineColor != nil {
			st = st.Underline(ulStyle, uvColorToTcell(cell.Style.UnderlineColor))
		} else {
			st = st.Underline(ulStyle)
		}
	}
	return st
}

func uvColorToTcell(c color.Color) tcell.Color {
	if c == nil {
		return tcell.ColorDefault
	}
	switch v := c.(type) {
	case ansi.BasicColor:
		return tcell.PaletteColor(int(v))
	case ansi.IndexedColor:
		return tcell.PaletteColor(int(v))
	default:
		return tcell.FromImageColor(c)
	}
}
