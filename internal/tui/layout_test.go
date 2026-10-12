package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mertturunc/iptv-tui/internal/cast"
	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// ansiRE strips styling so rendered rows can be measured as plain text.
var ansiRE = regexp.MustCompile("\x1b\\[[0-9;]*m")

// colorName names a style's color for comparison. Lipgloss's unset color is a
// sentinel struct rather than a Color value, so anything that is not a Color is
// reported as no color at all.
func colorName(c lipgloss.TerminalColor) string {
	if col, ok := c.(lipgloss.Color); ok {
		return string(col)
	}
	return ""
}

// layoutApp returns an app holding one source with long group and channel
// names, which is what exposes panes that overflow or refuse to fill.
func layoutApp() *App {
	cfg := &store.Store{Cfg: store.DefaultConfig()}
	a := New(cfg)
	a.SetSend(noSend)
	ld := &store.Loaded{
		Source:   &store.Source{ID: "a", Name: "demo", Kind: "m3u"},
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	cfg.AddSource(*ld.Source)
	for i := 1; i <= 40; i++ {
		ld.Channels[store.Live] = append(ld.Channels[store.Live], store.Channel{
			Key:  fmt.Sprintf("k%d", i),
			Name: fmt.Sprintf("Channel Number %d With A Long Display Name", i),
		})
	}
	ld.Groups[store.Live] = []string{"Ungrouped", "News", "Kids", "Entertainment 4K HD"}
	a.loaded = []*store.Loaded{ld}
	a.selectSource(0)
	return a
}

// maxLineWidth reports the widest rendered line of a block.
func maxLineWidth(s string) int {
	w := 0
	for _, line := range strings.Split(s, "\n") {
		if lw := lipgloss.Width(line); lw > w {
			w = lw
		}
	}
	return w
}

// TestColumnSplitIsOneThirdTwoThirds pins the wide-terminal layout: groups get
// a third of the console width, channels the rest, and the rendered result
// fills the console exactly with no line overflowing into a wrap.
func TestColumnSplitIsOneThirdTwoThirds(t *testing.T) {
	for _, term := range []int{80, 100, 120, 160, 200, 240} {
		a := layoutApp()
		a.Update(tea.WindowSizeMsg{Width: term, Height: 40})
		if a.vertical {
			t.Fatalf("width %d: expected two columns, got stacked layout", term)
		}
		width := a.width

		// Both columns plus the 3-column rule between them must total the
		// console width exactly.
		if got := a.grpW + 3 + a.chW; got != width {
			t.Errorf("width %d: columns total %d (grpW=%d chW=%d), want %d",
				term, got, a.grpW, a.chW, width)
		}

		// Groups get a third of the width, allowing for the 22-column floor.
		want := width / 3
		if want < 22 {
			want = 22
		}
		if a.grpW != want {
			t.Errorf("width %d: grpW = %d, want %d", term, a.grpW, want)
		}
		if a.chW <= a.grpW {
			t.Errorf("width %d: channel pane (%d) must be wider than the group pane (%d)",
				term, a.chW, a.grpW)
		}

		// The rule has to sit at the boundary of the two columns, and the
		// channel pane must start after it.
		body := a.renderMain()
		sep := -1
		for _, line := range strings.Split(body, "\n") {
			if i := strings.Index(line, glRuleV); i >= 0 {
				sep = lipgloss.Width(line[:i])
				break
			}
		}
		if sep != a.grpW+1 {
			t.Errorf("width %d: separator at column %d, want %d", term, sep, a.grpW+1)
		}

		if got := maxLineWidth(body); got > width {
			t.Errorf("width %d: rendered a %d-column line, which wraps", term, got)
		}
	}
}

// TestViewFitsTheTerminal is the frame contract: the view must be exactly as
// tall as the terminal and no wider, or the terminal scrolls and the header
// scrolls away. Chrome rows (header, now-playing strip, status strip) are
// reserved whether or not they have anything to say.
func TestViewFitsTheTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{
		{80, 24}, {100, 30}, {120, 34}, {160, 40}, {200, 50}, {240, 60},
		{60, 20}, {50, 30}, {72, 28},
	}
	for _, s := range sizes {
		a := layoutApp()
		a.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
		view := a.View()

		if got := lipgloss.Height(view); got != s.h {
			t.Errorf("%dx%d: view is %d rows tall, want exactly %d", s.w, s.h, got, s.h)
		}
		if got := lipgloss.Width(view); got > s.w {
			t.Errorf("%dx%d: view is %d columns wide, wider than the terminal", s.w, s.h, got)
		}

		// The now-playing strip is furniture: it holds its row whether or not
		// mpv is running, so the rails never jump when playback starts.
		before := lipgloss.Height(a.View())
		a.playing = &player.Process{}
		a.playingCh = store.Channel{Name: "Channel Number 1 With A Long Display Name"}
		a.playPos, a.playDur = 42*time.Minute, 90*time.Minute
		after := a.View()
		if got := lipgloss.Height(after); got != s.h {
			t.Errorf("%dx%d: view with playback is %d rows tall, want %d", s.w, s.h, got, s.h)
		}
		if before != s.h {
			t.Errorf("%dx%d: idle view already %d rows, want %d", s.w, s.h, before, s.h)
		}

		// Every overlay is placed against the same body budget, so each one
		// must also land on a frame of exactly the terminal's height.
		for _, mode := range []mode{modeHelp, modeSources, modeAdd, modeEpg, modeMpv, modeCast} {
			a.mode = mode
			if mode == modeSources {
				a.openSources()
			}
			if mode == modeAdd {
				a.openAdd("m3u")
			}
			if mode == modeMpv {
				a.openMpvSetup()
			}
			if mode == modeCast {
				a.openCast()
			}
			a.epg.progs = []store.Program{{Title: "News at Ten"}}
			if got := lipgloss.Height(a.View()); got != s.h {
				t.Errorf("%dx%d: overlay mode %d renders %d rows, want %d", s.w, s.h, mode, got, s.h)
			}
			if got := lipgloss.Width(a.View()); got > s.w {
				t.Errorf("%dx%d: overlay mode %d is %d columns wide, wider than the terminal",
					s.w, s.h, mode, got)
			}
			a.mode = modeMain
		}
	}
}

// TestMarginInsetsTheConsole pins the quiet void: the console is the terminal
// minus one column each side once there is room, and flush when there is not.
func TestMarginInsetsTheConsole(t *testing.T) {
	cases := []struct{ w, h, margin int }{
		{200, 50, 1}, {120, 34, 1}, {100, 30, 1},
		{80, 24, 0}, {60, 20, 0}, {100, 24, 0},
	}
	for _, c := range cases {
		a := layoutApp()
		a.Update(tea.WindowSizeMsg{Width: c.w, Height: c.h})
		if a.margin != c.margin {
			t.Errorf("%dx%d: margin = %d, want %d", c.w, c.h, a.margin, c.margin)
		}
		if want := c.w - 2*c.margin; a.width != want {
			t.Errorf("%dx%d: console width = %d, want %d", c.w, c.h, a.width, want)
		}
		// The rendered frame must start with the void and reach the far edge.
		view := a.View()
		if got := lipgloss.Width(view); got != c.w {
			t.Errorf("%dx%d: rendered frame is %d columns, want %d", c.w, c.h, got, c.w)
		}
	}
}

// TestFocusMovesTheChrome checks the focus answer is visible: the focused
// rail's title carries the signal badge and its selection bar is lit, while
// the other rail drops to engraved title and unlit bar.
func TestFocusMovesTheChrome(t *testing.T) {
	a := layoutApp()
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	hue := string(a.signal())

	// Channels focused.
	a.focus = "channels"
	a.applyChrome()
	if got := colorName(a.chList.Styles.Title.GetBackground()); got != hue {
		t.Errorf("focused channel title background = %q, want the signal %s", got, hue)
	}
	if got := colorName(a.grpList.Styles.Title.GetBackground()); got != "" {
		t.Errorf("idle group title has background %q, want none", got)
	}

	// Groups focused: the badge moves across and nothing else changes.
	a.focus = "groups"
	a.applyChrome()
	if got := colorName(a.grpList.Styles.Title.GetBackground()); got != hue {
		t.Errorf("focused group title background = %q, want the signal %s", got, hue)
	}
	if got := colorName(a.chList.Styles.Title.GetBackground()); got != "" {
		t.Errorf("idle channel title has background %q, want none", got)
	}
}

// TestFocusSelectionBarStates checks both selection bars stay the same width --
// a focus change must never shift a column -- while their fill differs.
func TestFocusSelectionBarStates(t *testing.T) {
	lit := selectStyle(colSignal, true)
	unlit := selectStyle(colSignal, false)
	if lit.GetPaddingLeft() != unlit.GetPaddingLeft() || lit.GetPaddingRight() != unlit.GetPaddingRight() {
		t.Errorf("selection bars differ in padding: lit %d/%d, unlit %d/%d",
			lit.GetPaddingLeft(), lit.GetPaddingRight(),
			unlit.GetPaddingLeft(), unlit.GetPaddingRight())
	}
	if got := colorName(lit.GetBackground()); got != string(colSignal) {
		t.Errorf("lit bar background = %q, want the signal %s", got, colSignal)
	}
	if got := colorName(unlit.GetBackground()); got != string(colFace) {
		t.Errorf("unlit bar background = %q, want the unlit instrument face %s", got, colFace)
	}
}

// TestPlaybarReadouts covers the three states of the now-playing strip: at
// rest, live with no duration, and VOD with a duration to count down.
func TestPlaybarReadouts(t *testing.T) {
	a := layoutApp()
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})

	rest := ansiRE.ReplaceAllString(a.renderPlaybar(), "")
	if !strings.Contains(rest, "no signal") {
		t.Errorf("idle strip = %q, want a no-signal readout", rest)
	}
	if lipgloss.Height(a.renderPlaybar()) != playbarRows {
		t.Errorf("idle strip is %d rows, want %d", lipgloss.Height(a.renderPlaybar()), playbarRows)
	}

	a.playing = &player.Process{}
	a.playingCh = store.Channel{Name: "Some Movie"}

	// Live: no duration is reported, so there is nothing to measure and the
	// strip must not show a clock frozen at zero.
	a.playPos, a.playDur = 90*time.Second, 0
	live := ansiRE.ReplaceAllString(a.renderPlaybar(), "")
	if !strings.Contains(live, "Some Movie") {
		t.Errorf("live strip = %q, want the channel name", live)
	}
	for _, unwanted := range []string{glMeterOn, "1:30", "/"} {
		if strings.Contains(live, unwanted) {
			t.Errorf("live strip = %q, want no %q without a duration", live, unwanted)
		}
	}

	// VOD: meter plus tabular position, duration and remaining.
	a.playPos, a.playDur = 12*time.Minute+4*time.Second, 58*time.Minute
	vod := ansiRE.ReplaceAllString(a.renderPlaybar(), "")
	for _, want := range []string{"12:04", "58:00", "-45:56", glMeterOn} {
		if !strings.Contains(vod, want) {
			t.Errorf("VOD strip = %q, want it to contain %q", vod, want)
		}
	}
	// The readout is held against the right edge and the meter stays a gauge.
	if got := lipgloss.Width(strings.TrimRight(vod, " ")); got != a.width {
		t.Errorf("VOD readout ends at column %d, want the console edge %d", got, a.width)
	}
	if strings.Contains(vod, strings.Repeat(glMeterOn, meterMaxCells+1)) {
		t.Errorf("VOD strip meter exceeds the %d-cell cap", meterMaxCells)
	}

	// The meter tracks the position: empty at the start, full at the end.
	a.playPos = 0
	if got := ansiRE.ReplaceAllString(a.renderPlaybar(), ""); strings.Contains(got, glMeterOn) {
		t.Errorf("strip at position zero = %q, want an empty meter", got)
	}
	a.playPos = a.playDur
	if got := ansiRE.ReplaceAllString(a.renderPlaybar(), ""); strings.Contains(got, glMeterOff) {
		t.Errorf("strip at full position = %q, want a full meter", got)
	}

	// A series episode reads its SxxExx code, the same as the status line.
	a.playingCh = store.Channel{Name: "Pilot", Season: 2, Episode: 5}
	if got := playLabel(a.playingCh); got != "S02E05 Pilot" {
		t.Errorf("playLabel = %q, want %q", got, "S02E05 Pilot")
	}
}

func TestClock(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0:00"},
		{9 * time.Second, "0:09"},
		{90 * time.Second, "1:30"},
		{59*time.Minute + 59*time.Second, "59:59"},
		{time.Hour, "1:00:00"},
		{2*time.Hour + 3*time.Minute, "2:03:00"},
		{-time.Second, "0:00"},
	}
	for _, c := range cases {
		if got := clock(c.d); got != c.want {
			t.Errorf("clock(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestGroupPaneFillsItsColumn guards the padding in fitPane. Bubbles lists
// shrink to their longest line, so without padding the group column would
// render far narrower than its share on wide terminals.
func TestGroupPaneFillsItsColumn(t *testing.T) {
	a := layoutApp()
	a.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	body := a.renderMain()

	for _, line := range strings.Split(body, "\n") {
		i := strings.Index(line, glRuleV)
		if i < 0 {
			continue
		}
		// Spaces before the rule prove the group pane was padded out.
		if got := strings.TrimRight(line[:i], " "); got == "" {
			t.Fatal("group pane is blank")
		}
		short := lipgloss.Width(line[:i]) < a.grpW
		if short {
			t.Fatalf("line %q ends at column %d, short of the %d-column group pane",
				line, lipgloss.Width(line[:i]), a.grpW)
		}
		return
	}
	t.Fatal("no rule rendered in two-column layout")
}

// TestNarrowTerminalStacks checks the split does not leak into terminals too
// narrow for two columns.
func TestNarrowTerminalStacks(t *testing.T) {
	a := layoutApp()
	a.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	if !a.vertical {
		t.Fatal("expected stacked layout at width 50")
	}
	if a.grpW != a.width || a.chW != a.width {
		t.Errorf("stacked panes should span the width, got grpW=%d chW=%d width=%d",
			a.grpW, a.chW, a.width)
	}
	if got := maxLineWidth(a.renderMain()); got > a.width {
		t.Errorf("rendered a %d-column line in a %d-column console", got, a.width)
	}
}

// TestGroupCountColumnSurvives guards the label budget. Bubbles truncates item
// titles at the list width minus the delegate padding, so a label field one
// rune too wide silently clips the count off the end of every group row.
func TestGroupCountColumnSurvives(t *testing.T) {
	for _, width := range []int{80, 120, 160, 200} {
		a := layoutApp()
		a.Update(tea.WindowSizeMsg{Width: width, Height: 34})
		a.View() // layout() is what sizes the label field

		// The budget must account for exactly the row's fixed parts.
		usable := a.grpList.Width() - groupRowPad
		if got, want := a.grpLabelW+groupRowIndent+groupRowGap+groupCountW, usable; got != want {
			t.Errorf("width %d: label field %d + row overhead = %d, want %d",
				width, a.grpLabelW, got, want)
		}

		rows := 0
		// The first non-blank line is the pane title, not a group row.
		title := true
		for _, line := range strings.Split(a.grpList.View(), "\n") {
			plain := strings.TrimRight(ansiRE.ReplaceAllString(line, ""), " ")
			if plain == "" {
				continue
			}
			if title {
				title = false
				continue
			}
			rows++
			if got := lipgloss.Width(plain) > a.grpList.Width(); got {
				t.Errorf("width %d: group row %q overflows the %d-column pane",
					width, plain, a.grpList.Width())
			}
			// The count is the last field on the row, so a clipped budget shows
			// up as a truncated row rather than a dropped digit.
			if last := plain[len(plain)-1]; last < '0' || last > '9' {
				t.Errorf("width %d: group row %q ends in %q, expected the count",
					width, plain, string(last))
			}
		}
		if rows < 5 {
			t.Errorf("width %d: only %d group rows rendered", width, rows)
		}
	}
}

// firstNonASCII returns the first rune of s outside printable 7-bit ASCII.
func firstNonASCII(s string) (rune, bool) {
	for _, r := range s {
		if r > 126 {
			return r, true
		}
	}
	return 0, false
}

// bubblesDelegateEllipsis is the one mark ASCII mode cannot reach: bubbles
// hardcodes it inside list.DefaultDelegate's truncation and exposes no way to
// change it. U+2026 is a period wearing three dots and is in every font that
// ships a monospace face, so it is a safe exception. Naming it here means a
// genuinely new exotic glyph still fails this test instead of someone's console.
const bubblesDelegateEllipsis = '…'

// TestASCIIDropsEveryGlyph is the promise behind --ascii. A terminal picks its
// own font, so a glyph we cannot control is a glyph that may render as a tofu
// box; the guarantee is that with the switch on, nothing exotic reaches the
// screen. Checked two ways, because each catches a different mistake: the
// token table catches a token nobody happened to render this frame, and the
// rendered sweep catches a literal someone typed straight into a string.
func TestASCIIDropsEveryGlyph(t *testing.T) {
	SetASCII(true)
	defer func() { SetASCII(false) }()

	// Read the tokens after the switch, not before: capturing them first would
	// compare the Unicode defaults against themselves and pass for the wrong
	// reason.
	tokens := []struct {
		name string
		got  string
	}{
		{"glMark", glMark}, {"glPlay", glPlay}, {"glStar", glStar},
		{"glCursor", glCursor}, {"glSlot", glSlot}, {"glRuleH", glRuleH},
		{"glRuleV", glRuleV}, {"glMeterOn", glMeterOn}, {"glMeterOff", glMeterOff},
		{"glSep", glSep}, {"glEllipsis", glEllipsis}, {"glDash", glDash},
		{"glUpDown", glUpDown}, {"glLeft", glLeft}, {"glRight", glRight},
		{"glAngleOpen", glAngleOpen}, {"glAngleClose", glAngleClose},
		{"glBars", glBars},
	}

	for _, tk := range tokens {
		if r, bad := firstNonASCII(tk.got); bad {
			t.Errorf("ASCII mode: %s = %q (U+%04X)", tk.name, tk.got, r)
		}
	}
	// The ASCII mark is three columns wide where the Unicode one is one, and
	// ellipsize feeds fixed-width label fields, so the frame has to be re-earned.
	if w := lipgloss.Width(ellipsize("abcdefgh", 8)); w != 8 {
		t.Errorf("ellipsize(%q, 8) is %d columns wide in ASCII mode, want 8", "abcdefgh", w)
	}

	sizes := []struct{ w, h int }{{80, 24}, {120, 34}, {60, 20}}
	for _, s := range sizes {
		a := layoutApp()
		a.Update(tea.WindowSizeMsg{Width: s.w, Height: s.h})
		a.playing = &player.Process{}
		a.playingCh = store.Channel{Name: "Channel Number 1"}
		a.playPos, a.playDur = 42*time.Minute, 90*time.Minute

		for _, mode := range []mode{modeMain, modeHelp, modeSources, modeAdd, modeEpg, modeMpv, modeCast} {
			a.mode = mode
			if mode == modeSources {
				a.openSources()
			}
			if mode == modeAdd {
				a.openAdd("m3u")
			}
			if mode == modeMpv {
				a.openMpvSetup()
			}
			if mode == modeCast {
				a.openCast()
				a.onScanMsg(castScanMsg{devices: []cast.Device{{Name: "Toot", Addr: "192.168.1.182"}}, done: true})
				a.castForm.caster = &cast.Caster{}
				a.onStatMsg(castStatMsg{st: &cast.MediaStatus{PlayerState: "PLAYING", CurrentTime: 61.5, Duration: 3600, Rate: 1}, rtt: 18 * time.Millisecond, bytes: 1 << 20})
				a.onStatMsg(castStatMsg{st: &cast.MediaStatus{PlayerState: "BUFFERING", CurrentTime: 61.5, Duration: 3600, Rate: 1}, rtt: 22 * time.Millisecond, bytes: 3 << 20})
			}
			a.epg.progs = []store.Program{{Title: "News at Ten"}}

			view := a.View()
			for _, r := range ansiRE.ReplaceAllString(view, "") {
				if r <= 126 || r == bubblesDelegateEllipsis {
					continue
				}
				t.Errorf("%dx%d mode %d: U+%04X reached the screen in ASCII mode",
					s.w, s.h, mode, r)
			}
			if got := lipgloss.Height(view); got != s.h {
				t.Errorf("%dx%d mode %d: ASCII frame is %d rows, want %d", s.w, s.h, mode, got, s.h)
			}
			if got := lipgloss.Width(view); got > s.w {
				t.Errorf("%dx%d mode %d: ASCII frame is %d columns, wider than the terminal",
					s.w, s.h, mode, got)
			}
			a.mode = modeMain
		}
	}

	// And the default must still be the world we designed. If ASCII ever became
	// the only mode, this is the line that would notice.
	SetASCII(false)
	a := layoutApp()
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	if _, plain := firstNonASCII(ansiRE.ReplaceAllString(a.View(), "")); !plain {
		t.Error("default mode renders pure ASCII: the Headend glyphs are gone")
	}
}
