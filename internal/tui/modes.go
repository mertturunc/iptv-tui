package tui

import (
	tea "github.com/charmbracelet/bubbletea"
)

// modeSpec bundles everything one mode needs — key routing, overlay content,
// footer hints — so adding a mode touches a single table entry instead of the
// three parallel switches (key dispatch in update.go, overlay paint and
// footer hints in view.go) that used to drift apart unnoticed.
type modeSpec struct {
	step    func(*App, tea.KeyMsg) (tea.Model, tea.Cmd)
	overlay func(*App, int) string
	// hints is a func, not a slice: entries resolve glyph tokens at render
	// time so --ascii swaps apply. A package-level slice would bake in the
	// Unicode defaults at init and leak them into ASCII frames.
	hints func() []keyHint
}

var modeSpecs = map[mode]modeSpec{
	modeHelp: {
		step:    (*App).helpStep,
		overlay: func(m *App, frame int) string { return helpView(m.width-16, frame-2, m.helpScroll) },
		hints:   func() []keyHint { return []keyHint{{"up/down", "scroll"}, {"esc", "back"}} },
	},
	modeSearch: {
		step:  (*App).searchStep,
		hints: func() []keyHint { return []keyHint{{"type", "filter"}, {"enter", "done"}, {"esc", "clear"}} },
	},
	modeAdd: {
		step: (*App).addStep,
		overlay: func(m *App, frame int) string {
			if m.addForm == nil {
				return ""
			}
			return m.addForm.View(m.width-8, frame-2)
		},
		hints: func() []keyHint {
			return []keyHint{{"enter", "next/submit"}, {glyphArrowUpDown, "move"}, {"k", "toggle m3u/xtream"}, {"esc", "cancel"}}
		},
	},
	modeSources: {
		step: (*App).sourcesStep,
		overlay: func(m *App, frame int) string {
			if m.srcOverlay == nil {
				return ""
			}
			return m.srcOverlay.View()
		},
		hints: func() []keyHint {
			return []keyHint{{"enter", "select"}, {"a", "add"}, {"r", "reload"}, {"d", "delete"}, {"esc", "back"}}
		},
	},
	modeEpg: {
		step: (*App).epgStep,
		overlay: func(m *App, frame int) string {
			if m.epg == nil {
				return ""
			}
			return m.epg.View(m.width-8, frame-4)
		},
		hints: func() []keyHint {
			return []keyHint{{glyphArrowUpDown, "scroll"}, {"esc", "back"}}
		},
	},
	modeMpv: {
		step: (*App).mpvStep,
		overlay: func(m *App, frame int) string {
			if m.mpvForm == nil {
				return ""
			}
			return m.mpvForm.View(m.width-8, frame-2)
		},
		hints: func() []keyHint { return []keyHint{{"enter", "save path"}, {"esc", "skip"}} },
	},
	modeCast: {
		step: (*App).castStep,
		overlay: func(m *App, frame int) string {
			if m.castForm == nil {
				return ""
			}
			return m.castForm.View(m.width-8, frame-2)
		},
		hints: func() []keyHint {
			return []keyHint{{"enter", "cast"}, {"up/down", "pick TV"}, {"r", "rescan"}, {"esc", "back (x stops TV)"}}
		},
	},
	modeAirplay: {
		step: (*App).airStep,
		overlay: func(m *App, frame int) string {
			if m.airForm == nil {
				return ""
			}
			return m.airForm.View(m.width-8, frame-2)
		},
		hints: func() []keyHint {
			return []keyHint{{"enter", "play"}, {"up/down", "pick TV"}, {"r", "rescan"}, {"esc", "back (x stops TV)"}}
		},
	},
}

// routeKey sends a key to the active mode's step handler, reporting false for
// the main mode (which handleKey serves inline below).
func (m *App) routeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	if spec, ok := modeSpecs[m.mode]; ok {
		model, cmd := spec.step(m, msg)
		return model, cmd, true
	}
	return m, nil, false
}

// overlayFor renders the active mode's overlay content for the given frame
// height, or "" when the mode paints none.
func (m *App) overlayFor(frame int) string {
	if spec, ok := modeSpecs[m.mode]; ok && spec.overlay != nil {
		return spec.overlay(m, frame)
	}
	return ""
}

// footerHints resolves the footer key hints: the mode's table entry, or the
// main-mode hints when the mode declares none.
func (m *App) footerHints() []keyHint {
	if m.mode == modeHelp {
		if helpPageSize(m.bodyH()-2) < len(helpRows()) {
			if spec, ok := modeSpecs[m.mode]; ok && spec.hints != nil {
				return spec.hints()
			}
		}
		return []keyHint{{"esc", "back"}}
	}
	if spec, ok := modeSpecs[m.mode]; ok && spec.hints != nil {
		return spec.hints()
	}
	return m.mainKeyHints()
}
