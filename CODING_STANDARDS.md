# Coding Standards

## Key bindings and text inputs

- Global keybindings in `internal/tui/update.go` (`handleKey`) and the mode handlers must never swallow printable characters required by focused text inputs (names, URLs, paths, hostnames).
- Overlay forms in `internal/tui/overlays.go` and the form `Update` methods own printable keys unless a global key is explicitly required and forwarded.
- Add or update targeted TUI tests for any new alias or binding.
- Keep in-app help (`internal/tui/overlays.go` `helpView`), footer hints (`internal/tui/view.go` `mainKeyHints`), README "TUI keys", and `docs/keybindings.md` in sync.
