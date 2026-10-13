package tui

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mertturunc/iptv-tui/internal/airplay"
	"github.com/mertturunc/iptv-tui/internal/cast"
	"github.com/mertturunc/iptv-tui/internal/dbg"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// ---------- Send to Apple TV (AirPlay) ----------

// airForm is the "send it to the Apple TV" sheet: TVs found on the LAN plus
// a manual address field. It mirrors castForm on purpose: same target
// resolution (typed > picked > last), same proxy-unless-DASH stream path,
// same 2s stats loop. The address is remembered in settings after a good
// play. Differences from Chromecast: no app to launch (Play replaces), and
// the client is stateless HTTP, so there is no session to reset or reattach.
type airForm struct {
	input    textinput.Model
	width    int
	found    []airplay.Device
	sel      int // highlighted found TV, -1 when none
	scanning bool
	scanNote string
	lastAddr string
	// pending is the channel V was pressed on. Enter plays it even if the
	// focus has moved since, so the sheet never depends on which pane is lit.
	pending    store.Channel
	hasPending bool
	target     airplay.Device
	client     *airplay.Client
	proxy      *cast.Proxy
	direct     bool // TV pulls the origin URL itself (DASH or proxy refused)
	casting    bool
	title      string
	busy       bool
	// stats is the approach-B/C record: receiver state samples (latest
	// last) plus proxy byte counts for the bitrate derivation. The sample
	// type and cap are shared with the Chromecast sheet.
	samples  []castSample
	stalls   int
	baseWall time.Time
	basePos  float64
	baseSet  bool
	statErr  string
	// pollMu serializes status polls the way castForm's does: a tick that
	// arrives while the previous poll still runs just skips, so slow polls
	// (up to the 8s timeout) never stack behind the 2s tick.
	pollMu sync.Mutex
}

// airTickMsg re-arms while an AirPlay session lives, like castTickMsg does
// for Chromecast.
type airTickMsg time.Time

func airTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return airTickMsg(t) })
}

// airStatMsg carries one finished poll: receiver status plus the proxy's
// cumulative byte count at poll time.
type airStatMsg struct {
	st    *airplay.PlaybackInfo
	rtt   time.Duration
	bytes uint64
	err   error
}

func newAirForm(width int, lastAddr string) *airForm {
	if width < 24 {
		width = 24
	}
	f := &airForm{width: width, sel: -1, lastAddr: strings.TrimSpace(lastAddr)}
	f.input = newField("type an Apple TV address, or pick a found TV", width, true)
	f.input.Focus()
	return f
}

// Resize applies a new available width to the input field.
func (f *airForm) Resize(width int) {
	if width <= 0 {
		return
	}
	f.width = width
	if width-6 > 0 {
		f.input.Width = width - 6
	}
}

func (f *airForm) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

// View renders the sheet: found TVs, the address field, what the TV is
// playing (if anything), and one line on the limits of AirPlay.
func (f *airForm) View(width, height int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", panelStyle.Bold(true).Render("send to Apple TV (AirPlay)"))
	switch {
	case f.scanning && len(f.found) == 0:
		fmt.Fprintf(&b, "  %s\n", "looking for Apple TVs on your network"+glyphEllipsis)
	case len(f.found) == 0:
		fmt.Fprintf(&b, "  %s\n", dim("no TVs answered "+glyphDash+" type the address below"))
	default:
		for i, d := range f.found {
			marker := "  "
			if i == f.sel {
				marker = sig(colSignal, glyphCursor) + " "
			}
			fmt.Fprintf(&b, "  %s%s\n", marker, ellipsize(d.Name+" ("+d.Addr+")", width-6))
		}
	}
	fmt.Fprintf(&b, "\n  %s\n  %s\n", "address (when the TV is not listed):", f.input.View())
	if f.hasPending {
		fmt.Fprintf(&b, "\n  %s\n", ellipsize("for: "+playLabel(f.pending), width-4))
	} else {
		fmt.Fprintf(&b, "\n  %s\n", "no channel pinned "+glyphDash+" esc back, pick one, press V")
	}
	if f.target.Addr != "" {
		fmt.Fprintf(&b, "\n  %s\n", ellipsize("target: "+f.target.Name+" ("+f.target.Addr+")", width-4))
	}
	if f.casting {
		fmt.Fprintf(&b, "  %s\n", ellipsize("on TV: "+f.title, width-4))
		b.WriteString(f.statsView(width - 4))
	} else if f.scanNote != "" {
		fmt.Fprintf(&b, "  %s\n", ellipsize(f.scanNote, width-4))
	} else if f.lastAddr != "" {
		fmt.Fprintf(&b, "  %s\n", ellipsize("last TV: "+f.lastAddr, width-4))
	}
	if f.busy {
		fmt.Fprintf(&b, "\n  %s\n", "talking to TV"+glyphEllipsis)
	}
	fmt.Fprintf(&b, "\n  %s", dim("proxied: TV pulls through here, headers work, bitrate measured."))
	fmt.Fprintf(&b, "\n  %s", dim("direct (DASH): TV pulls the origin itself, no headers, no bitrate."))
	fmt.Fprintf(&b, "\n  %s", dim("the TV must allow everyone (no AirPlay password)."))
	content := b.String()
	_ = height
	return panelBox(width, lipgloss.Height(content)+2,
		content+"\n  "+dim("enter="+f.enterHint()+"  "+glyphArrowUpDown+"=pick  r=rescan  esc=back  (x stops TV)"))
}

// statsView renders the buffer dashboard: receiver state in text, then the
// state ribbon and bitrate sparkline graphs. Same honest labeling as the
// Chromecast sheet: the ribbon shows IDLE spells the receiver admits to,
// the sparkline shows proxy throughput, and drift shows wall-clock minus
// media-time since sampling began.
func (f *airForm) statsView(room int) string {
	s, ok := f.latest()
	if !ok {
		return "  " + dim("waiting for first stats"+glyphEllipsis) + "\n"
	}
	var b strings.Builder
	pos := clock(time.Duration(s.pos * float64(time.Second)))
	if s.dur > 0 {
		pos += "/" + clock(time.Duration(s.dur*float64(time.Second)))
	}
	fmt.Fprintf(&b, "  %s\n", ellipsize(fmt.Sprintf("state: %s   pos %s   rate %.1fx   poll %dms",
		s.state, pos, s.rate, int(s.rtt/time.Millisecond)), room))
	second := ""
	if mbps, ok := f.bitrate(); ok {
		second = fmt.Sprintf("%.1f Mbps (%s)", mbps, humanMB(s.bytes))
	} else if f.direct || f.proxy == nil {
		second = "direct cast: no bitrate"
	} else {
		second = "measuring bitrate" + glyphEllipsis
	}
	if d, ok := f.drift(); ok && s.state == "PLAYING" {
		second += fmt.Sprintf("   drift %+.1fs", d)
	}
	second += fmt.Sprintf("   stalls %d", f.stalls)
	if f.statErr != "" {
		second += "   " + f.statErr
	} else if rng := f.proxyRange(); rng != "" {
		second += "   range " + rng
	}
	fmt.Fprintf(&b, "  %s\n", ellipsize(second, room))
	if f.proxy != nil {
		pulls := f.proxy.Requests()
		via := fmt.Sprintf("via %s   TV pulls %d", f.proxy.BaseURL(), pulls)
		if pulls == 0 {
			via += "   " + "TV hasn't pulled yet " + glyphDash + " allow the firewall / check network"
		}
		fmt.Fprintf(&b, "  %s\n", ellipsize(via, room))
	}
	if r := f.stateRibbon(room); r != "" {
		fmt.Fprintf(&b, "  %s\n", r)
	}
	if g := f.bitrateSpark(room); g != "" {
		fmt.Fprintf(&b, "  %s\n", g)
	}
	return b.String()
}

// proxyRange reports the TV's latest Range pull, if any.
func (f *airForm) proxyRange() string {
	if f.proxy == nil {
		return ""
	}
	return f.proxy.LastRange()
}

// stateRibbon draws one cell per recent sample: played for PLAYING,
// alarmed for IDLE (the AirPlay equivalent of a stall spell), dim otherwise.
func (f *airForm) stateRibbon(room int) string {
	n := len(f.samples)
	if n == 0 {
		return ""
	}
	if room > 40 {
		room = 40
	}
	if n > room {
		n = room
	}
	var b strings.Builder
	for _, s := range f.samples[len(f.samples)-n:] {
		switch s.state {
		case "PLAYING":
			b.WriteString(sig(colSignal, glyphMeterFilled))
		case "IDLE":
			b.WriteString(failure("!"))
		default:
			b.WriteString(glyphMeterEmpty)
		}
	}
	return b.String()
}

// bitrateSpark draws per-interval proxy throughput, scaled to its own max.
// Direct casts have no byte counts, so they get no graph rather than a
// flat line that would read as a dead stream.
func (f *airForm) bitrateSpark(room int) string {
	bars := []rune(glyphSparkLevels)
	n := len(f.samples)
	if f.proxy == nil || n < 2 || len(bars) == 0 {
		return ""
	}
	if room > 40 {
		room = 40
	}
	// Per-interval rates between consecutive samples.
	rates := make([]float64, 0, n-1)
	mx := 0.0
	for i := 1; i < n; i++ {
		a, c := f.samples[i-1], f.samples[i]
		dt := c.at.Sub(a.at).Seconds()
		v := 0.0
		if dt > 0 && c.bytes >= a.bytes {
			v = float64(c.bytes-a.bytes) * 8 / dt / 1e6
		}
		rates = append(rates, v)
		if v > mx {
			mx = v
		}
	}
	if len(rates) > room {
		rates = rates[len(rates)-room:]
	}
	if mx <= 0 {
		return string(bars[0])
	}
	var b strings.Builder
	for _, v := range rates {
		idx := int(v/mx*float64(len(bars)-1) + 0.5)
		b.WriteString(string(bars[idx]))
	}
	return b.String()
}

// enterHint names what enter will play on, so a single found TV (where
// up/down visibly does nothing) still reads as already picked.
func (f *airForm) enterHint() string {
	if strings.TrimSpace(f.input.Value()) != "" {
		return "play on typed address"
	}
	if f.sel >= 0 && f.sel < len(f.found) {
		return "play on " + f.found[f.sel].Name
	}
	if f.lastAddr != "" {
		return "play on last TV"
	}
	return "play"
}

// airScanMsg reports found AirPlay receivers; done marks the end of the
// scan (interim updates stream in while scanning stays true).
type airScanMsg struct {
	devices []airplay.Device
	err     error
	done    bool
}

// airDoneMsg reports a finished AirPlay attempt for the selected channel.
// The live client travels with the message so the UI goroutine owns it
// outright: no open session is ever stranded in a background goroutine.
type airDoneMsg struct {
	client *airplay.Client
	proxy  *cast.Proxy
	direct bool
	addr   string
	name   string
	title  string
	err    error
}

// airStopMsg reports a finished stop-TV attempt.
type airStopMsg struct{ err error }

// openAirplay shows the send-to-Apple-TV sheet and starts a LAN scan behind
// it. The channel under the cursor is pinned to the sheet, so enter plays
// it no matter which pane holds focus afterwards.
func (m *App) openAirplay() {
	m.airForm = newAirForm(m.width-10, m.cfg.AirPlayTV())
	if ch, ok := m.selectedChannel(); ok {
		m.airForm.pending, m.airForm.hasPending = ch, true
	}
	m.mode = modeAirplay
	m.layout()
	m.startAirScan()
}

// startAirScan browses for AirPlay receivers, listing them as they answer
// (about twice a second) via airScanMsg. Manual entry stays available
// throughout.
func (m *App) startAirScan() {
	f := m.airForm
	if f == nil || f.scanning {
		return
	}
	f.scanning = true
	f.scanNote = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		var last []airplay.Device
		err := airplay.ScanProgress(ctx, 500*time.Millisecond, func(devs []airplay.Device) {
			last = devs
			m.send(airScanMsg{devices: devs})
		})
		if err != nil {
			m.send(airScanMsg{err: err, done: true})
			return
		}
		m.send(airScanMsg{devices: last, done: true})
	}()
}

func (m *App) onAirScan(v airScanMsg) {
	if m.airForm == nil {
		return
	}
	f := m.airForm
	if v.done {
		f.scanning = false
	}
	if v.err != nil {
		f.scanNote = "scan failed " + glyphDash + " type the address"
		return
	}
	if v.devices == nil {
		return
	}
	// Keep the highlight on the same TV across interim updates.
	keep := ""
	if f.sel >= 0 && f.sel < len(f.found) {
		keep = f.found[f.sel].Addr
	}
	f.found = v.devices
	if len(f.found) == 0 {
		if v.done {
			f.scanNote = "no TVs answered " + glyphDash + " type the address"
		}
		f.sel = -1
		return
	}
	f.sel = -1
	for i, d := range f.found {
		if d.Addr == keep {
			f.sel = i
			break
		}
	}
	if f.sel < 0 {
		f.sel = 0
		for i, d := range f.found {
			if d.Addr == f.lastAddr {
				f.sel = i
				break
			}
		}
	}
	f.scanNote = ""
}

// airChannel is what enter plays: the channel V was pressed on, or failing
// that whatever is selected now.
func (m *App) airChannel() (store.Channel, bool) {
	if m.airForm != nil && m.airForm.hasPending {
		return m.airForm.pending, true
	}
	return m.selectedChannel()
}

// airTarget resolves what enter plays on: the typed address first, then the
// picked TV, then the last good TV. It does no network itself; probing a
// typed address happens in the background goroutine.
func (m *App) airTarget() (addr string, dev airplay.Device, haveDev, ok bool) {
	if m.airForm == nil {
		return "", airplay.Device{}, false, false
	}
	f := m.airForm
	if a := strings.TrimSpace(f.input.Value()); a != "" {
		return a, airplay.Device{}, false, true
	}
	if f.sel >= 0 && f.sel < len(f.found) {
		return "", f.found[f.sel], true, true
	}
	if f.lastAddr != "" {
		return f.lastAddr, airplay.Device{}, false, true
	}
	if f.scanning {
		m.setStatus("still looking "+glyphDash+" wait for the list or type the address", true)
	} else {
		m.setStatus("pick a TV or type its address first", true)
	}
	return "", airplay.Device{}, false, false
}

// startAirplay dials the TV (reusing the live client when the address
// matches) and plays the selected channel. It runs off the UI goroutine
// and reports back with airDoneMsg.
func (m *App) startAirplay(ch store.Channel) {
	if m.airForm == nil {
		return
	}
	if m.airForm.busy {
		m.setStatus("talking to TV "+glyphDash+" wait a moment", false)
		dbg.Log("tui: airplay refused while busy")
		return
	}
	addr, dev, haveDev, ok := m.airTarget()
	if !ok {
		return
	}
	if haveDev {
		dbg.Log("tui: airplay %q -> picked %s (%s)", ch.Name, dev.Name, dev.Addr)
	} else {
		dbg.Log("tui: airplay %q -> typed %q", ch.Name, addr)
	}
	f := m.airForm
	f.busy = true
	m.setStatus("", false)
	// Proxy unless DASH: the byte counter behind the bitrate readout needs
	// the stream to flow through here, and providers that demand headers
	// need them injected server-side (AirPlay takes a bare URL, no
	// headers). Apple TVs play HLS natively, so the proxy only rewrites
	// when the origin needs it, exactly like Chromecast.
	playURL := ch.URL
	var px *cast.Proxy
	if cast.ShouldProxy(ch.URL) {
		tvHost := dev.Addr
		if tvHost == "" {
			tvHost = addr // typed address: resolve before probing
		}
		if p, err := cast.StartProxy(ch.URL, m.effectiveUA(ch), m.effectiveReferer(ch), tvHost); err == nil {
			playURL, px = p.StreamURL(), p
		}
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if !haveDev {
			dev = airplay.Probe(addr)
		}
		c := f.client
		if c == nil || c.Addr() != dev.Addr {
			var err error
			c, err = airplay.Dial(ctx, dev.Addr)
			if err != nil {
				m.send(airDoneMsg{proxy: px, err: err})
				return
			}
		}
		// AirPlay Play replaces whatever runs: no launch, no stop-first.
		if err := c.Play(ctx, playURL, 0); err != nil {
			m.send(airDoneMsg{client: c, proxy: px, err: err})
			return
		}
		m.send(airDoneMsg{client: c, proxy: px, direct: px == nil, addr: dev.Addr, name: dev.Name, title: playLabel(ch)})
	}()
}

// stopAirplay ends playback on the Apple TV. Refused while another
// operation owns the session.
func (m *App) stopAirplay() {
	f := m.airForm
	if f == nil || f.client == nil {
		m.setStatus("nothing playing", false)
		return
	}
	if f.busy {
		m.setStatus("talking to TV "+glyphDash+" wait a moment", false)
		dbg.Log("tui: airplay stop refused while busy")
		return
	}
	f.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := f.client.Stop(ctx)
		m.send(airStopMsg{err: err})
	}()
}

func (m *App) onAirDone(v airDoneMsg) tea.Cmd {
	if m.airForm == nil {
		if v.proxy != nil {
			v.proxy.Close()
		}
		return nil
	}
	m.airForm.busy = false
	if v.err != nil {
		if v.proxy != nil {
			v.proxy.Close()
		}
		m.airForm.client = nil
		m.airForm.proxy = nil
		m.airForm.casting = false
		dbg.Log("tui: airplay failed: %s", dbg.Scrub(v.err.Error()))
		m.setStatus("send to Apple TV failed: "+ellipsize(v.err.Error(), 80), true)
		return nil
	}
	if prev := m.airForm.proxy; prev != nil && prev != v.proxy {
		prev.Close()
	}
	m.airForm.client = v.client
	m.airForm.proxy = v.proxy
	m.airForm.direct = v.direct
	m.airForm.casting = true
	m.airForm.title = v.title
	m.airForm.target = airplay.Device{Name: v.name, Addr: v.addr}
	m.airForm.lastAddr = v.addr
	// A new program starts a new record: the drift baseline and stall count
	// belong to this play, not the previous channel.
	m.airForm.samples = nil
	m.airForm.stalls = 0
	m.airForm.baseSet = false
	m.airForm.statErr = ""
	m.cfg.SetAirPlayTV(v.addr)
	m.cfg.Save()
	dbg.Log("tui: airplay ok: %q on %s (%s) via %s", v.title, v.name, v.addr, viaDesc(v.proxy))
	m.setStatus(glyphPlaying+" on Apple TV ("+v.name+"): "+v.title, false)
	return airTick()
}

func (m *App) onAirStop(v airStopMsg) {
	if m.airForm == nil {
		return
	}
	m.airForm.busy = false
	m.airForm.client = nil
	if m.airForm.proxy != nil {
		m.airForm.proxy.Close()
		m.airForm.proxy = nil
	}
	m.airForm.casting = false
	if v.err != nil {
		dbg.Log("tui: airplay stop failed: %s", dbg.Scrub(v.err.Error()))
		m.setStatus("stop Apple TV failed: "+ellipsize(v.err.Error(), 80), true)
		return
	}
	dbg.Log("tui: airplay stop ok")
	m.setStatus("Apple TV stopped", false)
}

// pollAirStats asks the receiver for one status sample plus the proxy's
// cumulative bytes. AirPlay is stateless HTTP, so overlapping polls cannot
// desync a stream — but a tick that arrives while a play, a stop, or the
// previous poll is in flight still skips, via the busy flag and pollMu.
func (m *App) pollAirStats() {
	f := m.airForm
	if f == nil || f.client == nil {
		return
	}
	if !f.pollMu.TryLock() {
		return
	}
	go func() {
		defer f.pollMu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		t0 := time.Now()
		st, _, err := f.client.Status(ctx)
		var b uint64
		if f.proxy != nil {
			b = f.proxy.Bytes()
		}
		m.send(airStatMsg{st: st, rtt: time.Since(t0), bytes: b, err: err})
	}()
}

func (m *App) onAirStat(v airStatMsg) {
	f := m.airForm
	if f == nil || f.client == nil {
		return // stale tick from a stopped session
	}
	if v.err != nil || v.st == nil {
		msg := "unknown"
		if v.err != nil {
			msg = v.err.Error()
		}
		dbg.Log("tui: airplay stat poll failed: %s", dbg.Scrub(msg))
		f.statErr = "stats: " + ellipsize(msg, 60)
		return
	}
	f.statErr = ""
	s := castSample{
		at: time.Now(), state: airplay.ShortState(v.st.State),
		pos: v.st.Position, dur: v.st.Duration, rate: v.st.Rate,
		rtt: v.rtt, bytes: v.bytes,
	}
	if n := len(f.samples); n > 0 && s.state == "IDLE" && f.samples[n-1].state != "IDLE" {
		f.stalls++
	}
	if !f.baseSet {
		f.baseWall, f.basePos, f.baseSet = s.at, s.pos, true
	}
	f.samples = append(f.samples, s)
	if len(f.samples) > castStatCap {
		f.samples = f.samples[len(f.samples)-castStatCap:]
	}
}

// latest returns the newest sample, if any.
func (f *airForm) latest() (castSample, bool) {
	if f == nil || len(f.samples) == 0 {
		return castSample{}, false
	}
	return f.samples[len(f.samples)-1], true
}

// drift is wall-clock elapsed minus media-time elapsed since sampling began:
// positive means the picture is falling behind real time.
func (f *airForm) drift() (float64, bool) {
	s, ok := f.latest()
	if !ok || !f.baseSet {
		return 0, false
	}
	return s.at.Sub(f.baseWall).Seconds() - (s.pos - f.basePos), true
}

// bitrate derives Mbps from the last two proxied samples. Direct casts have
// no byte counts — the TV pulls the origin itself — so there is nothing to
// derive from and ok is false.
func (f *airForm) bitrate() (float64, bool) {
	n := len(f.samples)
	if f == nil || f.proxy == nil || n < 2 {
		return 0, false
	}
	a, b := f.samples[n-2], f.samples[n-1]
	dt := b.at.Sub(a.at).Seconds()
	if dt <= 0 || b.bytes < a.bytes {
		return 0, false
	}
	return float64(b.bytes-a.bytes) * 8 / dt / 1e6, true
}

// tvCasting reports whether any TV session (Chromecast or AirPlay) is live.
// tvProgram, tvLatest, and tvBitrate read the newest one: AirPlay wins when
// both run, since starting it last means it owns the room.
func (m *App) tvCasting() bool {
	return (m.castForm != nil && m.castForm.casting) ||
		(m.airForm != nil && m.airForm.casting)
}

func (m *App) tvProgram() (title, name string) {
	if m.airForm != nil && m.airForm.casting {
		return m.airForm.title, m.airForm.target.Name
	}
	return m.castForm.title, m.castForm.target.Name
}

func (m *App) tvLatest() (castSample, bool) {
	if m.airForm != nil && m.airForm.casting {
		return m.airForm.latest()
	}
	return m.castForm.latest()
}

func (m *App) tvBitrate() (float64, bool) {
	if m.airForm != nil && m.airForm.casting {
		return m.airForm.bitrate()
	}
	return m.castForm.bitrate()
}

// airStep drives the send-to-Apple-TV sheet. Enter plays the pinned channel
// on the typed address, the picked TV, or the last TV in that order;
// up/down pick a found TV; r rescans while the address field is empty (so
// typing a hostname keeps its letters); esc goes back. Stopping is x in
// main mode — kept out of the sheet so every key stays typeable. The TV
// keeps playing after esc, like keep-mpv-on-quit.
func (m *App) airStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		return m, nil
	case "enter":
		ch, ok := m.airChannel()
		if !ok {
			m.setStatus("no channel pinned "+glyphDash+" esc back, pick one, press v", true)
			return m, nil
		}
		m.startAirplay(ch)
		return m, nil
	case "r", "R":
		if m.airForm != nil && strings.TrimSpace(m.airForm.input.Value()) == "" {
			m.startAirScan()
			return m, nil
		}
		return m, m.airForm.Update(msg)
	case "up", "down", "k", "j":
		if m.airForm != nil && len(m.airForm.found) > 0 {
			if msg.String() == "up" || msg.String() == "k" {
				m.airForm.sel = (m.airForm.sel - 1 + len(m.airForm.found)) % len(m.airForm.found)
			} else {
				m.airForm.sel = (m.airForm.sel + 1) % len(m.airForm.found)
			}
		}
		return m, nil
	}
	return m, m.airForm.Update(msg)
}
