# TUI UX polish — candidate directions

Captured during the ui-improvements branch work. Not a plan of record, just a menu of
things worth picking up when we want to make the TUI feel nicer to use.

## 1. Palette & visual cohesion pass

`internal/tui/styles.go` has real drift:

- ANSI 256 codes mixed with one stray hex (`#333333`) in `ModalSelectedStyle`.
- Three different "cyan accents" in use: `51`, `ColorAccent`, `ColorPrimary`.
- Toast and command-popup backgrounds are hard-coded `235` / `238` rather than
  named surface colors.
- Color names are adjectival (`ColorPrimary`, `ColorDim`) rather than semantic
  (`Surface`, `Subtle`, `Accent`, `Warn`, `Ok`, `Error`).

**Scope:**

- Define a small semantic palette (surface/subtle/muted/accent/warn/ok/error, plus
  one or two "on-surface" text colors).
- Route every existing style through the palette — no more literal `245`s scattered
  around.
- Pick a cohesive vibe. Two candidates:
  - **Warm "toaster"** — amber/copper accents, darker warm surfaces. Leans into the
    project name.
  - **Cool/cyberpunk** — the current direction, but deliberate: one accent cyan, one
    magenta for "system" events, strict about not adding more hues.

**Why it's worth doing:** low risk, mechanical, and the whole app feels more
intentional afterwards.

## 2. Persistent contextual hint bar

Single-line footer (lazygit / helix style) that changes based on focus context.

- Default chat: `tab panes · / commands · ? help · ctrl+c quit`
- Jobs pane focused: `↑↓ select · enter details · tab next pane`
- Modal open: `↑↓ nav · enter select · esc close`
- Output modal: `j/k scroll · g/G top/bottom · o close`

**Why it's worth doing:** right now every keybinding is invisible until you
already know it. This is probably the single biggest "intuitive to use" win
per line of code.

**Notes:**

- Should cost one line of vertical space, always visible.
- Style as very dim text so it never competes with content.
- Bindings come from a per-context table, not scattered string literals.

## 3. Smarter header / status strip

The top row is currently just a title. Give it a reason to exist as a compact,
glanceable live strip:

- connection dot (green/red)
- operator provider + model
- active job count · active worker count
- tokens-in-flight / speed
- server address (dim, right-aligned)

**Why it's worth doing:** status is currently scattered across the sidebar; a
single strip makes the whole thing feel like a cockpit.

## 4. Unified modal navigation & chrome

Different modals have slightly different key semantics today:

- grid uses `hjkl`
- output modal uses `g/G/ctrl-u/d`
- jobs modal has its own scheme

**Scope:**

- Shared modal shell (title bar, body, footer hint line) that every modal renders
  through.
- Consistent bindings across all modals: `↑↓/jk` navigate, `enter` select,
  `esc/q` close, `?` help, `/` filter where applicable.
- Shared footer hint line per modal — reuses the hint bar from #2.

**Why it's worth doing:** quiet win, but it removes a whole class of "wait, how do
I close this one again?" friction.

---

## Rough ordering suggestion

1. **#2 hint bar** first — biggest felt improvement, cheapest implementation, and
   #4 will reuse its rendering.
2. **#1 palette** next — fun, paints the whole app, low risk.
3. **#4 modal chrome** — builds on #2's hint rendering.
4. **#3 status strip** — nice-to-have, do when in the mood.
