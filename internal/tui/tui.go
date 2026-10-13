package tui

import (
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mertturunc/iptv-tui/internal/epg"
	"github.com/mertturunc/iptv-tui/internal/load"
	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
)

func (m *App) SetSend(fn func(tea.Msg)) { m.send = fn }

var nowFunc = time.Now

// ---------- styling (HEADEND theme in theme.go) ----------

// ---------- messages ----------

type sourceLoadedMsg struct {
	idx    int
	err    error
	loaded *store.Loaded
}
type seriesLoadedMsg struct {
	channels []store.Channel
	err      error
}
type playerMsg struct {
	ch   store.Channel
	proc *player.Process
	err  error
}
type playerExitedMsg struct {
	ch   store.Channel
	proc *player.Process
}
type playerLogMsg struct {
	line  string
	isErr bool
}

// epgBgLoadedMsg is a background EPG refresh for one channel; title guards
// against overwriting a newer view when the user has moved on.
type epgBgLoadedMsg struct {
	title string
	progs []store.Program
	err   error
}

// epgSourceUpdatedMsg reports that a source's XMLTV index finished refetching
// in the background (M3U sources). The index travels with the message so the
// swap onto the shared Loaded happens on the UI goroutine, not the fetcher.
type epgSourceUpdatedMsg struct {
	idx int
	epg *store.ProgramIndex
	err error
}

// playTickMsg advances the now-playing strip. It is separate from the spinner
// tick because it only exists while mpv is running, and it carries the
// position the resume feature records rather than animation frames.
type playTickMsg time.Time

// playTick re-arms once a second; Update stops re-arming it when mpv exits.
func playTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return playTickMsg(t) })
}

// marginFor is the quiet void around the console: one column down each side
// once the terminal has the room, so the rack does not bleed into the terminal
// edge. Vertically there is nothing to spare, so the void stays on the sides.
func marginFor(w, h int) int {
	if w >= 100 && h >= 30 {
		return 1
	}
	return 0
}

// ---------- app ----------

type mode int

const (
	modeMain mode = iota
	modeSearch
	modeAdd
	modeSources
	modeEpg
	modeHelp
	modeMpv
	modeCast
	modeAirplay
)

type groupKind string

const (
	groupKindAll    groupKind = "all"
	groupKindFav    groupKind = "fav"
	groupKindGroup  groupKind = "group"
	groupKindSeason groupKind = "season"
)

type groupEntry struct {
	label string
	count int
	kind  groupKind
}

// paneFocus names which rail takes the next keystroke.
type paneFocus string

const (
	focusChannels paneFocus = "channels"
	focusGroups   paneFocus = "groups"
)

type App struct {
	cfg  *store.Store
	send func(tea.Msg)

	width, height int

	loaded    []*store.Loaded
	activeIdx int
	mediaType store.MediaType

	groupEntries []groupEntry
	groupIdx     int
	visible      []store.Channel
	favs         map[string]bool

	inSeries     bool
	seriesTitle  string
	episodeChans []store.Channel

	chList  list.Model
	grpList list.Model
	focus   paneFocus

	mode mode

	searchIn     textinput.Model
	searchFilter string
	addForm      *addForm
	epg          *epgView
	srcOverlay   *list.Model
	mpvForm      *mpvForm
	castForm     *castForm
	airForm      *airForm

	spinner   spinner.Model
	busy      bool
	busyMsg   string
	status    string
	statusErr bool

	playing   *player.Process
	playingCh store.Channel

	numBuf string

	seriesCache sync.Map

	grpW, chW, grpH, chH int
	// grpLabelW is the rune budget for a group label: the group column minus
	// the star prefix, the gap, and the tabular count. Group rows are stamped
	// with it so names fill the column instead of stopping at a fixed width.
	grpLabelW int
	vertical  bool

	// termW/termH are the terminal's own size; width/height are the console
	// inside it, once the outer margin has been taken off.
	termW, termH int
	margin       int

	// chromeHue/chromeFocus remember what applyChrome last painted so it can
	// skip the repaint when neither the source hue nor the focus moved.
	chromeHue   string
	chromeFocus paneFocus

	// playPos/playDur are the last position mpv reported, refreshed on a tick
	// while playback runs so the now-playing strip advances.
	playPos, playDur time.Duration

	ready bool
}

func New(cfg *store.Store) *App {
	m := &App{
		cfg:       cfg,
		activeIdx: -1,
		mediaType: store.Live,
		favs:      map[string]bool{},
		focus:     focusChannels,
		status:    "welcome",
	}
	m.chList = list.New(nil, channelDelegate(colSignal, true), 40, 20)
	m.grpList = list.New(nil, groupDelegate(colSignal, false), 24, 20)
	// bubbles hardcodes the pagination dots, but it does expose them, so the
	// filled mark and the hollow one can come from our token table like the rest.
	m.chList.Paginator.ActiveDot = glyphFlag
	m.chList.Paginator.InactiveDot = glyphEmptySlot
	m.grpList.Paginator.ActiveDot = glyphFlag
	m.grpList.Paginator.InactiveDot = glyphEmptySlot
	m.searchIn = textinput.New()
	m.searchIn.Placeholder = "Filter channels" + glyphEllipsis
	m.searchIn.Width = 40
	m.spinner = spinner.New()
	m.spinner.Spinner = spinner.Dot
	if asciiMode {
		m.spinner.Spinner = spinnerASCII
	}
	m.spinner.Style = lipgloss.NewStyle().Foreground(colSignal)
	m.epg = &epgView{}
	m.chList.SetFilteringEnabled(false)
	m.grpList.SetFilteringEnabled(false)
	m.loaded = make([]*store.Loaded, len(cfg.Sources()))
	return m
}

// baseDelegate builds the shared rail chrome: single-line rows, no
// descriptions, signal selection bar, dimmed graphite for the rest.
func baseDelegate(hue lipgloss.Color, focused bool) list.DefaultDelegate {
	d := list.NewDefaultDelegate()
	d.SetHeight(1)
	d.ShowDescription = false
	d.SetSpacing(0)
	s := d.Styles
	s.SelectedTitle = selectStyle(hue, focused)
	s.SelectedDesc = selectStyle(hue, focused)
	s.DimmedTitle = s.DimmedTitle.Foreground(colDim)
	s.DimmedDesc = s.DimmedDesc.Foreground(colDim)
	s.FilterMatch = s.FilterMatch.Foreground(colSignal).Bold(true)
	d.Styles = s
	return d
}

func channelDelegate(hue lipgloss.Color, focused bool) list.ItemDelegate {
	return baseDelegate(hue, focused)
}

// Row shape of a group line, used by layout() to budget the label field so
// the count column lands on the pane's right edge without being clipped.
const (
	groupRowIndent = 1 // leading space, or the star for a favorite
	groupRowGap    = 1 // space between label and count
	groupCountW    = 5 // tabular count field
)

// groupRowPad is the horizontal padding the group delegate puts on every row.
// Read from the delegate styles when it is built so the label budget below
// stays correct if the delegate padding is ever changed.
var groupRowPad int

func groupDelegate(hue lipgloss.Color, focused bool) list.ItemDelegate {
	d := baseDelegate(hue, focused)
	s := d.Styles
	groupRowPad = s.NormalTitle.GetPaddingLeft() + s.NormalTitle.GetPaddingRight()
	return d
}

// applyChrome repaints both rails for the active source's hue and whichever
// rail currently holds focus: lit badge plus filled selection bar on the
// focused side, engraved title and unlit bar on the other. It repaints only
// when the hue or the focus actually moved, so layout() can call it on every
// pass without churning delegates each frame.
func (m *App) applyChrome() {
	hue := m.signal()
	groupsFocused := m.focus == focusGroups
	if m.chromeHue == string(hue) && m.chromeFocus == m.focus {
		return
	}
	m.chromeHue = string(hue)
	m.chromeFocus = m.focus

	m.chList.SetDelegate(channelDelegate(hue, !groupsFocused))
	m.grpList.SetDelegate(groupDelegate(hue, groupsFocused))

	// Bubbles ships the pane title as a neutral gray badge, which belongs to
	// no palette in this room. Replace it with the headend's own: signal fill
	// on the focused rail, engraved text on the idle one.
	chStyles := m.chList.Styles
	chStyles.Title = paneTitleStyle(hue, !groupsFocused)
	m.chList.Styles = chStyles

	grpStyles := m.grpList.Styles
	grpStyles.Title = paneTitleStyle(hue, groupsFocused)
	m.grpList.Styles = grpStyles
}

func (m *App) Init() tea.Cmd {
	m.startAllLoads()
	player.Debugf("color profile: %s", lipgloss.ColorProfile().Name())
	// Ask for mpv up front when none is runnable. Init runs only under the
	// real runtime (tests drive Update directly), so this never hijacks tests.
	if !player.Available(m.cfg.MpvPath()) {
		m.openMpvSetup()
	}
	return tea.Batch(m.spinner.Tick, tea.WindowSize())
}

func (m *App) activeLoaded() *store.Loaded {
	if m.activeIdx < 0 || m.activeIdx >= len(m.loaded) {
		return nil
	}
	return m.loaded[m.activeIdx]
}

func (m *App) startAllLoads() {
	for i := range m.cfg.Sources() {
		m.startSourceLoad(i)
	}
	if len(m.cfg.Sources()) == 0 {
		m.status = "no sources yet " + glyphDash + " press a to add one"
	}
}

func (m *App) startSourceLoad(idx int) {
	src, ok := m.cfg.SourceAt(idx)
	if !ok {
		return
	}
	// src is a value snapshot: the background fetch below never aliases the
	// live config slice.
	m.setBusy(true, "loading "+src.Name)
	go func() {
		var ld *store.Loaded
		var epgURLs []string
		if src.Kind.IsXtream() {
			ld = load.LoadXtream(&src)
		} else {
			ld, epgURLs = load.LoadM3U(&src)
		}
		m.send(sourceLoadedMsg{idx: idx, err: ld.LoadErr, loaded: ld})
		// Latent XMLTV refresh: channels are ready; EPG catches up in the
		// background (a cached index is served meanwhile by LoadM3U).
		if len(epgURLs) > 0 && ld.LoadErr == nil {
			go m.refreshXMLTV(epgURLs, idx, src.ID, m.effectiveSourceUA(src))
		}
	}()
}

// effectiveSourceUA falls back to the global default UA then a builtin.
func (m *App) effectiveSourceUA(src store.Source) string {
	if src.UserAgent != "" {
		return src.UserAgent
	}
	if ua := m.cfg.DefaultUserAgent(); ua != "" {
		return ua
	}
	return "iptv-tui/1.0"
}

// refreshXMLTV fetches a source's XMLTV index in the background, persists it
// to the disk cache, and hands the new index to the UI goroutine via message.
func (m *App) refreshXMLTV(urls []string, idx int, srcID, ua string) {
	newIdx, err := epg.FetchAndParse(urls, ua)
	if newIdx == nil {
		newIdx = &store.ProgramIndex{ByID: map[string][]store.Program{}}
	}
	epg.SaveIndexCache(srcID, newIdx, time.Now()) // best-effort
	m.send(epgSourceUpdatedMsg{idx: idx, epg: newIdx, err: err})
}

func (m *App) selectSource(idx int) {
	m.activeIdx = idx
	m.inSeries = false
	m.mediaType = store.Live
	m.groupIdx = 0
	m.setStatus("", false)
	m.rebuildItems()
}

// rebuildItems recomputes visible channels + items from current state.
func (m *App) rebuildItems() {
	m.rebuildGroupEntries()
	m.rebuildChannelItems()
}

func (m *App) rebuildGroupEntries() {
	ld := m.activeLoaded()
	if ld == nil {
		m.groupEntries = nil
		m.groupIdx = 0
		items := []list.Item{}
		m.grpList.SetItems(items)
		return
	}
	var entries []groupEntry
	if m.inSeries {
		// Inside a series the groups pane becomes the season rail: All plus
		// one entry per season in watch order.
		entries = append(entries, groupEntry{label: "All", count: len(m.episodeChans), kind: groupKindAll})
		seen := map[string]bool{}
		for _, c := range m.episodeChans {
			g := seasonLabel(c)
			if seen[g] {
				continue
			}
			seen[g] = true
			n := 0
			for _, e := range m.episodeChans {
				if seasonLabel(e) == g {
					n++
				}
			}
			entries = append(entries, groupEntry{label: g, count: n, kind: groupKindSeason})
		}
	} else {
		channels := ld.Channels[m.mediaType]
		allCount := len(channels)
		if allCount == 0 {
			entries = append(entries, groupEntry{label: "All", count: 0, kind: groupKindAll})
		} else {
			entries = append(entries, groupEntry{label: "All", count: allCount, kind: groupKindAll})
		}
		favCount := 0
		for _, c := range channels {
			if m.cfg.IsFavorite(c.Key) {
				favCount++
			}
		}
		entries = append(entries, groupEntry{label: "Favorites", count: favCount, kind: groupKindFav})
		for _, g := range ld.Groups[m.mediaType] {
			n := 0
			for _, c := range channels {
				if c.Group == g {
					n++
				}
			}
			entries = append(entries, groupEntry{label: g, count: n, kind: groupKindGroup})
		}
	}
	m.groupEntries = entries
	if m.groupIdx >= len(entries) {
		m.groupIdx = 0
	}
	items := make([]list.Item, 0, len(entries))
	for _, e := range entries {
		items = append(items, groupItem{label: e.label, count: e.count, fav: e.kind == groupKindFav, labelW: m.grpLabelW})
	}
	prev := m.grpList.Index()
	if prev >= len(items) {
		prev = 0
	}
	m.grpList.SetItems(items)
	m.grpList.Select(prev)
}

func (m *App) rebuildChannelItems() {
	channels := m.currentChannels()
	m.visible = channels
	ld := m.activeLoaded()
	items := make([]list.Item, 0, len(channels))
	for _, c := range channels {
		ci := channelItem{ch: c, fav: m.cfg.IsFavorite(c.Key), num: c.Number}
		if ld != nil && ld.EPG != nil && c.Type == store.Live {
			tvgID := c.TvgID
			if tvgID == "" {
				tvgID = c.TvgName
			}
			if now, _ := epg.NowNext(ld.EPG, tvgID); now != nil {
				ci.now = now.Title
			}
		}
		items = append(items, ci)
	}
	prev := m.chList.Index()
	if prev >= len(items) {
		prev = 0
	}
	m.chList.ResetSelected()
	m.chList.SetItems(items)
	if len(items) > 0 {
		m.chList.Select(prev)
	}
}

func (m *App) currentChannels() []store.Channel {
	ld := m.activeLoaded()
	if ld == nil {
		return nil
	}
	if m.inSeries {
		out := m.episodeChans
		if m.searchFilter != "" {
			out = filterByName(out, m.searchFilter)
		}
		if m.groupIdx >= 0 && m.groupIdx < len(m.groupEntries) {
			if e := m.groupEntries[m.groupIdx]; e.kind == groupKindSeason {
				var f []store.Channel
				for _, c := range out {
					if seasonLabel(c) == e.label {
						f = append(f, c)
					}
				}
				out = f
			}
		}
		return out
	}
	wall := ld.Channels[m.mediaType]
	if m.searchFilter != "" {
		return filterByName(wall, m.searchFilter)
	}
	if m.groupIdx < 0 || m.groupIdx >= len(m.groupEntries) {
		return nil
	}
	e := m.groupEntries[m.groupIdx]
	switch e.kind {
	case groupKindAll:
		return wall
	case groupKindFav:
		var out []store.Channel
		for _, c := range wall {
			if m.cfg.IsFavorite(c.Key) {
				out = append(out, c)
			}
		}
		return out
	default:
		var out []store.Channel
		for _, c := range wall {
			if c.Group == e.label {
				out = append(out, c)
			}
		}
		return out
	}
}

func filterByName(ch []store.Channel, q string) []store.Channel {
	q = strings.ToLower(strings.TrimSpace(q))
	var out []store.Channel
	for _, c := range ch {
		if strings.Contains(strings.ToLower(c.Name), q) {
			out = append(out, c)
		}
	}
	return out
}

// ---------- status ----------

func (m *App) setStatus(s string, isErr bool) {
	m.status = s
	m.statusErr = isErr
}

func (m *App) setBusy(b bool, msg string) {
	m.busy = b
	m.busyMsg = msg
}
