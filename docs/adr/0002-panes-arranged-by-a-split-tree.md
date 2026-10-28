# 2. The TUI's panes are arranged by a split tree, switched in the app

Date: 2026-10-28
Status: accepted

## Context

The TUI drew two screens: the Explorer with its detail panes (schema and preview), or the Editor above the Results, chosen by the focused zone. The Explorer and the Editor were never on screen together, so a user writing a query could not see the table list or its schema beside it. Pane placement was fixed: `P` moved only the detail pane (right, bottom, hidden), and the sizes were worked out twice, once for drawing and again in the key handlers.

The user wants to arrange the Explorer, Schema, Editor and Results panes side by side or stacked, in any combination. Research (`docs/research/flexible-layout.md`) found that the libraries provide only joins and a compositor, not a pane manager. The SQL tools checked (harlequin, lazysql, gobang, DBeaver, DataGrip) have fixed layouts. tmux, zellij, Vim and Helix arrange panes with split trees, and lazygit uses a weighted box tree.

## Decision

- **One model:** the four panes are the leaves of one n-ary split tree. Each split has a direction and a weight per child. One pure function turns the tree and the terminal size into a rectangle per pane, and both drawing and key handling use it.
- **Named presets are trees:** `classic` (the default), `query`, `browse`, `tall` and `focus` (one pane at a time). Editing a preset with the `ctrl+w` window commands (move, swap, rotate, resize) produces a `custom` layout. "Reset layout" goes back to the preset it came from.
- **Hide and maximise are flags, not tree edits:** a hidden pane keeps its slot, and maximising sets a zoom flag on the tree, as tmux does.
- **Small terminals step down at render time:** the Schema pane hides first, then below 70 columns the `focus` layout applies. The step-down is never saved, and the chosen layout returns when the terminal grows.
- **Switched in the app only:** keys and the command palette change the layout, and it is remembered in `state/tui.json`. There is no config-file layout, not even a preset name, so the file has nothing to keep in sync with the remembered state.
- **The words:** "Layout" now means the pane arrangement. The old Miller/tree/flat layouts become the "Explorer style" (`columns`, `tree`, `list`), still on `L`. The state file gets new keys and still reads the old `"layout"` key, which is not reused, because older bqt instances share the file.

## Consequences

- The spec's "two screens" model (section 15), the detail-pane placement (`P`) and the detail zoom are replaced. The spec is updated.
- Focus moves become geometric: `ctrl+w h/j/k/l` finds the neighbouring rectangle and prefers the most recently focused pane when several qualify. Today's fixed targets (`ctrl+w l` goes to the Editor) and their tests change.
- The Schema pane becomes a pane in its own right, focusable and scrollable. The preview pane goes away: free sampling moves into the Results pane (the Sample), and a `SELECT` shortcut opens a new buffer.
- Golden tests change across the TUI. They are regenerated, and new scenarios cover each preset and the step-down sizes.
- Mouse support (click to focus, wheel) can be added later on top of the same rectangles. Dragging borders is not planned.
- Layout stays frontend-only, as the core boundary requires (spec section 3). A GUI would bring its own arrangement.
