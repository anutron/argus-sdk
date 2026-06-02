package terminalpane

import (
	"testing"

	"github.com/anutron/argus-sdk/internal/testutil"
)

func TestDecodeSGRMouse(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want MouseEvent
		ok   bool
	}{
		// Well-formed sequences.
		{"wheel up", "\x1b[<64;10;5M", MouseEvent{Kind: WheelUp, X: 10, Y: 5}, true},
		{"wheel down", "\x1b[<65;1;1M", MouseEvent{Kind: WheelDown, X: 1, Y: 1}, true},
		{"left press is Other", "\x1b[<0;3;4M", MouseEvent{Kind: Other, X: 3, Y: 4}, true},
		{"left release is Other", "\x1b[<0;3;4m", MouseEvent{Kind: Other, X: 3, Y: 4}, true},
		{"wheel up release final", "\x1b[<64;7;2m", MouseEvent{Kind: WheelUp, X: 7, Y: 2}, true},
		{"drag is Other", "\x1b[<32;15;8M", MouseEvent{Kind: Other, X: 15, Y: 8}, true},
		{"shift-wheel is Other", "\x1b[<68;1;1M", MouseEvent{Kind: Other, X: 1, Y: 1}, true},
		{"large coordinates", "\x1b[<65;500;300M", MouseEvent{Kind: WheelDown, X: 500, Y: 300}, true},

		// Truncated sequences.
		{"empty", "", MouseEvent{}, false},
		{"bare escape", "\x1b", MouseEvent{}, false},
		{"csi only", "\x1b[", MouseEvent{}, false},
		{"prefix only", "\x1b[<", MouseEvent{}, false},
		{"missing final byte", "\x1b[<64;10;5", MouseEvent{}, false},
		{"missing last param", "\x1b[<64;10M", MouseEvent{}, false},

		// Trailing / leading bytes — slice must be exactly one sequence.
		{"trailing byte", "\x1b[<64;10;5Mx", MouseEvent{}, false},
		{"trailing second sequence", "\x1b[<64;1;1M\x1b[<64;1;1M", MouseEvent{}, false},
		{"leading byte", "x\x1b[<64;10;5M", MouseEvent{}, false},

		// Non-mouse CSI and malformed params.
		{"cursor position CSI", "\x1b[5;10H", MouseEvent{}, false},
		{"non-SGR mouse (X10)", "\x1b[M abc", MouseEvent{}, false},
		{"empty param", "\x1b[<64;;5M", MouseEvent{}, false},
		{"non-digit param", "\x1b[<64;1a;5M", MouseEvent{}, false},
		{"signed param", "\x1b[<64;-1;5M", MouseEvent{}, false},
		{"extra param", "\x1b[<64;1;2;3M", MouseEvent{}, false},
		{"absurdly long param", "\x1b[<64;99999999999;5M", MouseEvent{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DecodeSGRMouse([]byte(tc.in))
			testutil.Equal(t, ok, tc.ok)
			if ok {
				testutil.Equal(t, got, tc.want)
			}
		})
	}
}
