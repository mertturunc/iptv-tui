## Agent skills

### Issue tracker

Issues live in GitHub Issues. See `docs/agents/issue-tracker.md`.

### Triage labels

Default five-role vocabulary (label strings equal role names). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context layout (root `GLOSSARY.md` + `docs/adr/`). See `docs/agents/domain.md`.


## Navigation

Key binding implementation lives in:
- `internal/tui/update.go` (`handleKey`, `jumpToNumber`, `setMediaType`)
- `internal/tui/modes.go` (`modeSpecs`, `routeKey`, `footerHints`)
- `internal/tui/overlays.go` (`helpView`, form `Update` methods)
- `internal/tui/sources.go`, `internal/tui/cast.go`, `internal/tui/airplay.go`, `internal/tui/epgview.go`
- Inventory: `docs/keybindings.md`
- Standards: `CODING_STANDARDS.md`
