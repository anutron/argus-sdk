package terminalpane

import (
	"fmt"
	"image/color"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/anutron/argus-sdk/internal/testutil"
)

// --- helpers ---

func newSimScreen(t *testing.T, w, h int) tcell.SimulationScreen {
	t.Helper()
	s := tcell.NewSimulationScreen("")
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	s.SetSize(w, h)
	return s
}

func readCell(s tcell.SimulationScreen, col, row int) (rune, tcell.Style) {
	cells, w, _ := s.GetContents()
	idx := row*w + col
	if idx < 0 || idx >= len(cells) {
		return ' ', tcell.StyleDefault
	}
	c := cells[idx]
	if len(c.Runes) == 0 {
		return ' ', c.Style
	}
	return c.Runes[0], c.Style
}

func readRow(s tcell.SimulationScreen, row, w int) string {
	cells, cw, _ := s.GetContents()
	if row < 0 || row*cw >= len(cells) {
		return ""
	}
	var b strings.Builder
	for col := 0; col < w; col++ {
		idx := row*cw + col
		if idx >= len(cells) {
			break
		}
		for _, r := range cells[idx].Runes {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func waitForTouched(t *testing.T, tp *TerminalPane, want uint64) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if tp.Touched() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Touched did not reach %d (got %d)", want, tp.Touched())
}

// drawInRect resizes the pane to the given outer rect and runs Draw.
func drawInRect(t *testing.T, tp *TerminalPane, sim tcell.SimulationScreen, x, y, w, h int) {
	t.Helper()
	tp.SetRect(x, y, w, h)
	tp.Draw(sim)
	sim.Show()
}

// feedLines writes n numbered lines ("line1".."lineN", CRLF-separated) as a
// single chunk and waits for the consumer to ingest it. On a screen shorter
// than n rows the excess scrolls into the emulator's scrollback buffer.
func feedLines(t *testing.T, tp *TerminalPane, src chan []byte, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteString("\r\n")
		}
		fmt.Fprintf(&b, "line%d", i)
	}
	before := tp.Touched()
	src <- []byte(b.String())
	waitForTouched(t, tp, before+1)
}

// --- tests ---

func TestNew_ReturnsBoxSubclass(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	testutil.NotNil(t, tp.Box)
}

func TestTerminalPane_CursorPositioningHonoured(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(40, 10)

	// Move to row 5, col 10 (1-based in CSI), then write 'X'.
	src <- []byte("\x1b[5;10HX")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 50, 14)
	// Outer rect 0,0,42,12 — inner becomes 1,1,40,10 after border.
	drawInRect(t, tp, sim, 0, 0, 42, 12)

	// Inner origin is (1,1); CSI (5;10) targets row 4, col 9 (0-based) inside emu.
	// Screen position: x=1+9=10, y=1+4=5.
	r, _ := readCell(sim, 10, 5)
	testutil.Equal(t, r, 'X')
}

func TestTerminalPane_UTF8GlyphIsSingleCell(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(40, 6)

	// Box-drawing characters — each is one display column but multiple UTF-8 bytes.
	src <- []byte("\x1b[1;1H─→│┌┘")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 50, 10)
	drawInRect(t, tp, sim, 0, 0, 42, 8)

	row := readRow(sim, 1, 50)
	if !strings.Contains(row, "─→│┌┘") {
		t.Fatalf("expected exact box-drawing runes in row, got %q", row)
	}

	// Each glyph occupies exactly one cell.
	cases := []struct {
		col  int
		want rune
	}{{1, '─'}, {2, '→'}, {3, '│'}, {4, '┌'}, {5, '┘'}}
	for _, tc := range cases {
		r, _ := readCell(sim, tc.col, 1)
		if r != tc.want {
			t.Errorf("col %d: got %q want %q", tc.col, r, tc.want)
		}
	}
}

func TestTerminalPane_SGRColorsLand(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	// Red foreground 'R'.
	src <- []byte("\x1b[1;1H\x1b[31mR\x1b[0m")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 22, 6)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	r, style := readCell(sim, 1, 1)
	testutil.Equal(t, r, 'R')
	fg, _, _ := style.Decompose()
	if fg != tcell.PaletteColor(1) {
		t.Errorf("expected red foreground (palette 1), got %v", fg)
	}
}

func TestTerminalPane_SecondFullFrameReplacesPriorSurface(t *testing.T) {
	src := make(chan []byte, 2)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	// First frame: "AA" at top-left.
	src <- []byte("\x1b[2J\x1b[1;1HAA")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 22, 6)
	drawInRect(t, tp, sim, 0, 0, 22, 6)
	r, _ := readCell(sim, 1, 1)
	testutil.Equal(t, r, 'A')

	// Second frame: clear screen, write "BB" at (3,3).
	src <- []byte("\x1b[2J\x1b[3;3HBB")
	waitForTouched(t, tp, 2)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	// AA must be gone (cells back to blank).
	r0, _ := readCell(sim, 1, 1)
	if r0 != ' ' {
		t.Errorf("expected (1,1) blank after clear-and-redraw, got %q", r0)
	}
	// BB lands at inner (3,3) → screen (1+2,1+2) = (3,3).
	rB, _ := readCell(sim, 3, 3)
	testutil.Equal(t, rB, 'B')
}

func TestTerminalPane_ResizeAdoptsNewDimensions(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()

	tp.Resize(80, 24)
	cols, rows := tp.PTYSize()
	testutil.Equal(t, cols, 80)
	testutil.Equal(t, rows, 24)

	tp.Resize(40, 10)
	cols, rows = tp.PTYSize()
	testutil.Equal(t, cols, 40)
	testutil.Equal(t, rows, 10)

	// Write within the new bounds and confirm it renders.
	src <- []byte("\x1b[1;1HZ")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 42, 12)
	drawInRect(t, tp, sim, 0, 0, 42, 12)
	r, _ := readCell(sim, 1, 1)
	testutil.Equal(t, r, 'Z')
}

func TestTerminalPane_DrawAutoResizesEmulatorOnInnerRectChange(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()

	// Outer rect 22x6 → inner 20x4.
	sim := newSimScreen(t, 30, 10)
	tp.SetRect(0, 0, 22, 6)
	tp.Draw(sim)
	cols, rows := tp.PTYSize()
	testutil.Equal(t, cols, 20)
	testutil.Equal(t, rows, 4)

	// Resize the outer rect → emulator picks up the new inner dims on next Draw.
	tp.SetRect(0, 0, 32, 8)
	tp.Draw(sim)
	cols, rows = tp.PTYSize()
	testutil.Equal(t, cols, 30)
	testutil.Equal(t, rows, 6)
}

func TestTerminalPane_TouchedIncrementsOnNewBytes(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	before := tp.Touched()
	src <- []byte("hello")
	waitForTouched(t, tp, before+1)
}

func TestTerminalPane_TouchedDoesNotIncrementOnEmptyChunks(t *testing.T) {
	src := make(chan []byte, 2)
	tp := New(src)
	defer tp.Close()

	src <- []byte("hi")
	waitForTouched(t, tp, 1)
	got := tp.Touched()
	src <- []byte("")
	time.Sleep(20 * time.Millisecond)
	testutil.Equal(t, tp.Touched(), got)
}

func TestTerminalPane_DrawRendersTitleInBorder(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.SetTitle("PluginX")

	sim := newSimScreen(t, 20, 4)
	drawInRect(t, tp, sim, 0, 0, 20, 4)

	top := readRow(sim, 0, 20)
	testutil.Contains(t, top, "PluginX")
}

func TestTerminalPane_OnRedrawFiresAfterBytes(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()

	var (
		mu    sync.Mutex
		count int
	)
	tp.OnNeedRedraw = func() {
		mu.Lock()
		defer mu.Unlock()
		count++
	}

	src <- []byte("x")
	waitForTouched(t, tp, 1)

	mu.Lock()
	defer mu.Unlock()
	if count < 1 {
		t.Fatalf("expected OnNeedRedraw to fire at least once, got %d", count)
	}
}

func TestTerminalPane_InputBackReceivesRune(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	back := make(chan []byte, 4)
	tp.SetInputBack(back)

	handler := tp.InputHandler()
	if handler == nil {
		t.Fatal("expected non-nil InputHandler when InputBack is set")
	}
	handler(tcell.NewEventKey(tcell.KeyRune, 'a', tcell.ModNone), func(_ tview.Primitive) {})

	select {
	case got := <-back:
		testutil.Equal(t, string(got), "a")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("InputBack did not receive keystroke")
	}
}

func TestTerminalPane_InputBackForwardsEnter(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	back := make(chan []byte, 4)
	tp.SetInputBack(back)

	handler := tp.InputHandler()
	handler(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone), func(_ tview.Primitive) {})

	select {
	case got := <-back:
		testutil.Equal(t, string(got), "\r")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("InputBack did not receive enter")
	}
}

func TestTerminalPane_InputHandlerNilWhenNoInputBack(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	if tp.InputHandler() != nil {
		t.Fatal("expected nil InputHandler when no InputBack set")
	}
}

func TestTerminalPane_PasteHandlerForwardsToInputBack(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	back := make(chan []byte, 4)
	tp.SetInputBack(back)

	ph := tp.PasteHandler()
	if ph == nil {
		t.Fatal("expected non-nil PasteHandler")
	}
	ph("pasted", func(_ tview.Primitive) {})

	select {
	case got := <-back:
		testutil.Equal(t, string(got), "pasted")
	case <-time.After(200 * time.Millisecond):
		t.Fatal("paste did not reach InputBack")
	}
}

func TestTerminalPane_CloseIsIdempotent(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	tp.Close()
	tp.Close() // must not panic
}

func TestTerminalPane_CloseStopsConsumer(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	tp.Close()

	select {
	case <-tp.done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("consumer goroutine did not exit after Close")
	}
}

func TestTerminalPane_SourceClosedStopsConsumer(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()

	close(src)
	select {
	case <-tp.done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("consumer did not exit after source close")
	}
}

func TestTerminalPane_DrawHandlesZeroRect(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	sim := newSimScreen(t, 10, 4)
	tp.SetRect(0, 0, 0, 0)
	tp.Draw(sim)
}

func TestTerminalPane_DrawHandlesTinyRect(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	sim := newSimScreen(t, 10, 4)
	tp.SetRect(0, 0, 1, 1)
	tp.Draw(sim)
}

func TestTerminalPane_DrawWhenFocused(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.SetRect(0, 0, 10, 4)
	tp.Focus(nil)
	sim := newSimScreen(t, 10, 4)
	tp.Draw(sim)
}

func TestTerminalPane_CursorShownAtCorrectPositionWhenFocused(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(40, 10)

	// CSI [3;5H moves cursor to row 3, col 5 (1-indexed) = (4,2) 0-indexed.
	src <- []byte("\x1b[3;5H")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 50, 14)
	tp.Focus(nil)
	// Outer rect 0,0,42,12 → inner origin (1,1).
	drawInRect(t, tp, sim, 0, 0, 42, 12)

	// Screen cursor: x=1+4=5, y=1+2=3.
	cx, cy, visible := sim.GetCursor()
	if !visible {
		t.Fatal("expected cursor visible when pane is focused")
	}
	if cx != 5 || cy != 3 {
		t.Errorf("cursor at (%d,%d), want (5,3)", cx, cy)
	}
}

func TestTerminalPane_CursorHiddenWhenNotFocused(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(40, 10)

	src <- []byte("\x1b[3;5H")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 50, 14)
	// Pane not focused — Draw must call HideCursor.
	drawInRect(t, tp, sim, 0, 0, 42, 12)

	_, _, visible := sim.GetCursor()
	if visible {
		t.Fatal("expected cursor hidden when pane is not focused")
	}
}

func TestTerminalPane_CursorHiddenByDECTCEM(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(40, 10)

	// ESC[?25l disables DECTCEM (hides cursor).
	src <- []byte("\x1b[3;5H\x1b[?25l")
	waitForTouched(t, tp, 1)

	sim := newSimScreen(t, 50, 14)
	tp.Focus(nil)
	drawInRect(t, tp, sim, 0, 0, 42, 12)

	_, _, visible := sim.GetCursor()
	if visible {
		t.Fatal("expected cursor hidden when DECTCEM is off, even when focused")
	}
}

func TestTerminalPane_CursorHiddenWhenScrolled(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)
	tp.ScrollBy(3)

	sim := newSimScreen(t, 24, 8)
	tp.Focus(nil)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	_, _, visible := sim.GetCursor()
	if visible {
		t.Fatal("expected cursor hidden while scrolled into history")
	}
}

func TestTerminalPane_SendDropsWhenBackFull(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	back := make(chan []byte, 1)
	tp.SetInputBack(back)
	back <- []byte("blocker")
	// Must not block.
	handler := tp.InputHandler()
	handler(tcell.NewEventKey(tcell.KeyRune, 'x', tcell.ModNone), func(_ tview.Primitive) {})
}

func TestTerminalPane_SendNoOpWithoutInputBack(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.send([]byte("ignored"))
}

func TestTerminalPane_SendEmptyBytes(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	back := make(chan []byte, 1)
	tp.SetInputBack(back)
	tp.send(nil)
	select {
	case <-back:
		t.Fatal("expected no send for empty bytes")
	case <-time.After(20 * time.Millisecond):
	}
}

func TestTerminalPane_ScrollByMovesIntoHistory(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	// 10 lines on a 4-row screen → 6 lines in scrollback.
	feedLines(t, tp, src, 10)
	testutil.Equal(t, tp.emu.ScrollbackLen(), 6)

	tp.ScrollBy(3)
	testutil.Equal(t, tp.ScrollOffset(), 3)
	tp.ScrollBy(2)
	testutil.Equal(t, tp.ScrollOffset(), 5)
}

func TestTerminalPane_ScrollByClampsToScrollbackLen(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)

	tp.ScrollBy(1000)
	testutil.Equal(t, tp.ScrollOffset(), tp.emu.ScrollbackLen())
}

func TestTerminalPane_ScrollByClampsAtZero(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)

	tp.ScrollBy(3)
	tp.ScrollBy(-1000)
	testutil.Equal(t, tp.ScrollOffset(), 0)
}

func TestTerminalPane_ScrollByNoOpWithoutScrollback(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	// Nothing fed — no history to scroll into.
	tp.ScrollBy(5)
	testutil.Equal(t, tp.ScrollOffset(), 0)
}

func TestTerminalPane_ResetScrollReturnsToLive(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)

	tp.ScrollBy(4)
	testutil.Equal(t, tp.ScrollOffset(), 4)
	tp.ResetScroll()
	testutil.Equal(t, tp.ScrollOffset(), 0)
}

func TestTerminalPane_ScrollByNoOpWhenEmulatorMissing(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.mu.Lock()
	tp.emu = nil
	tp.mu.Unlock()
	tp.ScrollBy(5) // must not panic
	testutil.Equal(t, tp.ScrollOffset(), 0)
}

func TestTerminalPane_PaintScrolledShowsHistoryWindow(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	// 10 lines on a 4-row screen: scrollback holds line1..line6, the live
	// screen shows line7..line10.
	feedLines(t, tp, src, 10)

	tp.ScrollBy(3)
	sim := newSimScreen(t, 24, 8)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	// Combined lines [3, 7) → line4..line7. The top content row is partly
	// covered by the [SCROLL] badge, so assert the unobscured rows.
	testutil.Contains(t, readRow(sim, 2, 24), "line5")
	testutil.Contains(t, readRow(sim, 3, 24), "line6")
	testutil.Contains(t, readRow(sim, 4, 24), "line7")
}

func TestTerminalPane_PaintScrolledToOldestLine(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)

	tp.ScrollBy(1000) // clamps to ScrollbackLen (6)
	sim := newSimScreen(t, 24, 8)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	// Combined lines [0, 4) → line1..line4 (line1 sits under the badge).
	testutil.Contains(t, readRow(sim, 2, 24), "line2")
	testutil.Contains(t, readRow(sim, 3, 24), "line3")
	testutil.Contains(t, readRow(sim, 4, 24), "line4")
}

func TestTerminalPane_PaintScrolledShowsBadge(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)

	tp.ScrollBy(2)
	sim := newSimScreen(t, 24, 8)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	// Badge renders on the top content row (inner row 1 after the border).
	testutil.Contains(t, readRow(sim, 1, 24), "[SCROLL]")
}

func TestTerminalPane_PaintAtZeroOffsetMatchesLiveView(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)

	// Scroll up, then back to live — the badge must vanish and the live
	// screen rows (line7..line10) must paint exactly as before scrolling.
	tp.ScrollBy(3)
	tp.ResetScroll()
	sim := newSimScreen(t, 24, 8)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	if strings.Contains(readRow(sim, 1, 24), "[SCROLL]") {
		t.Fatal("badge must not render at offset 0")
	}
	testutil.Contains(t, readRow(sim, 1, 24), "line7")
	testutil.Contains(t, readRow(sim, 2, 24), "line8")
	testutil.Contains(t, readRow(sim, 3, 24), "line9")
	testutil.Contains(t, readRow(sim, 4, 24), "line10")
}

func TestTerminalPane_AnchorLockGrowsOffsetWithNewOutput(t *testing.T) {
	src := make(chan []byte, 2)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	// 10 lines → scrollback line1..line6, live line7..line10.
	feedLines(t, tp, src, 10)
	tp.ScrollBy(3)
	testutil.Equal(t, tp.ScrollOffset(), 3)

	// Two more lines push line11/line12 into the live screen and line7/
	// line8 into scrollback (sbLen 6 → 8). The effective offset must grow
	// by the same delta so the viewed window stays put.
	before := tp.Touched()
	src <- []byte("\r\nline11\r\nline12")
	waitForTouched(t, tp, before+1)
	testutil.Equal(t, tp.ScrollOffset(), 5)
}

func TestTerminalPane_AnchorLockKeepsViewedContentStable(t *testing.T) {
	src := make(chan []byte, 2)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)
	tp.ScrollBy(3)

	sim := newSimScreen(t, 24, 8)
	drawInRect(t, tp, sim, 0, 0, 22, 6)
	wantRows := []string{readRow(sim, 2, 24), readRow(sim, 3, 24), readRow(sim, 4, 24)}

	// New output arrives while scrolled — repaint must show the same rows.
	before := tp.Touched()
	src <- []byte("\r\nline11\r\nline12")
	waitForTouched(t, tp, before+1)
	drawInRect(t, tp, sim, 0, 0, 22, 6)

	gotRows := []string{readRow(sim, 2, 24), readRow(sim, 3, 24), readRow(sim, 4, 24)}
	for i := range wantRows {
		testutil.Equal(t, gotRows[i], wantRows[i])
	}
}

func TestTerminalPane_AnchorLockReclampsWhenBufferShrinks(t *testing.T) {
	src := make(chan []byte, 1)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)
	tp.ScrollBy(4)
	testutil.Equal(t, tp.ScrollOffset(), 4)

	// Simulate the buffer trimming below the anchored offset — the
	// effective offset must re-clamp to the available history.
	tp.emu.ClearScrollback()
	testutil.Equal(t, tp.ScrollOffset(), 0)
}

func TestTerminalPane_AnchorLockHonorsScrollbackCapacity(t *testing.T) {
	src := make(chan []byte, 2)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)
	// Cap the buffer so it trims while scrolled.
	tp.emu.SetScrollbackSize(4)

	feedLines(t, tp, src, 10) // sbLen capped at 4
	tp.ScrollBy(1000)
	testutil.Equal(t, tp.ScrollOffset(), 4)

	// More output: the buffer trims oldest lines; the offset stays clamped
	// within [0, ScrollbackLen] instead of running past the buffer.
	before := tp.Touched()
	src <- []byte("\r\nline11\r\nline12")
	waitForTouched(t, tp, before+1)
	testutil.Equal(t, tp.ScrollOffset(), 4)
}

func TestTerminalPane_ScrollPastZeroClearsAnchor(t *testing.T) {
	src := make(chan []byte, 3)
	tp := New(src)
	defer tp.Close()
	tp.Resize(20, 4)

	feedLines(t, tp, src, 10)
	tp.ScrollBy(3)
	tp.ScrollBy(-1000)
	testutil.Equal(t, tp.ScrollOffset(), 0)

	// Anchor is cleared: new output must NOT drag the offset back up.
	before := tp.Touched()
	src <- []byte("\r\nline11\r\nline12")
	waitForTouched(t, tp, before+1)
	testutil.Equal(t, tp.ScrollOffset(), 0)
}

func TestUvCellToTcellStyle_NilCellReturnsDefault(t *testing.T) {
	st := uvCellToTcellStyle(nil)
	testutil.Equal(t, st, tcell.StyleDefault)
}

func TestUvCellToTcellStyle_AllAttributes(t *testing.T) {
	cell := &uv.Cell{}
	cell.Style.Attrs = uv.AttrBold | uv.AttrFaint | uv.AttrItalic |
		uv.AttrBlink | uv.AttrReverse | uv.AttrStrikethrough
	st := uvCellToTcellStyle(cell)
	_, _, attrs := st.Decompose()
	if attrs&tcell.AttrBold == 0 {
		t.Error("bold not set")
	}
	if attrs&tcell.AttrDim == 0 {
		t.Error("dim not set")
	}
	if attrs&tcell.AttrItalic == 0 {
		t.Error("italic not set")
	}
	if attrs&tcell.AttrBlink == 0 {
		t.Error("blink not set")
	}
	if attrs&tcell.AttrReverse == 0 {
		t.Error("reverse not set")
	}
	if attrs&tcell.AttrStrikeThrough == 0 {
		t.Error("strikethrough not set")
	}
}

func TestUvCellToTcellStyle_UnderlineStyles(t *testing.T) {
	cases := []struct {
		name string
		ul   ansi.UnderlineStyle
	}{
		{"single", ansi.UnderlineSingle},
		{"double", ansi.UnderlineDouble},
		{"curly", ansi.UnderlineCurly},
		{"dotted", ansi.UnderlineDotted},
		{"dashed", ansi.UnderlineDashed},
		{"unknown", ansi.UnderlineStyle(99)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cell := &uv.Cell{}
			cell.Style.Underline = tc.ul
			st := uvCellToTcellStyle(cell)
			_, _, attrs := st.Decompose()
			if attrs&tcell.AttrUnderline == 0 {
				t.Error("underline not set")
			}
		})
	}
}

func TestUvCellToTcellStyle_UnderlineColor(t *testing.T) {
	cell := &uv.Cell{}
	cell.Style.Underline = ansi.UnderlineSingle
	cell.Style.UnderlineColor = ansi.BasicColor(2)
	st := uvCellToTcellStyle(cell)
	_, _, attrs := st.Decompose()
	if attrs&tcell.AttrUnderline == 0 {
		t.Error("underline not set when color provided")
	}
}

func TestUvColorToTcell_Cases(t *testing.T) {
	cases := []struct {
		name string
		in   color.Color
		want tcell.Color
	}{
		{"nil", nil, tcell.ColorDefault},
		{"basic", ansi.BasicColor(4), tcell.PaletteColor(4)},
		{"indexed", ansi.IndexedColor(200), tcell.PaletteColor(200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := uvColorToTcell(tc.in)
			testutil.Equal(t, got, tc.want)
		})
	}
}

func TestUvColorToTcell_TrueColor(t *testing.T) {
	// Anything that isn't ansi.BasicColor or ansi.IndexedColor falls through
	// to tcell.FromImageColor — just confirm we don't crash and we get a
	// non-default color back.
	got := uvColorToTcell(color.RGBA{R: 255, G: 0, B: 0, A: 255})
	if got == tcell.ColorDefault {
		t.Error("expected non-default tcell color for RGBA input")
	}
}

func TestTerminalPane_ResizeBelowMinClamps(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.Resize(0, 0)
	cols, rows := tp.PTYSize()
	if cols < minCols || rows < minRows {
		t.Fatalf("Resize did not clamp to minimums: %dx%d", cols, rows)
	}
}

func TestTerminalPane_ResizeNoOpWhenUnchanged(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.Resize(50, 12)
	tp.Resize(50, 12) // second call must hit the unchanged short-circuit
	cols, rows := tp.PTYSize()
	testutil.Equal(t, cols, 50)
	testutil.Equal(t, rows, 12)
}

func TestTerminalPane_FeedNoOpWhenEmulatorMissing(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.mu.Lock()
	tp.emu = nil
	tp.mu.Unlock()
	tp.feed([]byte("ignored")) // must not panic
}

func TestTerminalPane_PaintNoOpWhenEmulatorMissing(t *testing.T) {
	src := make(chan []byte)
	tp := New(src)
	defer tp.Close()
	tp.mu.Lock()
	tp.emu = nil
	tp.mu.Unlock()
	sim := newSimScreen(t, 10, 4)
	tp.paint(sim, 0, 0, 10, 4) // must not panic
}

func TestEventBytes_AllCases(t *testing.T) {
	cases := []struct {
		key  tcell.Key
		r    rune
		want string
	}{
		{tcell.KeyRune, 'z', "z"},
		{tcell.KeyEnter, 0, "\r"},
		{tcell.KeyTab, 0, "\t"},
		{tcell.KeyBackspace, 0, "\x7f"},
		{tcell.KeyBackspace2, 0, "\x7f"},
		{tcell.KeyEscape, 0, "\x1b"},
		{tcell.KeyF1, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tcell.KeyNames[tc.key], func(t *testing.T) {
			ev := tcell.NewEventKey(tc.key, tc.r, tcell.ModNone)
			testutil.Equal(t, string(eventBytes(ev)), tc.want)
		})
	}
}
