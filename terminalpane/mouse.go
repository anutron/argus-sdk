package terminalpane

import "bytes"

// MouseEventKind classifies a decoded SGR mouse event. Wheel ticks are the
// only kinds callers act on today (scrollback); everything else — clicks,
// drags, motion — decodes as Other so callers can swallow the sequence
// without forwarding it to the PTY.
type MouseEventKind int

const (
	// Other is any well-formed SGR mouse sequence that isn't a wheel tick.
	Other MouseEventKind = iota
	// WheelUp is SGR button code 64.
	WheelUp
	// WheelDown is SGR button code 65.
	WheelDown
)

// MouseEvent is a decoded SGR mouse sequence.
type MouseEvent struct {
	Kind MouseEventKind
	X    int // column, 1-based as encoded
	Y    int // row, 1-based as encoded
}

// sgrMousePrefix opens every SGR mouse sequence: ESC [ <.
var sgrMousePrefix = []byte("\x1b[<")

// DecodeSGRMouse parses b as exactly one SGR mouse sequence:
//
//	ESC [ < Cb ; Cx ; Cy (M|m)
//
// Returns ok=false unless the entire byte slice is one well-formed sequence
// — truncated input, trailing bytes, and non-mouse CSI all fail. Cb 64 maps
// to WheelUp, 65 to WheelDown, anything else (press, release, drag) to Other.
func DecodeSGRMouse(b []byte) (MouseEvent, bool) {
	if !bytes.HasPrefix(b, sgrMousePrefix) {
		return MouseEvent{}, false
	}
	final := b[len(b)-1]
	if final != 'M' && final != 'm' {
		return MouseEvent{}, false
	}
	params := bytes.Split(b[len(sgrMousePrefix):len(b)-1], []byte{';'})
	if len(params) != 3 {
		return MouseEvent{}, false
	}
	cb, ok := parseMouseParam(params[0])
	if !ok {
		return MouseEvent{}, false
	}
	cx, ok := parseMouseParam(params[1])
	if !ok {
		return MouseEvent{}, false
	}
	cy, ok := parseMouseParam(params[2])
	if !ok {
		return MouseEvent{}, false
	}

	kind := Other
	switch cb {
	case 64:
		kind = WheelUp
	case 65:
		kind = WheelDown
	}
	return MouseEvent{Kind: kind, X: cx, Y: cy}, true
}

// parseMouseParam parses a non-empty all-digits SGR parameter. Stricter than
// strconv.Atoi — signs, spaces, and empty params are malformed in the SGR
// encoding and must fail the whole sequence.
func parseMouseParam(p []byte) (int, bool) {
	if len(p) == 0 {
		return 0, false
	}
	n := 0
	for _, c := range p {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<20 { // far beyond any real terminal coordinate
			return 0, false
		}
	}
	return n, true
}
