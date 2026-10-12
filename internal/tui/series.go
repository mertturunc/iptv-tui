package tui

import (
	"fmt"

	"github.com/mertturunc/iptv-tui/internal/load"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// ---------- series ----------
// Series containers expand into season/episode rails here: opening a series,
// receiving its lazily loaded episodes, leaving the episode view, and naming
// the season rows.

func (m *App) openSeries(ch store.Channel) {
	ld := m.activeLoaded()
	if ld == nil {
		return
	}
	// Copy: the source crosses into a background goroutine, where an interior
	// slice pointer would race a later Add/RemoveSource reallocation.
	src, ok := m.cfg.FindSourceCopy(ch.SourceID)
	if !ok || !src.Kind.IsXtream() {
		return
	}
	m.seriesTitle = ch.Name
	m.busy = true
	m.busyMsg = "loading episodes" + glyphEllipsis
	m.setStatus("", false)
	go func() {
		eps := load.ExpandSeries(ld, &src, ch.SeriesID, &m.seriesCache)
		m.send(seriesLoadedMsg{channels: eps})
	}()
}

// seasonLabel names the groups-pane row an episode belongs to. Episodes from
// mapEpisodes always carry "Season N"; anything else falls into Episodes so
// the rail never shows a blank row.
func seasonLabel(c store.Channel) string {
	if c.Group != "" {
		return c.Group
	}
	return "Episodes"
}

func (m *App) onSeriesLoaded(v seriesLoadedMsg) {
	m.setBusy(false, "")
	m.inSeries = false
	if v.err != nil {
		m.setStatus("series load failed: "+v.err.Error(), true)
		m.rebuildItems()
		return
	}
	m.episodeChans = v.channels
	if len(v.channels) == 0 {
		m.setStatus("no episodes found for "+m.seriesTitle, false)
		m.rebuildItems()
		return
	}
	m.inSeries = true
	m.groupIdx = 0
	m.focus = focusChannels
	m.rebuildItems()
	m.grpList.Select(0)
	m.setStatus(fmt.Sprintf("%d episodes", len(v.channels)), false)
}

// exitSeries leaves the season/episode view and returns to the series list.
func (m *App) exitSeries() {
	m.inSeries = false
	m.episodeChans = nil
	m.groupIdx = 0
	m.rebuildItems()
	m.setStatus("back to series list", false)
}
