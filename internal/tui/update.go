package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/player"
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
	switch m.mode {
	case modeHelp:
		return m.helpStep(msg)
	case modeSearch:
		return m.searchStep(msg)
	case modeAdd:
		return m.addStep(msg)
	case modeSources:
		return m.sourcesStep(msg)
	case modeEpg:
		return m.epgStep(msg)
	case modeMpv:
		return m.mpvStep(msg)
	case modeCast:
		return m.castStep(msg)
	}

	switch msg.String() {
	case "ctrl+c":
		m.flushAndSave()
		m.stopMpvIfNotKept()
		return m, tea.Quit
	case "q":
		m.flushAndSave()
		m.stopMpvIfNotKept()
		return m, tea.Quit
	case "?":
		m.mode = modeHelp
		return m, nil
	case "/":
		m.mode = modeSearch
		m.searchIn.SetValue(m.searchFilter)
		return m, m.searchIn.Focus()
	case "s":
		m.openSources()
		return m, nil
	case "a":
		m.openAdd(store.SourceM3U)
		return m, nil
	case "r":
		if m.activeIdx >= 0 {
			m.startSourceLoad(m.activeIdx)
		}
		return m, nil
	case "e":
		if !m.inSeries {
			if ch, ok := m.selectedChannel(); ok {
				m.openEpg(ch)
			} else {
				m.setStatus("no channel selected", false)
			}
		}
		return m, nil
	case "f":
		m.toggleFavOnSelected()
		return m, nil
	case "x":
		stopped := false
		if m.playing != nil {
			m.setStatus("stopping "+m.playerName()+glEllipsis, false)
			m.stopMpv()
			stopped = true
		}
		if m.castForm != nil && m.castForm.caster != nil {
			// Stop the TV too when both run (mpv first, per README);
			// a single press ends everything.
			m.setStatus("stopping TV"+glEllipsis, false)
			m.stopCast()
			stopped = true
		}
		if !stopped {
			m.setStatus("nothing playing", false)
		}
		return m, nil
	case "m":
		m.cfg.Cfg.Settings.KeepMpvOnQuit = !m.cfg.Cfg.Settings.KeepMpvOnQuit
		m.cfg.Save()
		state := "off"
		if m.cfg.Cfg.Settings.KeepMpvOnQuit {
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
		if m.focus == "channels" {
			m.focus = "groups"
		} else {
			m.focus = "channels"
		}
		return m, nil
	case "g":
		m.focus = "groups"
		return m, nil
	case "left", "h":
		if m.focus == "channels" {
			m.focus = "groups"
		}
		return m, nil
	case "right", "l":
		m.focus = "channels"
		return m, nil
	case "enter":
		if m.numBuf != "" {
			m.jumpToNumber()
			return m, nil
		}
		return m.onActivate()
	case "o":
		// A stale digit buffer must not swallow a play request: digits are
		// only a jump prefix for enter, so drop them here and play.
		m.numBuf = ""
		return m.onPlaySelected()
	case "c":
		m.openCast()
		return m, nil
	case "esc":
		if m.numBuf != "" {
			m.numBuf = ""
			m.setStatus("number entry cleared", false)
			return m, nil
		}
		if m.inSeries {
			m.exitSeries()
		}
		return m, nil
	case "backspace":
		if m.numBuf != "" {
			m.numBuf = ""
			m.setStatus("number entry cleared", false)
			return m, nil
		}
		if m.inSeries {
			m.exitSeries()
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
		if m.focus == "channels" {
			m.chList, cmd = m.chList.Update(msg)
		} else {
			m.grpList, cmd = m.grpList.Update(msg)
		}
		return m, cmd
	}
}

func (m *App) onActivate() (tea.Model, tea.Cmd) {
	if m.focus == "groups" {
		m.groupIdx = m.grpList.Index()
		m.focus = "channels"
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
	if m.focus != "channels" {
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
		m.setStatus(glStar+" added favorite: "+ch.Name, false)
	} else {
		m.setStatus("removed favorite: "+ch.Name, false)
	}
	m.rebuildItems()
}

func (m *App) setMediaType(t store.MediaType) {
	if m.activeIdx < 0 {
		return
	}
	src := &m.cfg.Cfg.Sources[m.activeIdx]
	if !src.Kind.IsXtream() {
		return
	}
	m.mediaType = t
	m.inSeries = false
	m.groupIdx = 0
	m.focus = "channels"
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
			m.setStatus(fmt.Sprintf("jumped to #%d "+glSep+" %s", n, c.Name), false)
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
		m.setStatus("loaded "+m.cfg.Cfg.Sources[v.idx].Name, false)
	}
	if m.activeIdx == v.idx {
		m.rebuildItems()
	}
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
	m.focus = "channels"
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

func (m *App) onPlayerMsg(v playerMsg) {
	m.setBusy(false, "")
	if v.err != nil {
		m.setStatus("mpv error: "+v.err.Error(), true)
		return
	}
	p := v.proc
	m.playing = p
	m.playingCh = v.ch
	ch := v.ch
	p.OnLog = func(line string, isErr bool) {
		m.send(playerLogMsg{line: line, isErr: isErr})
	}
	p.OnExit = func() {
		m.send(playerExitedMsg{ch: ch, proc: p})
	}
	label := playLabel(ch)
	m.setStatus(glPlay+" "+v.proc.PlayerName()+": "+label, false)
}

func (m *App) onPlayerLog(v playerLogMsg) {
	if v.isErr {
		m.setStatus(m.playerName()+": "+ellipsize(v.line, 90), true)
	}
}

func (m *App) onPlayerExited(v playerExitedMsg) {
	pos, dur := v.proc.Position()
	if pos > 0 {
		m.cfg.RecordPosition(v.ch.Key, pos, dur)
	}
	m.cfg.Save()
	if m.playing == v.proc {
		m.playing = nil
		m.playingCh = store.Channel{}
		m.playPos, m.playDur = 0, 0
	}
	m.setStatus("mpv closed"+withPos(pos), false)
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
		m.focus = "channels"
		m.rebuildItems()
		return m, nil
	}
	var cmd tea.Cmd
	m.searchIn, cmd = m.searchIn.Update(msg)
	m.searchFilter = strings.TrimSpace(m.searchIn.Value())
	m.rebuildItems()
	return m, cmd
}

func (m *App) addStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.String() == "k" {
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
			m.setStatus("incomplete source "+glDash+" fill the required fields", true)
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
	case "a":
		m.openAdd(store.SourceM3U)
		return m, nil
	case "enter":
		if item, ok := m.srcOverlay.SelectedItem().(sourceListItem); ok {
			m.selectSource(item.idx)
			m.mode = modeMain
		}
		return m, nil
	case "r":
		if item, ok := m.srcOverlay.SelectedItem().(sourceListItem); ok {
			m.startSourceLoad(item.idx)
			m.setStatus("reloading "+m.cfg.Cfg.Sources[item.idx].Name, false)
		}
		return m, nil
	case "d":
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
	name := m.cfg.Cfg.Sources[idx].Name
	m.cfg.RemoveSource(m.cfg.Cfg.Sources[idx].ID)
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
		m.setStatus("no sources left "+glDash+" press a to add one", false)
	}
	m.rebuildItems()
	m.setStatus("deleted source "+name, false)
}

func (m *App) epgStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		return m, nil
	case "up":
		if m.epg.scroll > 0 {
			m.epg.scroll--
		}
		return m, nil
	case "down":
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

// mpvStep drives the player setup form. Enter saves a pasted binary path (an
// empty field just re-checks, in case mpv was installed meanwhile); esc skips
// and leaves browsing working without playback.
func (m *App) mpvStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		m.setStatus("player setup skipped "+glDash+" press enter on a channel to retry", false)
		return m, nil
	case "enter":
		if path := strings.TrimSpace(m.mpvForm.path.Value()); path != "" {
			m.cfg.Cfg.Settings.MpvPath = path
			m.cfg.Save()
		}
		if exe, kind, ok := player.Detect(m.cfg.Cfg.Settings.MpvPath); ok {
			m.mode = modeMain
			m.setStatus(string(kind)+": "+exe, false)
		} else {
			m.setStatus("still no player found "+glDash+" install mpv or paste its full path", true)
		}
		return m, nil
	}
	return m, m.mpvForm.Update(msg)
}

// ---------- sources overlay helpers ----------

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
	m.srcOverlay.Title = "SOURCES " + glSep + " " + fmt.Sprint(len(m.cfg.Cfg.Sources))
	m.srcOverlay.SetItems(m.buildSourceItems())
	if m.activeIdx >= 0 && m.activeIdx < len(m.cfg.Cfg.Sources) {
		m.srcOverlay.Select(m.activeIdx)
	}
	m.mode = modeSources
	m.layout()
}

func (m *App) buildSourceItems() []list.Item {
	items := make([]list.Item, 0, len(m.cfg.Cfg.Sources))
	for i := range m.cfg.Cfg.Sources {
		src := m.cfg.Cfg.Sources[i]
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

// openMpvSetup shows the player setup form: no mpv, VLC, or MPC-HC was found,
// so playback needs either an install or an explicit binary path.
func (m *App) openMpvSetup() {
	m.mpvForm = newMpvForm(m.width - 10)
	m.mode = modeMpv
	m.layout()
	m.setStatus("no player found "+glDash+" install mpv or set its path", true)
}

// playerName names the running backend for status lines.
func (m *App) playerName() string {
	if m.playing != nil {
		return m.playing.PlayerName()
	}
	return "mpv"
}

func (m *App) stopMpvIfNotKept() {
	if m.playing == nil || m.cfg.Cfg.Settings.KeepMpvOnQuit {
		return
	}
	pos, dur := m.playing.Position()
	if pos > 0 {
		m.cfg.RecordPosition(m.playingCh.Key, pos, dur)
		m.cfg.Save()
	}
	m.playing.Stop()
}

func withPos(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf(" at %02d:%02d", int(d/time.Minute), int(d/time.Second)%60)
}
