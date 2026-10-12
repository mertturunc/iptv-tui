package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mertturunc/iptv-tui/internal/store"
)

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func mediaLabel(t store.MediaType) string {
	switch t {
	case store.Movie:
		return "MOVIES"
	case store.Series:
		return "SERIES"
	default:
		return "LIVE"
	}
}

// Row budget of the fixed chrome. The header is nameplate + readout line +
// hairline rule; the status strip is key hints + status text; the now-playing
// strip sits between them and is always reserved so the rails never jump a
// row when playback starts.
const (
	headerRows  = 3
	footerRows  = 2
	playbarRows = 1
)

// footerH is the status strip plus the now-playing strip.
func (m *App) footerH() int { return footerRows + playbarRows }

// bodyH is the row budget between header and status strip. Every surface --
// the rails and each overlay -- is laid out against this one number so they
// all land on the same frame.
func (m *App) bodyH() int {
	h := m.height - headerRows - m.footerH()
	if h < 5 {
		h = 5
	}
	return h
}

func (m *App) layout() {
	if !m.ready || m.width <= 0 {
		return
	}
	body := m.bodyH()
	// Narrow terminals stack the panes vertically; wide ones get two columns.
	stacked := m.width < 60
	tall := m.height >= 40
	m.vertical = stacked || (tall && m.width < 80)

	switch {
	case m.vertical:
		m.grpW = m.width
		m.chW = m.width
		m.grpH = clamp(body*2/5, 4, body)
		m.chH = body - m.grpH
		m.grpList.SetSize(m.grpW-2, m.grpH)
		m.chList.SetSize(m.chW-2, m.chH)
	default:
		m.grpH, m.chH = body, body
		// Column split: groups get 1/3 of the width, channels the other 2/3.
		// The floor keeps group names readable on a narrow terminal; there is
		// deliberately no ceiling, so wide terminals follow the ratio.
		m.grpW = clamp(m.width/3, 22, m.width)
		// The remaining width is split by the 3-column rule between panes.
		m.chW = m.width - m.grpW - 3
		m.grpList.SetSize(m.grpW-2, body)
		m.chList.SetSize(m.chW-2, body)
	}
	m.syncGroupLabelWidth()
	// Focus: the active pane's title lights in the signal hue, the other pane
	// stays engraved dim. Titles: the channels pane gets the live readout,
	// groups a plain rack label.
	m.applyChrome()
	m.grpList.Title = "GROUPS"
	m.chList.Title = m.channelPaneTitle()
	m.grpList.SetShowHelp(false)
	m.chList.SetShowHelp(false)
	m.grpList.SetShowStatusBar(false)
	m.chList.SetShowStatusBar(false)
	if m.srcOverlay != nil {
		w := clamp(m.width-12, 40, m.width-2)
		h := clamp(m.bodyH()-6, 8, m.bodyH()-2)
		m.srcOverlay.SetSize(w, h)
	}
	if m.addForm != nil {
		m.addForm.Resize(m.width - 6)
	}
	if m.mpvForm != nil {
		m.mpvForm.Resize(m.width - 6)
	}
	if m.castForm != nil {
		m.castForm.Resize(m.width - 6)
	}
}

func (m *App) channelPaneTitle() string {
	if m.inSeries {
		t := "EPISODES " + glSep + " " + ellipsize(m.seriesTitle, 24)
		if m.groupIdx >= 0 && m.groupIdx < len(m.groupEntries) {
			if e := m.groupEntries[m.groupIdx]; e.kind == "season" {
				t += " " + glSep + " " + ellipsize(e.label, 12)
			}
		}
		return t
	}
	ld := m.activeLoaded()
	if ld == nil {
		return ""
	}
	label := ""
	if m.groupIdx >= 0 && m.groupIdx < len(m.groupEntries) {
		label = m.groupEntries[m.groupIdx].label
	}
	// CH▸ readout: the selected channel's number, tabular digits.
	chNum := 0
	if m.mediaType == store.Live && m.numBuf == "" {
		if it, ok := m.chList.SelectedItem().(channelItem); ok && it.num > 0 {
			chNum = it.num
		}
	}
	readout := ""
	if chNum > 0 {
		readout = "CH" + glCursor + fmt.Sprintf("%03d", chNum) + "  "
	} else if m.numBuf != "" {
		readout = "CH" + glCursor + m.numBuf + "_  "
	}
	label = strings.ToUpper(label)
	return readout + fmt.Sprintf("%s %s %s %s %d", mediaLabel(m.mediaType), glSep, label, glSep, len(m.visible))
}

func (m *App) chPaneWidth() int { return m.chW }

func (m *App) View() string {
	if !m.ready {
		return paintFrame(m.spinner.View() + " iptv-tui" + glEllipsis)
	}
	m.layout()
	body := m.renderMain()
	frame := m.bodyH()
	switch m.mode {
	case modeHelp:
		body = placeOverlay(helpView(m.width-16, frame-2), m.width, frame)
	case modeSources:
		if m.srcOverlay != nil {
			body = placeOverlay(m.srcOverlay.View(), m.width, frame)
		}
	case modeAdd:
		if m.addForm != nil {
			body = placeOverlay(m.addForm.View(m.width-8, frame-2), m.width, frame)
		}
	case modeEpg:
		if m.epg != nil {
			body = placeOverlay(m.epg.View(m.width-8, frame-4), m.width, frame)
		}
	case modeMpv:
		if m.mpvForm != nil {
			body = placeOverlay(m.mpvForm.View(m.width-8, frame-2), m.width, frame)
		}
	case modeCast:
		if m.castForm != nil {
			body = placeOverlay(m.castForm.View(m.width-8, frame-2), m.width, frame)
		}
	}
	out := lipgloss.JoinVertical(lipgloss.Top, m.renderHeader(), body, m.renderPlaybar(), m.renderFooter())
	if m.margin > 0 {
		// Quiet void: one column of ground down each side, so the console sits
		// in the room instead of bleeding into the terminal edge.
		out = lipgloss.NewStyle().Padding(0, m.margin).Render(out)
	}
	// Full-bleed frame: pad short lines out to the whole viewport in ground,
	// so no terminal-default gap survives at any edge. paintFrame then gives
	// every cell its explicit colors on top.
	full := lipgloss.NewStyle().
		Width(m.termW).
		Height(m.termH).
		Background(colGround).
		Render(out)
	// Every span below already chose its colors; paintFrame only fills the
	// cells nobody claimed so the palette switch is total.
	return paintFrame(full)
}

// sgrRe matches SGR color/style sequences; escRe any other escape, which
// paintFrame passes through without touching.
var (
	sgrRe = regexp.MustCompile("\x1b\\[([0-9;]*)m")
	escRe = regexp.MustCompile("\x1b\\][^\x07]*\x07|\x1b\\[[?0-9;]*[A-Za-z]")
)

// themeCodes renders the active ink/ground as SGR sequences for paintFrame,
// in whatever palette the terminal actually speaks. Ascii gets nothing (no
// color support, status quo); 16/256-color get nearest approximations so the
// switch is still total there.
func themeCodes() (fg, bg string) {
	prof := lipgloss.ColorProfile()
	if prof == termenv.Ascii {
		return "", ""
	}
	ink, ok1 := hexRGB(Themes[themeIdx].Ink)
	ground, ok2 := hexRGB(Themes[themeIdx].Ground)
	if !ok1 || !ok2 {
		return "", ""
	}
	switch prof {
	case termenv.ANSI256:
		return fmt.Sprintf("\x1b[38;5;%dm", nearest256(ink)),
			fmt.Sprintf("\x1b[48;5;%dm", nearest256(ground))
	case termenv.ANSI:
		fi, bi := nearest16(ink), nearest16(ground)
		fgCode := fmt.Sprintf("\x1b[%dm", 30+fi)
		bgCode := fmt.Sprintf("\x1b[%dm", 40+bi)
		if fi >= 8 {
			fgCode = fmt.Sprintf("\x1b[%dm", 90+fi-8)
		}
		if bi >= 8 {
			bgCode = fmt.Sprintf("\x1b[%dm", 100+bi-8)
		}
		return fgCode, bgCode
	default: // TrueColor and beyond
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", ink[0], ink[1], ink[2]),
			fmt.Sprintf("\x1b[48;2;%d;%d;%dm", ground[0], ground[1], ground[2])
	}
}

// ansi16 is the xterm 16-color table rungs for nearest-match lookups.
var ansi16 = [16][3]int{
	{0, 0, 0}, {205, 0, 0}, {0, 205, 0}, {205, 205, 0},
	{0, 0, 238}, {205, 0, 205}, {0, 205, 205}, {229, 229, 229},
	{127, 127, 127}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
	{92, 92, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
}

func dist2(a, b [3]int) int {
	dr, dg, db := a[0]-b[0], a[1]-b[1], a[2]-b[2]
	return dr*dr + dg*dg + db*db
}

func nearest16(c [3]int) int {
	best, bestD := 0, dist2(c, ansi16[0])
	for i := 1; i < 16; i++ {
		if d := dist2(c, ansi16[i]); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func xterm256(n int) [3]int {
	if n < 16 {
		return ansi16[n]
	}
	if n < 232 {
		n -= 16
		r, g, b := n/36, (n%36)/6, n%6
		comp := func(v int) int {
			if v == 0 {
				return 0
			}
			return 55 + 40*v
		}
		return [3]int{comp(r), comp(g), comp(b)}
	}
	g := 8 + 10*(n-232)
	return [3]int{g, g, g}
}

func nearest256(c [3]int) int {
	best, bestD := 0, dist2(c, xterm256(0))
	for i := 1; i < 256; i++ {
		if d := dist2(c, xterm256(i)); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func hexRGB(s string) ([3]int, bool) {
	var c [3]int
	if len(s) != 7 || s[0] != '#' {
		return c, false
	}
	for i := 0; i < 3; i++ {
		n, err := strconv.ParseUint(s[1+i*2:3+i*2], 16, 8)
		if err != nil {
			return c, false
		}
		c[i] = int(n)
	}
	return c, true
}

// paintFrame gives every cell explicit theme colors. Lipgloss and bubbles
// only emit codes where a style sets them, so bare gaps would fall back to
// the terminal's own colors and a light palette could never fully take over
// (nor a dark one on a light terminal). Walking the finished frame and
// filling unset fg/bg with ink/ground closes every gap, including bubbles
// internals no style hook can reach. The fill speaks the detected profile,
// so degraded terminals get consistent approximations instead of holes.
func paintFrame(s string) string {
	fgCode, bgCode := themeCodes()
	var b strings.Builder
	b.Grow(len(s) + 1024)
	fgSet, bgSet := false, false
	i := 0
	for i < len(s) {
		if s[i] == '\x1b' {
			if m := sgrRe.FindStringSubmatch(s[i:]); m != nil {
				trackSGR(m[1], &fgSet, &bgSet)
				b.WriteString(m[0])
				i += len(m[0])
				continue
			}
			if m := escRe.FindString(s[i:]); m != "" {
				b.WriteString(m)
				i += len(m)
				continue
			}
			b.WriteByte(s[i])
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] != '\x1b' {
			j++
		}
		run := s[i:j]
		if hasCell(run) {
			if !bgSet && bgCode != "" {
				b.WriteString(bgCode)
				bgSet = true
			}
			if hasVisible(run) && !fgSet && fgCode != "" {
				b.WriteString(fgCode)
				fgSet = true
			}
		}
		b.WriteString(run)
		i = j
	}
	return b.String()
}

// trackSGR folds one SGR parameter string into the fg/bg set flags.
func trackSGR(params string, fgSet, bgSet *bool) {
	if params == "" {
		*fgSet, *bgSet = false, false
		return
	}
	for _, p := range strings.Split(params, ";") {
		switch p {
		case "0":
			*fgSet, *bgSet = false, false
		case "39":
			*fgSet = false
		case "49":
			*bgSet = false
		case "38":
			*fgSet = true
		case "48":
			*bgSet = true
		default:
			n, err := strconv.Atoi(p)
			if err != nil {
				continue
			}
			switch {
			case n >= 30 && n <= 37 || n >= 90 && n <= 97:
				*fgSet = true
			case n >= 40 && n <= 47 || n >= 100 && n <= 107:
				*bgSet = true
			}
		}
	}
}

func hasCell(run string) bool {
	for _, r := range run {
		if r != '\n' {
			return true
		}
	}
	return false
}

func hasVisible(run string) bool {
	for _, r := range run {
		if r != '\n' && r != ' ' {
			return true
		}
	}
	return false
}

// placeOverlay centers an overlay in the body and holds it there. Overlays
// size themselves to their content, which is right while it fits and wrong
// once it does not: lipgloss.Place centers but never cuts, so a tall sheet
// would push the nameplate off the top of the terminal. Clipping here keeps the
// frame intact for every surface.
func placeOverlay(content string, width, height int) string {
	placed := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, content)
	return lipgloss.NewStyle().
		Width(width).
		Height(height).
		MaxWidth(width).
		MaxHeight(height).
		Render(placed)
}

func (m *App) renderHeader() string {
	hue := m.signal()
	var left []string
	// Nameplate: hairline-framed label with the signal mark on each side.
	nameplate := lipgloss.NewStyle().
		Bold(true).
		Foreground(colInk).
		Background(colFace).
		Padding(0, 1).
		Render(glMark + " IPTVTUI " + glMark)
	left = append(left, nameplate)
	hasActive := m.activeIdx >= 0 && m.activeIdx < len(m.cfg.Cfg.Sources)
	if hasActive {
		name := ellipsize(m.cfg.Cfg.Sources[m.activeIdx].Name, 18)
		chip := lipgloss.NewStyle().
			Bold(true).
			Foreground(colGround).
			Background(hue).
			Padding(0, 1).
			Render(name)
		left = append(left, chip)
	}
	if hasActive && m.cfg.Cfg.Sources[m.activeIdx].Kind.IsXtream() && m.width >= 64 {
		for _, t := range []store.MediaType{store.Live, store.Movie, store.Series} {
			if m.mediaType == t {
				left = append(left, sigBold(hue, " "+mediaLabel(t)+" "))
			} else {
				left = append(left, muted(" "+mediaLabel(t)+" "))
			}
		}
	}
	line1 := lipgloss.JoinHorizontal(lipgloss.Top, left...)
	padded := lipgloss.NewStyle().Width(m.width).Render(line1)
	line2 := m.renderHeaderLine2()
	// Hairline under the header: 1px rule across the full width.
	rule := headendRule.Width(m.width).Render(strings.Repeat(glRuleH, m.width))
	return lipgloss.JoinVertical(lipgloss.Top, padded, line2, rule)
}

func (m *App) renderHeaderLine2() string {
	hue := m.signal()
	var chips []string
	chips = append(chips, " focus: "+m.focus)
	if m.numBuf != "" {
		chips = append(chips, sig(hue, " #"+m.numBuf+"_"))
	}
	if m.searchFilter != "" {
		chips = append(chips, muted(" /"+ellipsize(m.searchFilter, 20)))
	}
	line := strings.Join(chips, "  ")
	return lipgloss.NewStyle().Width(m.width).Render(ellipsize(dim(line), m.width))
}

func (m *App) renderMain() string {
	if len(m.cfg.Cfg.Sources) == 0 {
		msg := "\n No sources yet.\n\n press a to add an M3U playlist or Xtream portal\n press ? for help\n "
		return panelBox(m.width, m.bodyH(), panelStyle.Bold(true).Render(msg))
	}
	ld := m.activeLoaded()
	if ld == nil {
		return panelBox(m.width, m.bodyH(), m.spinner.View()+" loading source"+glEllipsis)
	}
	if ld.LoadErr != nil && len(ld.Channels[store.Live]) == 0 && len(ld.Channels[store.Movie]) == 0 {
		return panelBox(m.width, m.bodyH(), failure("load error: "+ld.LoadErr.Error()))
	}
	grp := m.grpList.View()
	ch := m.chList.View()
	if m.vertical {
		return lipgloss.NewStyle().Width(m.width).Render(
			lipgloss.JoinVertical(lipgloss.Top, grp, ch))
	}
	// Pad both panes to their exact column width. A bubbles list shrinks to
	// its longest line, so without this the split chosen in layout() would
	// not be visible on screen -- the channel pane would start right after
	// the group text no matter how wide the terminal is.
	grp = fitPane(grp, m.grpW)
	ch = fitPane(ch, m.chW)
	// The rule between the rails carries the signal while the channel rail has
	// focus, so the boundary reads as part of the live side of the console.
	sep := paneRuleStyle(m.signal(), m.focus != "groups")
	body := lipgloss.JoinHorizontal(lipgloss.Top, grp, sep.Render(" "+glRuleV+" "), ch)
	return lipgloss.NewStyle().Width(m.width).Render(body)
}

// syncGroupLabelWidth recomputes how many runes a group label may use and
// re-stamps the rows when that budget changed.
func (m *App) syncGroupLabelWidth() {
	// The list's own width already excludes the pane padding added by the
	// SetSize call in layout; its rows then lose the delegate's padding, which
	// is what bubbles truncates item titles against.
	usable := m.grpList.Width() - groupRowPad
	// The row spends its indent and gap before the label and five columns on
	// the count after it.
	overhead := groupRowIndent + groupRowGap + groupCountW
	want := clamp(usable-overhead, 10, usable)
	if want == m.grpLabelW {
		return
	}
	m.grpLabelW = want

	items := m.grpList.Items()
	prev := m.grpList.Index()
	restamped := make([]list.Item, 0, len(items))
	for _, it := range items {
		g, ok := it.(groupItem)
		if !ok {
			restamped = append(restamped, it)
			continue
		}
		g.labelW = want
		restamped = append(restamped, g)
	}
	m.grpList.SetItems(restamped)
	if prev < len(restamped) {
		m.grpList.Select(prev)
	}
}

// fitPane stretches a pane to exactly w columns and clips anything wider.
func fitPane(s string, w int) string {
	return lipgloss.NewStyle().Width(w).MaxWidth(w).Render(s)
}

// ---------- key strip ----------

type keyHint struct{ key, label string }

func keyHintString(hints []keyHint) string {
	parts := make([]string, len(hints))
	for i, h := range hints {
		parts[i] = h.key + " " + h.label
	}
	return strings.Join(parts, " "+glSep+" ")
}

func (m *App) mainKeyHints() []keyHint {
	switch {
	case m.width < 60:
		return []keyHint{
			{glUpDown, "nav"}, {"enter", "play"}, {"/", "search"}, {"f", "fav"},
			{"s", "sources"}, {"?", "help"}, {"q", "quit"},
		}
	case m.width < 90:
		return []keyHint{
			{glUpDown, "nav"}, {"enter", "play"}, {"/", "search"}, {"f", "fav"},
			{"e", "epg"}, {"s", "sources"}, {"r", "reload"}, {"x", "stop"}, {"?", "help"}, {"q", "quit"},
		}
	case m.width < 120:
		return []keyHint{
			{glUpDown, "nav"}, {"enter", "play"}, {"/", "search"}, {"f", "fav"},
			{"e", "epg"}, {"s", "sources"}, {"a", "add"}, {"r", "reload"}, {"x", "stop"},
			{"1-3", "tabs"}, {"?", "help"}, {"q", "quit"},
		}
	default:
		return []keyHint{
			{glUpDown, "nav"}, {"enter", "play"}, {"/", "search"}, {"f", "fav"},
			{"e", "epg"}, {"s", "sources"}, {"a", "add"}, {"r", "reload"}, {"x", "stop"},
			{"1-3", "tabs"}, {"m", "keep-mpv"}, {"?", "help"}, {"q", "quit"},
		}
	}
}

func (m *App) keyHints() string { return keyHintString(m.mainKeyHints()) }

// styledHints renders the key strip with keys in the active signal and
// labels engraved dim, joined by hairline dots — a headend row of lamps.
// Hints that would overflow the terminal width are dropped, not wrapped.
func (m *App) styledHints(hints []keyHint) string {
	hue := m.signal()
	sep := muted(" " + glSep + " ")
	var parts []string
	w := 0
	for _, h := range hints {
		p := sigBold(hue, h.key) + " " + muted(h.label)
		add := lipgloss.Width(p)
		if len(parts) > 0 {
			add += lipgloss.Width(sep)
		}
		if w+add > m.width-2 {
			break
		}
		if len(parts) > 0 {
			parts = append(parts, sep)
		}
		parts = append(parts, p)
		w += add
	}
	return strings.Join(parts, "")
}

func (m *App) renderFooter() string {
	var hints []keyHint
	switch m.mode {
	case modeSearch:
		hints = []keyHint{{"type", "filter"}, {"enter", "done"}, {"esc", "clear"}}
	case modeAdd:
		hints = []keyHint{{"enter", "next/submit"}, {glUpDown, "move"}, {"k", "toggle m3u/xtream"}, {"esc", "cancel"}}
	case modeSources:
		hints = []keyHint{{"enter", "select"}, {"a", "add"}, {"r", "reload"}, {"d", "delete"}, {"esc", "back"}}
	case modeEpg:
		hints = []keyHint{{glUpDown, "scroll"}, {"esc", "back"}}
	case modeHelp:
		hints = []keyHint{{"esc", "back"}}
	case modeMpv:
		hints = []keyHint{{"enter", "save path"}, {"esc", "skip"}}
	case modeCast:
		hints = []keyHint{{"enter", "cast"}, {"up/down", "pick TV"}, {"r", "rescan"}, {"esc", "back (x stops TV)"}}
	default:
		hints = m.mainKeyHints()
	}
	line1 := lipgloss.NewStyle().Width(m.width).Render(m.styledHints(hints))
	status := m.status
	if m.statusErr {
		status = failure(status)
	} else if status != "" {
		status = muted(status)
	}
	if m.busy {
		status = sig(m.signal(), m.spinner.View()) + " " + m.busyMsg
	}
	line2 := lipgloss.NewStyle().Width(m.width).Render(ellipsize(status, m.width))
	return lipgloss.JoinVertical(lipgloss.Top, line1, line2)
}

// ---------- now playing strip ----------

// clock formats a duration as a readout: mm:ss, or h:mm:ss once it passes an
// hour, so the digits never mix scales.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Round(time.Second) / time.Second)
	h, mnt, sec := total/3600, (total%3600)/60, total%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, mnt, sec)
	}
	return fmt.Sprintf("%d:%02d", mnt, sec)
}

// playLabel names what mpv is playing, folding a series episode into its
// SxxExx code the way the rack labels channels.
func playLabel(ch store.Channel) string {
	if ch.Season > 0 {
		return fmt.Sprintf("S%02dE%02d %s", ch.Season, ch.Episode, ch.Name)
	}
	return ch.Name
}

// renderPlaybar is the always-reserved meter above the status strip. The row
// is furniture for the console whether or not anything is playing, so the
// rails never jump a row when playback starts; what changes is the readout.
//
// While mpv runs it carries the live marker, what is playing, and the position
// this app is built to resume from. VOD and series get a progress meter plus
// tabular position / duration / remaining. Live channels have no duration, so
// they get no readout rather than a meter that could never move. While a
// trackable mpv item reports nothing yet, the strip says it is still linking
// instead of sitting blank. At rest the row reads "no signal" and stays
// engraved.
func (m *App) renderPlaybar() string {
	if m.playing == nil && (m.castForm == nil || !m.castForm.casting) {
		return lipgloss.NewStyle().Width(m.width).Render(
			headendRule.Render(glPlay) + " " + muted("no signal"))
	}
	if m.playing == nil {
		// A cast outlives the terminal session: the strip names the TV and
		// what it is showing while no local player runs, with the receiver
		// state and measured bitrate when samples have arrived.
		left := success(glPlay) + " TV " + glSep + " " +
			ellipsize(m.castForm.title, clamp(m.width/3, 12, 30))
		var rp []string
		if s, ok := m.castForm.latest(); ok {
			rp = append(rp, s.state)
			if s.pos > 0 {
				rp = append(rp, clock(time.Duration(s.pos*float64(time.Second))))
			}
		}
		if mbps, ok := m.castForm.bitrate(); ok {
			rp = append(rp, fmt.Sprintf("%.1f Mbps", mbps))
		}
		rp = append(rp, m.castForm.target.Name)
		right := muted(strings.Join(rp, "  "))
		row := left
		if gap := m.width - lipgloss.Width(left) - lipgloss.Width(right); gap > 2 {
			row += strings.Repeat(" ", gap)
		}
		row += right
		return lipgloss.NewStyle().Width(m.width).Render(row)
	}
	hue := m.signal()
	// playPos/playDur are written only by the playback tick, so the readout has
	// one authority and never disagrees with itself between frames.
	pos, dur := m.playPos, m.playDur

	left := success(glPlay) + " " + ellipsize(playLabel(m.playingCh), clamp(m.width/3, 12, 30))

	// Tabular readout, held against the right edge like every other number in
	// this room. A stream that reports no duration has nothing to measure, so
	// it gets no readout rather than a clock stuck at zero.
	right := ""
	if dur > 0 {
		right = muted(clock(pos)) + muted("/") + muted(clock(dur))
		if rem := dur - pos; rem > 0 {
			right += muted("  -") + muted(clock(rem))
		}
	} else if pos == 0 && m.playingCh.IsPositionTrackable() &&
		m.playerName() == "mpv" && !m.playing.Connected() {
		// mpv should report here; silence means the IPC link is not up yet
		// (or never came up). VLC/MPC-HC have no link by design, and live
		// has nothing to measure, so neither may claim to be linking.
		right = muted("waiting for player" + glEllipsis)
	}

	row := left
	if right != "" {
		// The meter fills the gap between label and readout. It is capped so it
		// reads as a gauge instead of a rule across the whole console, and
		// dropped outright when there is no duration or no room to draw it.
		if gap := m.width - lipgloss.Width(left) - lipgloss.Width(right); gap > 2 {
			if w := clamp(gap-2, 0, meterMaxCells); dur > 0 && w >= 8 {
				row += "  " + m.progressMeter(w, float64(pos)/float64(dur), hue) +
					strings.Repeat(" ", gap-2-w)
			} else {
				row += strings.Repeat(" ", gap)
			}
		}
		row += right
	}
	return lipgloss.NewStyle().Width(m.width).Render(row)
}

// meterMaxCells caps the progress meter. A gauge wider than this stops reading
// as a measurement and starts reading as another rule in the frame.
const meterMaxCells = 36

// progressMeter draws w cells of signal-colored rule over dim rule: a patch
// cable filling up, not a progress bar bolted onto a list.
func (m *App) progressMeter(w int, frac float64, hue lipgloss.Color) string {
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(w) + 0.5)
	if filled > w {
		filled = w
	}
	return sigBold(hue, strings.Repeat(glMeterOn, filled)) +
		headendRule.Render(strings.Repeat(glMeterOff, w-filled))
}
