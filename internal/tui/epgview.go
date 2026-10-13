package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/epg"
	"github.com/mertturunc/iptv-tui/internal/load"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// ---------- EPG ----------
// Everything behind the `e` guide panel: opening it for Xtream (cached short
// listing instantly, network refresh behind) and M3U (in-memory XMLTV index),
// plus the list-trimming helper.

func (m *App) openEpg(ch store.Channel) {
	m.epg = &epgView{title: ch.Name}
	m.mode = modeEpg
	src := m.cfg.FindSource(ch.SourceID)
	ld := m.activeLoaded()
	if src == nil || ld == nil {
		m.epg.err = "source not loaded"
		return
	}
	if src.Kind.IsXtream() && ch.StreamID > 0 {
		// Serve the cached listing instantly (fresh or stale), then refresh
		// in the background; the panel only spins when there is nothing yet.
		cached, cErr := epg.LoadShortEpg(src.ID, ch.StreamID, 6*time.Hour)
		switch cErr {
		case epg.ErrNoCache:
			m.epg.loading = true
		default: // fresh or stale — show immediately
			m.epg.progs = cached
		}
		go func() {
			fresh, err := load.EpgForStream(src, ch.StreamID)
			epg.SaveShortEpg(src.ID, ch.StreamID, fresh, time.Now())
			m.send(epgBgLoadedMsg{title: ch.Name, progs: fresh, err: err})
		}()
		return
	}
	// M3U: use the in-memory XMLTV index (served from disk cache on load).
	tvgID := ch.TvgID
	if tvgID == "" {
		tvgID = ch.TvgName
	}
	progs := ld.EPG.Get(tvgID)
	if len(progs) == 0 && tvgID != ch.TvgName {
		progs = ld.EPG.Get(ch.TvgName)
	}
	m.epg.progs = trimPrograms(progs, 30)
	if len(progs) == 0 {
		m.epg.err = "no EPG data for this channel"
	}
}

func trimPrograms(progs []store.Program, n int) []store.Program {
	if len(progs) <= n {
		return progs
	}
	return progs[:n]
}

func (m *App) epgStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		return m, nil
	case "up", "k":
		if m.epg.scroll > 0 {
			m.epg.scroll--
		}
		return m, nil
	case "down", "j":
		if m.epg.scroll < len(m.epg.progs)-1 {
			m.epg.scroll++
		}
		return m, nil
	case "ctrl+c":
		m.flushAndSave()
		return m, tea.Quit
	}
	return m, nil
}
