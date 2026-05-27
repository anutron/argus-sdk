package widget

import (
	"regexp"

	"github.com/gdamore/tcell/v2"
)

// AnsiRe matches ANSI escape sequences (CSI, OSC, simple escapes).
// CSI sequences: \x1b[ <params 0x20-0x3f>* <final 0x40-0x7e>
// OSC sequences are terminated by either BEL (\x07) or ST (\x1b\\).
var AnsiRe = regexp.MustCompile(`\x1b(?:\[[\x20-\x3f]*[\x40-\x7e]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][0-9A-B]|[78DEHM])`)

// DrawBorder draws a Unicode box border.
func DrawBorder(screen tcell.Screen, x, y, w, h int, style tcell.Style) {
	if w < 2 || h < 2 {
		return
	}
	screen.SetContent(x, y, '╭', nil, style)
	screen.SetContent(x+w-1, y, '╮', nil, style)
	screen.SetContent(x, y+h-1, '╰', nil, style)
	screen.SetContent(x+w-1, y+h-1, '╯', nil, style)
	for col := x + 1; col < x+w-1; col++ {
		screen.SetContent(col, y, '─', nil, style)
		screen.SetContent(col, y+h-1, '─', nil, style)
	}
	for row := y + 1; row < y+h-1; row++ {
		screen.SetContent(x, row, '│', nil, style)
		screen.SetContent(x+w-1, row, '│', nil, style)
	}
}

// InnerRect holds the content area inside a bordered panel.
type InnerRect struct {
	X, Y, W, H int
}

// DrawBorderedPanel draws a rounded border at (x, y, w, h) with an optional
// title embedded in the top border, and returns the inner content rect.
//
// The interior is blanked with (' ', tcell.StyleDefault) before the border
// is drawn. tview's screen.Clear() already does this screen-wide each
// frame; the fill is defense-in-depth against future optimizations that
// might bypass Clear for partial redraws. The fill style is hardcoded to
// tcell.StyleDefault because every current caller wants a transparent
// interior; if a future bordered panel ever lives on top of a tinted
// layer, this helper will need a fillStyle parameter.
//
// When w or h is below the 2x2 minimum required for a border, the returned
// InnerRect is the zero value so callers can short-circuit on
// `inner.W <= 0 || inner.H <= 0`.
func DrawBorderedPanel(screen tcell.Screen, x, y, w, h int, title string, style tcell.Style) InnerRect {
	if w < 2 || h < 2 {
		return InnerRect{}
	}
	FillArea(screen, x+1, y+1, w-2, h-2, ' ', tcell.StyleDefault)
	DrawBorder(screen, x, y, w, h, style)
	if title != "" {
		for i, r := range title {
			if x+1+i < x+w-1 {
				screen.SetContent(x+1+i, y, r, nil, style.Bold(true))
			}
		}
	}
	return InnerRect{X: x + 1, Y: y + 1, W: w - 2, H: h - 2}
}
