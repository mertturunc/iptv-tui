package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/store"
)

func (m *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case tea.WindowSizeMsg:
		w, h := v.Width, v.Height
		if w <= 0 || h <= 0 {
			if cw, ch := currentSize(); cw > 0 && ch > 0 {
				w, h = cw, ch
			}
		}
		if w <= 0 {
			w = 80
		}
		if h <= 0 {
			h = 24
		}
		if w != m.termW || h != m.termH {
			m.termW, m.termH = w, h
			m.margin = marginFor(w, h)
			// The console is the terminal minus the quiet void down each side,
			// so every width below is the inner one the rails are laid out in.
			m.width, m.height = w-2*m.margin, h
			m.ready = true
			m.layout()
		}
		return m, nil
	case tea.KeyMsg:
		return m.handleKey(v)
	case sourceLoadedMsg:
		m.onSourceLoaded(v)
		return m, nil
	case seriesLoadedMsg:
		m.onSeriesLoaded(v)
		return m, nil
	case playerMsg:
		m.onPlayerMsg(v)
		if m.playing != nil {
			return m, playTick()
		}
		return m, nil
	case playTickMsg:
		// Advance the now-playing strip. The tick re-arms only while mpv runs,
		// so an idle console schedules nothing.
		if m.playing != nil {
			m.playPos, m.playDur = m.playing.Position()
			return m, playTick()
		}
		m.playPos, m.playDur = 0, 0
		return m, nil
	case playerExitedMsg:
		m.onPlayerExited(v)
		return m, nil
	case playerLogMsg:
		m.onPlayerLog(v)
		return m, nil
	case castDoneMsg:
		return m, m.onCastDone(v)
	case castStopMsg:
		m.onCastStop(v)
		return m, nil
	case castTickMsg:
		// Re-arm only while a session lives and no cast/stop is in flight;
		// the poller would otherwise interleave on the receiver channel.
		if m.castForm != nil && m.castForm.caster != nil && !m.castForm.busy {
			m.pollCastStats()
			return m, castTick()
		}
		return m, nil
	case castStatMsg:
		m.onStatMsg(v)
		return m, nil
	case castScanMsg:
		m.onScanMsg(v)
		return m, nil
	case airDoneMsg:
		return m, m.onAirDone(v)
	case airStopMsg:
		m.onAirStop(v)
		return m, nil
	case airTickMsg:
		// Re-arm only while a session lives and no play/stop is in flight.
		if m.airForm != nil && m.airForm.client != nil && !m.airForm.busy {
			m.pollAirStats()
			return m, airTick()
		}
		return m, nil
	case airStatMsg:
		m.onAirStat(v)
		return m, nil
	case airScanMsg:
		m.onAirScan(v)
		return m, nil
	case epgBgLoadedMsg:
		// Background per-channel refresh; ignore if the user has moved on.
		if m.mode == modeEpg && m.epg.title == v.title {
			m.epg.loading = false
			if v.err != nil {
				if len(m.epg.progs) == 0 {
					m.epg.err = "EPG unavailable: " + v.err.Error()
				}
				return m, nil
			}
			m.epg.progs = v.progs
			m.epg.scroll = 0
			if len(v.progs) == 0 {
				m.epg.err = "no EPG data for this channel"
			}
		}
		return m, nil
	case epgSourceUpdatedMsg:
		// Swap the refetched index onto the shared Loaded on the UI goroutine,
		// then refresh the visible list in case now/next titles changed.
		if v.idx >= 0 && v.idx < len(m.loaded) && m.loaded[v.idx] != nil && v.epg != nil {
			m.loaded[v.idx].EPG = v.epg
		}
		if v.idx == m.activeIdx {
			m.rebuildChannelItems()
		}
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func isDigit(s string) bool {
	return len(s) == 1 && s[0] >= '0' && s[0] <= '9'
}

func (m *App) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// UX aliases for the main rails: vim-style h/j/k/l on top of the arrows, so
	// both vocabularies work without crowding the text inputs in other modes.
	if m.mode == modeMain {
		switch msg.String() {
		case "j":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "k":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		}
	}
	if model, cmd, ok := m.routeKey(msg); ok {
		return model, cmd
	}

	switch msg.String() {
	case "ctrl+c", "q", "Q":
		m.flushAndSave()
		m.stopMpvIfNotKept()
		return m, tea.Quit
	case "?", "F1":
		m.mode = modeHelp
		return m, nil
	case "/", "ctrl+f":
		m.mode = modeSearch
		m.searchIn.SetValue(m.searchFilter)
		return m, m.searchIn.Focus()
	case "s", "S":
		m.openSources()
		return m, nil
	case "a", "A", "n", "+":
		// `A` opens AirPlay (below); keep `a` for add and `A` for Apple TV.
		if msg.String() == "A" {
			m.openAirplay()
			return m, nil
		}
		m.openAdd(store.SourceM3U)
		return m, nil
	case "r", "R":
		if m.activeIdx >= 0 {
			m.startSourceLoad(m.activeIdx)
		}
		return m, nil
	case "e", "i":
		if !m.inSeries {
			if ch, ok := m.selectedChannel(); ok {
				m.openEpg(ch)
			} else {
				m.setStatus("no channel selected", false)
			}
		}
		return m, nil
	case "f", "F", "*":
		m.toggleFavOnSelected()
		return m, nil
	case "x", "X":
		stopped := false
		if m.playing != nil {
			m.setStatus("stopping "+m.playerName()+glyphEllipsis, false)
			m.stopMpv()
			stopped = true
		}
		if m.castForm != nil && m.castForm.caster != nil {
			// Stop the TV too when both run (mpv first, per README);
			// a single press ends everything.
			m.setStatus("stopping TV"+glyphEllipsis, false)
			m.stopCast()
			stopped = true
		}
		if m.airForm != nil && m.airForm.client != nil {
			m.setStatus("stopping Apple TV"+glyphEllipsis, false)
			m.stopAirplay()
			stopped = true
		}
		if !stopped {
			m.setStatus("nothing playing", false)
		}
		return m, nil
	case "m", "K":
		kept := m.cfg.ToggleKeepMpvOnQuit()
		m.cfg.Save()
		state := "off"
		if kept {
			state = "on"
		}
		m.setStatus("keep mpv running after quit: "+state, false)
		return m, nil
	case "1":
		m.setMediaType(store.Live)
		return m, nil
	case "2":
		m.setMediaType(store.Movie)
		return m, nil
	case "3":
		m.setMediaType(store.Series)
		return m, nil
	case "tab":
		if m.focus == focusChannels {
			m.focus = focusGroups
		} else {
			m.focus = focusChannels
		}
		return m, nil
	case "g", "G":
		m.focus = focusGroups
		return m, nil
	case "left", "h":
		if m.focus == focusChannels {
			m.focus = focusGroups
		}
		return m, nil
	case "right", "l":
		if m.focus == focusGroups {
			m.focus = focusChannels
		}
		return m, nil
	case "[":
		m.cycleMediaType(-1)
		return m, nil
	case "]":
		m.cycleMediaType(1)
		return m, nil
	case "#":
		m.numBuf = ""
		m.setStatus("type channel number, then enter", false)
		return m, nil
	case "enter", " ", "space":
		if m.numBuf != "" {
			m.jumpToNumber()
			return m, nil
		}
		return m.onActivate()
	case "o", "p":
		// A stale digit buffer must not swallow a play request: digits are
		// only a jump prefix for enter, so drop them here and play.
		m.numBuf = ""
		return m.onPlaySelected()
	case "c", "C":
		m.openCast()
		return m, nil
	case "v", "V":
		m.openAirplay()
		return m, nil
	case "esc", "backspace":
		if m.numBuf != "" {
			m.numBuf = ""
			m.setStatus("number entry cleared", false)
			return m, nil
		}
		if m.inSeries {
			m.exitSeries()
			return m, nil
		}
		return m, nil
	case "delete":
		// Delete is destructive in text fields elsewhere; here it only clears
		// a pending numeric entry so it never quits the app by accident.
		if m.numBuf != "" {
			m.numBuf = ""
			m.setStatus("number entry cleared", false)
		}
		return m, nil
	default:
		if isDigit(msg.String()) {
			m.numBuf += msg.String()
			if len(m.numBuf) > 4 {
				m.numBuf = m.numBuf[len(m.numBuf)-4:]
			}
			// Avoid burning the key; the list doesn't care about digits.
			return m, nil
		}
		var cmd tea.Cmd
		if m.focus == focusChannels {
			m.chList, cmd = m.chList.Update(msg)
		} else {
			m.grpList, cmd = m.grpList.Update(msg)
		}
		return m, cmd
	}
}

func (m *App) onActivate() (tea.Model, tea.Cmd) {
	if m.focus == focusGroups {
		m.groupIdx = m.grpList.Index()
		m.focus = focusChannels
		m.rebuildChannelItems()
		return m, nil
	}
	ch, ok := m.selectedChannel()
	if !ok {
		m.setStatus("no channel selected", false)
		return m, nil
	}
	if m.inSeries || ch.Type != store.Series {
		m.launchChannel(ch)
		return m, nil
	}
	m.openSeries(ch)
	return m, nil
}

func (m *App) onPlaySelected() (tea.Model, tea.Cmd) {
	ch, ok := m.selectedChannel()
	if !ok {
		m.setStatus("no channel selected", false)
		return m, nil
	}
	if ch.Type == store.Series && !m.inSeries {
		// Series containers carry no direct URL (see loadXtreamNet), so
		// "play" expands the series just like enter; episodes play normally.
		m.openSeries(ch)
		return m, nil
	}
	m.launchChannel(ch)
	return m, nil
}

func (m *App) selectedChannel() (store.Channel, bool) {
	if m.focus != focusChannels {
		return store.Channel{}, false
	}
	if m.inSeries {
		idx := m.chList.Index()
		if idx < 0 || idx >= len(m.visible) {
			return store.Channel{}, false
		}
		return m.visible[idx], true
	}
	if m.chList.Index() < 0 {
		return store.Channel{}, false
	}
	item, ok := m.chList.SelectedItem().(channelItem)
	if !ok {
		return store.Channel{}, false
	}
	return item.ch, true
}

func (m *App) toggleFavOnSelected() {
	ch, ok := m.selectedChannel()
	if !ok {
		m.setStatus("no channel selected", false)
		return
	}
	nowFav := m.cfg.ToggleFavorite(ch.Key)
	m.cfg.Save()
	if nowFav {
		m.setStatus(glyphFavorite+" added favorite: "+ch.Name, false)
	} else {
		m.setStatus("removed favorite: "+ch.Name, false)
	}
	m.rebuildItems()
}

// cycleMediaType moves Live/Movies/Series in the given direction, mirroring the
// 1/2/3 shortcuts so arrow keys on the rails can reach the other tabs without
// a separate gesture.
func (m *App) cycleMediaType(delta int) {
	if m.activeIdx < 0 {
		return
	}
	src, ok := m.cfg.SourceAt(m.activeIdx)
	if !ok || !src.Kind.IsXtream() {
		return
	}
	order := []store.MediaType{store.Live, store.Movie, store.Series}
	cur := 0
	for i, t := range order {
		if t == m.mediaType {
			cur = i
		}
	}
	m.setMediaType(order[(cur+delta+len(order))%len(order)])
}

func (m *App) setMediaType(t store.MediaType) {
	if m.activeIdx < 0 {
		return
	}
	src, ok := m.cfg.SourceAt(m.activeIdx)
	if !ok || !src.Kind.IsXtream() {
		return
	}
	m.mediaType = t
	m.inSeries = false
	m.groupIdx = 0
	m.focus = focusChannels
	m.rebuildItems()
}

func (m *App) jumpToNumber() {
	num := m.numBuf
	m.numBuf = ""
	n, err := strconv.Atoi(num)
	if err != nil || n <= 0 {
		return
	}
	for i, c := range m.visible {
		if c.Number == n {
			m.chList.Select(i)
			m.setStatus(fmt.Sprintf("jumped to #%d "+glyphSeparator+" %s", n, c.Name), false)
			return
		}
	}
	m.setStatus("no channel number "+num, false)
}

func (m *App) onSourceLoaded(v sourceLoadedMsg) {
	m.setBusy(false, "")
	m.loaded[v.idx] = v.loaded
	if m.activeIdx < 0 && m.loaded[0] != nil {
		m.selectSource(0)
		return
	}
	if v.err != nil {
		m.setStatus("load failed: "+v.err.Error(), true)
	} else if m.activeIdx == v.idx {
		if name, ok := m.cfg.SourceName(v.idx); ok {
			m.setStatus("loaded "+name, false)
		}
	}
	if m.activeIdx == v.idx {
		m.rebuildItems()
	}
}

// helpStep drives the help sheet. Left/right preview the palette ring in
// place; esc and every other key close the sheet (the previous behavior).
func (m *App) helpStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "left", "right":
		if msg.String() == "left" {
			m.cycleTheme(-1)
		} else {
			m.cycleTheme(1)
		}
		return m, nil
	default:
		m.mode = modeMain
		return m, nil
	}
}

// ---------- mode steps ----------

func (m *App) searchStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		m.searchFilter = ""
		m.searchIn.Blur()
		m.rebuildItems()
		return m, nil
	case "enter":
		m.mode = modeMain
		m.searchIn.Blur()
		m.focus = focusChannels
		m.rebuildItems()
		return m, nil
	}
	var cmd tea.Cmd
	m.searchIn, cmd = m.searchIn.Update(msg)
	m.searchFilter = strings.TrimSpace(m.searchIn.Value())
	m.rebuildItems()
	return m, cmd
}
