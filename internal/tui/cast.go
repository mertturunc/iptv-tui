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

	"github.com/mertturunc/iptv-tui/internal/cast"
	"github.com/mertturunc/iptv-tui/internal/dbg"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// ---------- Send to TV ----------

// castForm is the "send it to TV" sheet: TVs found on the LAN plus a manual
// address field. The address is remembered in settings after a good cast.
type castForm struct {
	input    textinput.Model
	width    int
	found    []cast.Device
	sel      int // highlighted found TV, -1 when none
	scanning bool
	scanNote string
	lastAddr string
	// pending is the channel C was pressed on. Enter casts it even if the
	// focus has moved since, so the sheet never depends on which pane is lit.
	pending    store.Channel
	hasPending bool
	target     cast.Device
	caster     *cast.Caster
	proxy      *cast.Proxy
	direct     bool // TV pulls the origin URL itself (DASH or proxy refused)
	casting    bool
	title      string
	busy       bool
	// stats is the approach-B/C record: receiver state samples (latest
	//last) plus proxy byte counts for the bitrate derivation.
	samples  []castSample
	stalls   int
	baseWall time.Time
	basePos  float64
	baseSet  bool
	statErr  string
	// pollMu serializes status polls: two readers splitting one TLS stream
	// desyncs the framing for good (the "bad frame size" mystery), so a
	// tick that arrives while the previous poll still runs just skips.
	pollMu sync.Mutex
}

// castSample is one 2s observation: receiver state plus cumulative proxy
// bytes at that moment.
type castSample struct {
	at    time.Time
	state string
	pos   float64
	dur   float64
	rate  float64
	rtt   time.Duration
	bytes uint64
}

// castStatCap bounds the dashboard history: a minute at the 2s tick.
const castStatCap = 30

// castTickMsg re-arms while a cast session lives, like playTickMsg does for
// local playback.
type castTickMsg time.Time

func castTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(t time.Time) tea.Msg { return castTickMsg(t) })
}

// castStatMsg carries one finished poll: receiver status plus the proxy's
// cumulative byte count at poll time.
type castStatMsg struct {
	st    *cast.MediaStatus
	rtt   time.Duration
	bytes uint64
	err   error
}

func newCastForm(width int, lastAddr string) *castForm {
	if width < 24 {
		width = 24
	}
	f := &castForm{width: width, sel: -1, lastAddr: strings.TrimSpace(lastAddr)}
	f.input = newField("type a TV address, or pick a found TV", width, true)
	f.input.Focus()
	return f
}

// Resize applies a new available width to the input field.
func (f *castForm) Resize(width int) {
	if width <= 0 {
		return
	}
	f.width = width
	if width-6 > 0 {
		f.input.Width = width - 6
	}
}

func (f *castForm) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

// View renders the sheet: found TVs, the address field, what the TV is
// playing (if anything), and one line on the limits of casting.
func (f *castForm) View(width, height int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", panelStyle.Bold(true).Render("send to TV"))
	switch {
	case f.scanning && len(f.found) == 0:
		fmt.Fprintf(&b, "  %s\n", "looking for TVs on your network"+glEllipsis)
	case len(f.found) == 0:
		fmt.Fprintf(&b, "  %s\n", dim("no TVs answered "+glDash+" type the address below"))
	default:
		for i, d := range f.found {
			marker := "  "
			if i == f.sel {
				marker = sig(colSignal, glCursor) + " "
			}
			fmt.Fprintf(&b, "  %s%s\n", marker, ellipsize(d.Name+" ("+d.Addr+")", width-6))
		}
	}
	fmt.Fprintf(&b, "\n  %s\n  %s\n", "address (when the TV is not listed):", f.input.View())
	if f.hasPending {
		fmt.Fprintf(&b, "\n  %s\n", ellipsize("for: "+playLabel(f.pending), width-4))
	} else {
		fmt.Fprintf(&b, "\n  %s\n", "no channel pinned "+glDash+" esc back, pick one, press C")
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
		fmt.Fprintf(&b, "\n  %s\n", "talking to TV"+glEllipsis)
	}
	fmt.Fprintf(&b, "\n  %s", dim("proxied: TV pulls through here, headers work, bitrate measured."))
	fmt.Fprintf(&b, "\n  %s", dim("direct (DASH): TV pulls the origin itself, no headers, no bitrate."))
	content := b.String()
	_ = height
	return panelBox(width, lipgloss.Height(content)+2,
		content+"\n  "+dim("enter="+f.enterHint()+"  "+glUpDown+"=pick  r=rescan  esc=back  (x stops TV)"))
}

// statsView renders the buffer dashboard: receiver state in text, then the
// state ribbon and bitrate sparkline graphs. Honest labeling throughout:
// the ribbon shows BUFFERING spells the receiver admits to, the sparkline
// shows proxy throughput (approach A), and drift shows wall-clock minus
// media-time since sampling began.
func (f *castForm) statsView(room int) string {
	s, ok := f.latest()
	if !ok {
		return "  " + dim("waiting for first stats"+glEllipsis) + "\n"
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
		second = "measuring bitrate" + glEllipsis
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
			via += "   " + "TV hasn't pulled yet " + glDash + " allow the firewall / check network"
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

// viaDesc names the stream path for logs: the proxy base or direct.
func viaDesc(px *cast.Proxy) string {
	if px == nil {
		return "direct"
	}
	return "proxy " + px.BaseURL()
}

// proxyRange reports the TV's latest Range pull, if any.
func (f *castForm) proxyRange() string {
	if f.proxy == nil {
		return ""
	}
	return f.proxy.LastRange()
}

// stateRibbon draws one cell per recent sample: played for PLAYING,
// alarmed for BUFFERING, dim otherwise.
func (f *castForm) stateRibbon(room int) string {
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
			b.WriteString(sig(colSignal, glMeterOn))
		case "BUFFERING":
			b.WriteString(failure("!"))
		default:
			b.WriteString(glMeterOff)
		}
	}
	return b.String()
}

// bitrateSpark draws per-interval proxy throughput, scaled to its own max.
// Direct casts have no byte counts, so they get no graph rather than a
// flat line that would read as a dead stream.
func (f *castForm) bitrateSpark(room int) string {
	bars := []rune(glBars)
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

// enterHint names what enter will cast to, so a single found TV (where// up/down visibly does nothing) still reads as already picked.
func (f *castForm) enterHint() string {
	if strings.TrimSpace(f.input.Value()) != "" {
		return "cast typed address"
	}
	if f.sel >= 0 && f.sel < len(f.found) {
		return "cast to " + f.found[f.sel].Name
	}
	if f.lastAddr != "" {
		return "cast last TV"
	}
	return "cast"
}

// castScanMsg reports found Chromecast receivers; done marks the end of the
// scan (interim updates stream in while scanning stays true).
type castScanMsg struct {
	devices []cast.Device
	err     error
	done    bool
}

// castDoneMsg reports a finished cast attempt for the selected channel. The
// live session travels with the message so the UI goroutine owns it outright:
// no open connection is ever stranded in a background goroutine.
type castDoneMsg struct {
	caster *cast.Caster
	proxy  *cast.Proxy
	direct bool
	addr   string
	name   string
	title  string
	err    error
}

// castStopMsg reports a finished stop-TV attempt.
type castStopMsg struct{ err error }

// openCast shows the send-to-TV sheet and starts a LAN scan behind it. The
// channel under the cursor is pinned to the sheet, so enter casts it no
// matter which pane holds focus afterwards.
func (m *App) openCast() {
	m.castForm = newCastForm(m.width-10, m.cfg.Cfg.Settings.CastTV)
	if ch, ok := m.selectedChannel(); ok {
		m.castForm.pending, m.castForm.hasPending = ch, true
	}
	m.mode = modeCast
	m.layout()
	m.startScan()
}

// startScan browses for Chromecast receivers, listing them as they answer
// (about twice a second) via castScanMsg. Manual entry stays available
// throughout.
func (m *App) startScan() {
	f := m.castForm
	if f == nil || f.scanning {
		return
	}
	f.scanning = true
	f.scanNote = ""
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		var last []cast.Device
		err := cast.ScanProgress(ctx, 500*time.Millisecond, func(devs []cast.Device) {
			last = devs
			m.send(castScanMsg{devices: devs})
		})
		if err != nil {
			m.send(castScanMsg{err: err, done: true})
			return
		}
		m.send(castScanMsg{devices: last, done: true})
	}()
}

func (m *App) onScanMsg(v castScanMsg) {
	if m.castForm == nil {
		return
	}
	f := m.castForm
	if v.done {
		f.scanning = false
	}
	if v.err != nil {
		f.scanNote = "scan failed " + glDash + " type the address"
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
			f.scanNote = "no TVs answered " + glDash + " type the address"
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

// castChannel is what enter casts: the channel C was pressed on, or failing
// that whatever is selected now.
func (m *App) castChannel() (store.Channel, bool) {
	if m.castForm != nil && m.castForm.hasPending {
		return m.castForm.pending, true
	}
	return m.selectedChannel()
}

// castTarget resolves what enter casts to: the typed address first, then the
// picked TV, then the last good TV. It does no network itself; probing a
// typed address happens in the background goroutine.
func (m *App) castTarget() (addr string, dev cast.Device, haveDev, ok bool) {
	if m.castForm == nil {
		return "", cast.Device{}, false, false
	}
	f := m.castForm
	if a := strings.TrimSpace(f.input.Value()); a != "" {
		return a, cast.Device{}, false, true
	}
	if f.sel >= 0 && f.sel < len(f.found) {
		return "", f.found[f.sel], true, true
	}
	if f.lastAddr != "" {
		return f.lastAddr, cast.Device{}, false, true
	}
	if f.scanning {
		m.setStatus("still looking "+glDash+" wait for the list or type the address", true)
	} else {
		m.setStatus("pick a TV or type its address first", true)
	}
	return "", cast.Device{}, false, false
}

// startCast dials the TV (reusing the live session when the address matches),
// launches the receiver, and loads the selected channel. It runs off the UI
// goroutine and reports back with castDoneMsg.
func (m *App) startCast(ch store.Channel) {
	if m.castForm == nil {
		return
	}
	if m.castForm.busy {
		m.setStatus("talking to TV "+glDash+" wait a moment", false)
		dbg.Log("tui: cast refused while busy")
		return
	}
	addr, dev, haveDev, ok := m.castTarget()
	if !ok {
		return
	}
	if haveDev {
		dbg.Log("tui: cast %q -> picked %s (%s)", ch.Name, dev.Name, dev.Addr)
	} else {
		dbg.Log("tui: cast %q -> typed %q", ch.Name, addr)
	}
	f := m.castForm
	f.busy = true
	m.setStatus("", false)
	// Proxy unless DASH: the byte counter behind the bitrate readout needs
	// the stream to flow through here, and providers that demand headers
	// need them injected server-side. The mime still comes from the origin
	// URL, which carries the extension the proxy URL lacks.
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
	mime := cast.GuessContentType(ch.URL)
	streamType := cast.GuessStreamType(ch.Type == store.Live)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if !haveDev {
			dev = cast.Probe(addr)
		}
		c := f.caster
		if c == nil || c.Addr() != dev.Addr {
			if c != nil {
				c.Close()
			}
			var err error
			c, err = cast.Dial(ctx, dev.Addr)
			if err != nil {
				m.send(castDoneMsg{proxy: px, err: err})
				return
			}
			if err := c.Launch(ctx); err != nil {
				c.Close()
				m.send(castDoneMsg{proxy: px, err: err})
				return
			}
		} else {
			// Same TV, live connection: reset the running app first. A
			// wedged receiver (stuck session from an unloadable stream)
			// survives a bare LAUNCH — the TV just reattaches to the
			// corpse — so STOP it before launching fresh. A failed stop
			// is fine; Launch below starts clean anyway.
			if err := c.StopApp(ctx); err != nil {
				dbg.Log("cast: pre-cast stop: %s", dbg.Scrub(err.Error()))
			}
			if err := c.Launch(ctx); err != nil {
				c.Close()
				m.send(castDoneMsg{proxy: px, err: err})
				return
			}
		}
		if err := c.Load(ctx, playURL, ch.Name, mime, streamType); err != nil {
			m.send(castDoneMsg{caster: c, proxy: px, err: err})
			return
		}
		m.send(castDoneMsg{caster: c, proxy: px, direct: px == nil, addr: dev.Addr, name: dev.Name, title: playLabel(ch)})
	}()
}

// stopCast ends the receiver session on the TV. Refused while another
// operation owns the session: two speakers on one channel cross responses
// and race Close against send, which is the nil-pointer panic this guards.
func (m *App) stopCast() {
	f := m.castForm
	if f == nil || f.caster == nil {
		m.setStatus("nothing casting", false)
		return
	}
	if f.busy {
		m.setStatus("talking to TV "+glDash+" wait a moment", false)
		dbg.Log("tui: stop refused while busy")
		return
	}
	f.busy = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		c := f.caster
		err := c.StopApp(ctx)
		c.Close()
		m.send(castStopMsg{err: err})
	}()
}

func (m *App) onCastDone(v castDoneMsg) tea.Cmd {
	if m.castForm == nil {
		if v.caster != nil {
			v.caster.Close()
		}
		if v.proxy != nil {
			v.proxy.Close()
		}
		return nil
	}
	m.castForm.busy = false
	if v.err != nil {
		if v.caster != nil {
			v.caster.Close()
		}
		if v.proxy != nil {
			v.proxy.Close()
		}
		m.castForm.caster = nil
		m.castForm.proxy = nil
		m.castForm.casting = false
		dbg.Log("tui: cast failed: %s", dbg.Scrub(v.err.Error()))
		m.setStatus("send to TV failed: "+ellipsize(v.err.Error(), 80), true)
		return nil
	}
	if prev := m.castForm.caster; prev != nil && prev != v.caster {
		prev.Close()
	}
	if prev := m.castForm.proxy; prev != nil && prev != v.proxy {
		prev.Close()
	}
	m.castForm.caster = v.caster
	m.castForm.proxy = v.proxy
	m.castForm.direct = v.direct
	m.castForm.casting = true
	m.castForm.title = v.title
	m.castForm.target = cast.Device{Name: v.name, Addr: v.addr}
	m.castForm.lastAddr = v.addr
	// A new program starts a new record: the drift baseline and stall count
	// belong to this cast, not the previous channel.
	m.castForm.samples = nil
	m.castForm.stalls = 0
	m.castForm.baseSet = false
	m.castForm.statErr = ""
	m.cfg.Cfg.Settings.CastTV = v.addr
	m.cfg.Save()
	dbg.Log("tui: cast ok: %q on %s (%s) via %s", v.title, v.name, v.addr, viaDesc(v.proxy))
	m.setStatus(glPlay+" on TV ("+v.name+"): "+v.title, false)
	return castTick()
}

func (m *App) onCastStop(v castStopMsg) {
	if m.castForm == nil {
		return
	}
	m.castForm.busy = false
	m.castForm.caster = nil
	if m.castForm.proxy != nil {
		m.castForm.proxy.Close()
		m.castForm.proxy = nil
	}
	m.castForm.casting = false
	if v.err != nil {
		dbg.Log("tui: stop failed: %s", dbg.Scrub(v.err.Error()))
		m.setStatus("stop TV failed: "+ellipsize(v.err.Error(), 80), true)
		return
	}
	dbg.Log("tui: stop ok")
	m.setStatus("TV stopped", false)
}

// pollCastStats asks the receiver for one status sample plus the proxy's
// cumulative bytes. Serialized against other receiver traffic by the busy
// flag, and against itself by pollMu: the tick skips while a cast, a stop,
// or the previous poll is in flight.
func (m *App) pollCastStats() {
	f := m.castForm
	if f == nil || f.caster == nil {
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
		st, _, err := f.caster.Status(ctx)
		var b uint64
		if f.proxy != nil {
			b = f.proxy.Bytes()
		}
		m.send(castStatMsg{st: st, rtt: time.Since(t0), bytes: b, err: err})
	}()
}

func (m *App) onStatMsg(v castStatMsg) {
	f := m.castForm
	if f == nil || f.caster == nil {
		return // stale tick from a stopped cast
	}
	if v.err != nil || v.st == nil {
		msg := "unknown"
		if v.err != nil {
			msg = v.err.Error()
		}
		dbg.Log("tui: stat poll failed: %s", dbg.Scrub(msg))
		f.statErr = "stats: " + ellipsize(msg, 60)
		return
	}
	f.statErr = ""
	s := castSample{
		at: time.Now(), state: cast.ShortState(v.st.PlayerState),
		pos: v.st.CurrentTime, dur: v.st.Duration, rate: v.st.Rate,
		rtt: v.rtt, bytes: v.bytes,
	}
	if n := len(f.samples); n > 0 && s.state == "BUFFERING" && f.samples[n-1].state != "BUFFERING" {
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
func (f *castForm) latest() (castSample, bool) {
	if f == nil || len(f.samples) == 0 {
		return castSample{}, false
	}
	return f.samples[len(f.samples)-1], true
}

// drift is wall-clock elapsed minus media-time elapsed since sampling began:
// positive means the picture is falling behind real time.
func (f *castForm) drift() (float64, bool) {
	s, ok := f.latest()
	if !ok || !f.baseSet {
		return 0, false
	}
	return s.at.Sub(f.baseWall).Seconds() - (s.pos - f.basePos), true
}

// bitrate derives Mbps from the last two proxied samples. Direct casts have
// no byte counts — the TV pulls the origin itself — so there is nothing to
// derive from and ok is false.
func (f *castForm) bitrate() (float64, bool) {
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

func humanMB(n uint64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}

// castStep drives the send-to-TV sheet. Enter casts the pinned channel to
// the typed address, the picked TV, or the last TV in that order; up/down
// pick a found TV; r rescans while the address field is empty (so typing a
// hostname keeps its letters); esc goes back. Stopping is x in main mode —
// kept out of the sheet so every key stays typeable. The TV keeps playing
// after esc, like keep-mpv-on-quit.
func (m *App) castStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		return m, nil
	case "enter":
		ch, ok := m.castChannel()
		if !ok {
			m.setStatus("no channel pinned "+glDash+" esc back, pick one, press c", true)
			return m, nil
		}
		m.startCast(ch)
		return m, nil
	case "r":
		if m.castForm != nil && strings.TrimSpace(m.castForm.input.Value()) == "" {
			m.startScan()
			return m, nil
		}
		return m, m.castForm.Update(msg)
	case "up", "down":
		if m.castForm != nil && len(m.castForm.found) > 0 {
			if msg.String() == "up" {
				m.castForm.sel = (m.castForm.sel - 1 + len(m.castForm.found)) % len(m.castForm.found)
			} else {
				m.castForm.sel = (m.castForm.sel + 1) % len(m.castForm.found)
			}
		}
		return m, nil
	}
	return m, m.castForm.Update(msg)
}
