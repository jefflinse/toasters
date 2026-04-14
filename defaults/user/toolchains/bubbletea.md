---
id: bubbletea
name: Bubble Tea
description: The Bubble Tea TUI framework and Charm ecosystem.
vars:
  version:
    description: The Bubble Tea major version.
    default: "2"
---

Bubble Tea {{ vars.version }} is a Go TUI framework based on The Elm Architecture.
Key packages in the Charm ecosystem:
- `bubbletea` — the core framework (Model, Update, View, Cmd, Msg).
- `bubbles` — reusable TUI components (textinput, list, table, viewport, spinner, etc.).
- `lipgloss` — declarative terminal styling and layout.
- `huh` — form/survey component library.
- `log` — structured logging that plays nicely with the TUI.

Architecture patterns:
- Every component implements the `tea.Model` interface (Init, Update, View).
- State flows one direction: Msg -> Update -> Model -> View.
- Side effects are expressed as `tea.Cmd` values returned from Update, never executed inline.
- Use `tea.WindowSizeMsg` for responsive layouts.
- Compose models by embedding child models and forwarding messages.
- Use `key.Matches` with `key.Binding` for key handling.
