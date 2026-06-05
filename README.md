# argus-sdk

Primitives shared by argus and its plugins (hera, iris). Currently a
downstream snapshot of argus internals; intended to be the source of
truth if drn/argus ever adopts back.

## Packages

- **`pluginview/`** — `tcell.Screen` over WebSocket. Implements the wire
  contract argus's plugin-pane terminalpane consumes: JSON resize/focus
  envelopes on text frames, accumulated ANSI surface bytes on binary
  frames. Hand the resulting screen to tview and you're rendering.
- **`terminalpane/`** — VT-emulator-backed `tview.Box`-equivalent widget.
  Mirrors argus's center column: cursor positioning, SGR colors,
  alt-screen, UTF-8 graphemes. Use one per stream you want to render.
- **`widget/`** — drawing primitives: `DrawText`, `FillArea`,
  `DrawBorder`, `DrawBorderedPanel`, the `InnerRect` type, the `AnsiRe`
  regex.
- **`keyenc/`** — `Encode` maps a `tcell` key event to the raw bytes a
  terminal application expects on its input stream (xterm conventions:
  arrows, modified arrows, Ctrl/Alt/Shift combos, C0 controls). The single
  source of truth `terminalpane` uses to forward keystrokes to a plugin.
- **`theme/`** — color, icon, and style tokens. Match these in custom
  widgets so plugin UIs feel like argus.

## Provenance

- `theme/`, `widget/`, `terminalpane/`, `keyenc/` were copied from
  `drn/argus internal/tui/` (theme, widget, terminalpane, keyenc packages).
- `pluginview/` was copied from `anutron/hera internal/view/screen/`.

Both upstreams retain their original copies for now. When argus is ready
to consume this SDK, the upstream copies get deleted and argus imports
from here.

## Versioning

Pre-1.0 (`v0.0.x`). Breaking changes are allowed until the first plugin
ships against a tagged release. After that, semver applies.
