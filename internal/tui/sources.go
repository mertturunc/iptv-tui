package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/store"
)

// ---------- sources ----------
// Everything behind the `s` sheet and the `a` add form: opening them,
// stepping their keys, validating submissions, and deleting sources.

func (m *App) addStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "k" || msg.String() == "t" {
		kind := store.SourceM3U
		if m.addForm.kind == store.SourceM3U {
			kind = store.SourceXtream
		}
		m.openAdd(kind)
		return m, nil
	}
	res, cmd := m.addForm.Update(msg)
	if res.cancelled {
		m.mode = modeMain
		return m, nil
	}
	if res.submitted {
		if res.src.Kind.IsM3U() && res.src.URL == "" || res.src.Kind.IsXtream() && res.src.Server == "" {
			m.setStatus("incomplete source "+glyphDash+" fill the required fields", true)
			return m, nil
		}
		m.cfg.AddSource(res.src)
		m.loaded = append(m.loaded, nil)
		m.cfg.Save()
		idx := len(m.loaded) - 1
		m.mode = modeMain
		m.selectSource(idx)
		m.startSourceLoad(idx)
		m.setStatus("added source "+res.src.Name, false)
		return m, nil
	}
	return m, cmd
}

func (m *App) sourcesStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		return m, nil
	case "a", "A", "n", "+":
		m.openAdd(store.SourceM3U)
		return m, nil
	case "enter":
		if item, ok := m.srcOverlay.SelectedItem().(sourceListItem); ok {
			m.selectSource(item.idx)
			m.mode = modeMain
		}
		return m, nil
	case "r", "R":
		if item, ok := m.srcOverlay.SelectedItem().(sourceListItem); ok {
			m.startSourceLoad(item.idx)
			if name, ok := m.cfg.SourceName(item.idx); ok {
				m.setStatus("reloading "+name, false)
			}
		}
		return m, nil
	case "d", "D", "delete":
		if item, ok := m.srcOverlay.SelectedItem().(sourceListItem); ok {
			m.deleteSource(item.idx)
		}
		return m, nil
	}
	var cmd tea.Cmd
	nu, cmd := m.srcOverlay.Update(msg)
	m.srcOverlay = &nu
	return m, cmd
}

func (m *App) deleteSource(idx int) {
	src, ok := m.cfg.SourceAt(idx)
	if !ok {
		return
	}
	m.cfg.RemoveSource(src.ID)
	m.loaded = append(m.loaded[:idx], m.loaded[idx+1:]...)
	if m.activeIdx == idx {
		m.activeIdx = -1
	}
	if m.activeIdx > idx {
		m.activeIdx--
	}
	m.cfg.Save()
	m.mode = modeSources
	items := m.buildSourceItems()
	if len(items) > 0 {
		m.srcOverlay.SetItems(items)
		m.srcOverlay.Select(0)
	} else {
		m.mode = modeMain
		m.setStatus("no sources left "+glyphDash+" press a to add one", false)
	}
	m.rebuildItems()
	m.setStatus("deleted source "+src.Name, false)
}

func (m *App) openSources() {
	if m.srcOverlay == nil {
		nl := list.New(nil, groupDelegate(colSignal, true), 40, 10)
		m.srcOverlay = &nl
		m.srcOverlay.SetFilteringEnabled(false)
		m.srcOverlay.SetShowHelp(false)
		m.srcOverlay.SetShowStatusBar(false)
	}
	// Re-chrome on every open, not just the first: the overlay holds focus
	// outright and wears the same chrome as the rails (signal badge, none of
	// the bubbles status/help bars) — in the CURRENT palette, which may have
	// changed since the overlay was built.
	m.srcOverlay.SetDelegate(groupDelegate(colSignal, true))
	srcStyles := m.srcOverlay.Styles
	srcStyles.Title = paneTitleStyle(colSignal, true)
	m.srcOverlay.Styles = srcStyles
	// Totals belong in the title, as they do on the channel rail, and the count
	// has to be restated here because sources can be added while this is open.
	sources := m.cfg.Sources()
	m.srcOverlay.Title = "SOURCES " + glyphSeparator + " " + fmt.Sprint(len(sources))
	m.srcOverlay.SetItems(m.buildSourceItems())
	if m.activeIdx >= 0 && m.activeIdx < len(sources) {
		m.srcOverlay.Select(m.activeIdx)
	}
	m.mode = modeSources
	m.layout()
}

func (m *App) buildSourceItems() []list.Item {
	sources := m.cfg.Sources()
	items := make([]list.Item, 0, len(sources))
	for i := range sources {
		src := sources[i]
		loaded := i < len(m.loaded) && m.loaded[i] != nil && m.loaded[i].LoadErr == nil
		items = append(items, sourceListItem{src: src, idx: i, load: !loaded})
	}
	return items
}

func (m *App) openAdd(kind store.SourceKind) {
	m.addForm = newAddForm(kind, m.width-10)
	m.mode = modeAdd
	m.layout()
}
