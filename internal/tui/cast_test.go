package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/cast"
	"github.com/mertturunc/iptv-tui/internal/store"
)

func keyMsg(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestCastOverlayOpenClose(t *testing.T) {
	a := newTestApp()
	a.Update(keyMsg("c"))
	if a.mode != modeCast || a.castForm == nil {
		t.Fatal("c should open the send-to-TV sheet")
	}
	if out := a.View(); !strings.Contains(out, "send to TV") {
		t.Fatalf("cast sheet missing: %q", out)
	}
	a.Update(keyMsg("esc"))
	if a.mode != modeMain {
		t.Fatal("esc should close the cast sheet")
	}
}

func TestCastEnterWithoutAddress(t *testing.T) {
	a := newTestApp()
	a.openCast()
	// Empty field and no known TV: must fail loudly, not dial anything.
	a.Update(keyMsg("enter"))
	if !a.statusErr {
		t.Fatal("casting with no address should set an error status")
	}
	if a.mode != modeCast {
		t.Fatal("failed cast must stay on the sheet")
	}
}

func TestCastStopIdle(t *testing.T) {
	a := newTestApp()
	a.openCast()
	a.mode = modeMain // back out; x in main with nothing running
	a.Update(keyMsg("x"))
	if a.statusErr {
		t.Fatalf("idle stop should be a plain note, got %q", a.status)
	}
	if !strings.Contains(a.status, "nothing playing") {
		t.Fatalf("idle x should say nothing playing, got %q", a.status)
	}
}

func TestMainXStopsTV(t *testing.T) {
	a := newTestApp()
	a.openCast()
	a.mode = modeMain
	a.castForm.caster = &cast.Caster{}
	// x with no local player but a live cast stops the TV, not mpv.
	a.Update(keyMsg("x"))
	if !a.castForm.busy {
		t.Fatal("stop should have started (busy flag set)")
	}
}

func TestCastBusySerializes(t *testing.T) {
	a := newTestApp()
	a.openCast()
	a.mode = modeMain // x is handled in main mode now
	a.castForm.caster = &cast.Caster{}
	a.castForm.busy = true
	// x while a cast is in flight must refuse, not race Close vs send.
	a.Update(keyMsg("x"))
	if a.castForm.caster == nil {
		t.Fatal("busy stop must not touch the session")
	}
	if !strings.Contains(a.status, "wait a moment") {
		t.Fatalf("busy stop should say to wait, got %q", a.status)
	}
	// Enter while busy must refuse the same way (back on the sheet).
	a.mode = modeCast
	a.castForm.pending = store.Channel{Key: "k", Name: "Busy", URL: "http://x/y.m3u8"}
	a.castForm.hasPending = true
	a.Update(keyMsg("enter"))
	if !strings.Contains(a.status, "wait a moment") {
		t.Fatalf("busy cast should say to wait, got %q", a.status)
	}
}

func TestCastRTypesWhenAddressStarted(t *testing.T) {
	a := newTestApp()
	a.openCast()
	// A started address owns the letter keys: r must type, not rescan.
	a.castForm.input.SetValue("my-tv")
	a.castForm.scanning = false
	a.Update(keyMsg("r"))
	if got := a.castForm.input.Value(); got != "my-tvr" {
		t.Fatalf("r with text started should type, got %q", got)
	}
	if a.castForm.scanning {
		t.Fatal("typing r must not rescan")
	}
	// Empty field: r rescans.
	a.castForm.input.SetValue("")
	a.Update(keyMsg("r"))
	if !a.castForm.scanning {
		t.Fatal("r on empty field should rescan")
	}
}

func TestCastDoneAndStop(t *testing.T) {
	a := newTestApp()
	a.openCast()
	a.onCastDone(castDoneMsg{addr: "192.168.1.50", name: "Living Room TV", title: "Demo"})
	if a.castForm == nil || !a.castForm.casting {
		t.Fatal("cast done should mark the sheet casting")
	}
	if a.cfg.Cfg.Settings.CastTV != "192.168.1.50" {
		t.Fatalf("last TV not remembered: %q", a.cfg.Cfg.Settings.CastTV)
	}
	if out := a.renderPlaybar(); !strings.Contains(out, "Demo") {
		t.Fatalf("playbar should name the TV program: %q", out)
	}
	a.onCastStop(castStopMsg{})
	if a.castForm.casting {
		t.Fatal("stop should clear casting")
	}
	// A failed cast clears state and complains.
	a.onCastDone(castDoneMsg{err: errors.New("boom")})
	if a.castForm.casting || !a.statusErr {
		t.Fatal("failed cast should clear casting with an error status")
	}
}

func TestCastPinsChannelAtOpen(t *testing.T) {
	a := newTestApp()
	ld := &store.Loaded{
		Source:   &store.Source{ID: "a", Name: "demo", Kind: "m3u"},
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	a.cfg.AddSource(*ld.Source)
	ld.Channels[store.Live] = []store.Channel{{Key: "k", Name: "Pinned", URL: "http://x/y.m3u8"}}
	a.loaded = []*store.Loaded{ld}
	a.selectSource(0)
	a.openCast()
	if !a.castForm.hasPending || a.castForm.pending.Name != "Pinned" {
		t.Fatal("open should pin the channel under the cursor")
	}
	if out := a.View(); !strings.Contains(out, "for: Pinned") {
		t.Fatalf("sheet should show the pinned channel: %q", out)
	}
	// Focus moves away afterwards; enter must still cast the pinned channel.
	a.focus = "groups"
	a.castForm.lastAddr = "192.168.1.182"
	a.Update(keyMsg("enter"))
	if !a.castForm.busy {
		t.Fatal("enter should cast the pinned channel regardless of focus")
	}
}

func TestCastInterimScanKeepsLooking(t *testing.T) {
	a := newTestApp()
	a.openCast()
	if !a.castForm.scanning {
		t.Fatal("sheet should be scanning on open")
	}
	a.onScanMsg(castScanMsg{devices: []cast.Device{{Name: "Toot", Addr: "192.168.1.182"}}})
	if !a.castForm.scanning {
		t.Fatal("interim update must not end the scan")
	}
	if out := a.View(); !strings.Contains(out, "Toot (192.168.1.182)") {
		t.Fatalf("interim TVs should list immediately: %q", out)
	}
	a.onScanMsg(castScanMsg{devices: []cast.Device{{Name: "Toot", Addr: "192.168.1.182"}}, done: true})
	if a.castForm.scanning {
		t.Fatal("done update must end the scan")
	}
}

func TestCastEnterHintNamesTarget(t *testing.T) {
	a := newTestApp()
	a.openCast()
	a.onScanMsg(castScanMsg{devices: []cast.Device{{Name: "Toot", Addr: "192.168.1.182"}}, done: true})
	if out := a.View(); !strings.Contains(out, "enter=cast to Toot") {
		t.Fatalf("hint should name the picked TV: %q", out)
	}
	a.castForm.input.SetValue("192.168.1.99")
	if out := a.View(); !strings.Contains(out, "enter=cast typed address") {
		t.Fatalf("hint should prefer typed input: %q", out)
	}
}

func TestCastRemembersLastTV(t *testing.T) {
	a := newTestApp()
	a.cfg.Cfg.Settings.CastTV = "192.168.1.50"
	a.openCast()
	if a.castForm.lastAddr != "192.168.1.50" {
		t.Fatalf("sheet should know last TV, got %q", a.castForm.lastAddr)
	}
	if out := a.View(); !strings.Contains(out, "last TV") {
		t.Fatalf("sheet should mention last TV: %q", out)
	}
	// With an empty field and nothing found, enter falls back to the last TV
	// and starts the attempt instead of failing.
	a.openCast()
	ld := &store.Loaded{
		Source:   &store.Source{ID: "a", Name: "demo", Kind: "m3u"},
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	a.cfg.AddSource(*ld.Source)
	ld.Channels[store.Live] = []store.Channel{{Key: "k", Name: "Recast", URL: "http://x/y.m3u8"}}
	a.loaded = []*store.Loaded{ld}
	a.selectSource(0)
	a.Update(keyMsg("enter"))
	if !a.castForm.busy {
		t.Fatal("enter should recast the last TV when the field is empty")
	}
}

func TestCastStatsRecord(t *testing.T) {
	a := newTestApp()
	a.openCast()
	a.castForm.caster = &cast.Caster{}
	a.castForm.proxy = &cast.Proxy{}
	a.castForm.casting = true
	feed := func(state string, pos float64, bytes uint64) {
		a.onStatMsg(castStatMsg{
			st:  &cast.MediaStatus{PlayerState: state, CurrentTime: pos, Duration: 3600, Rate: 1},
			rtt: 20 * time.Millisecond, bytes: bytes,
		})
		time.Sleep(10 * time.Millisecond) // separate wall-clock samples
	}
	feed("PLAYING", 60, 1<<20)
	feed("PLAYING", 62, 3<<20)
	feed("BUFFERING", 62, 3<<20)
	if len(a.castForm.samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(a.castForm.samples))
	}
	if a.castForm.stalls != 1 {
		t.Fatalf("stalls = %d, want 1 (one entry into BUFFERING)", a.castForm.stalls)
	}
	out := a.View()
	for _, want := range []string{"state: BUFFERING", "stalls 1", "Mbps", "!"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dashboard missing %q in:\n%s", want, out)
		}
	}
	if bar := a.renderPlaybar(); !strings.Contains(bar, "BUFFERING") {
		t.Fatalf("playbar should carry receiver state: %q", bar)
	}
	// A failed poll keeps old samples and notes the error.
	a.onStatMsg(castStatMsg{err: errors.New("timeout")})
	if len(a.castForm.samples) != 3 || a.castForm.statErr == "" {
		t.Fatal("failed poll must preserve samples and note the error")
	}
}

func TestCastDriftAndBitrate(t *testing.T) {
	f := newCastForm(60, "")
	base := time.Now()
	f.proxy = &cast.Proxy{}
	f.samples = []castSample{
		{at: base, state: "PLAYING", pos: 60, bytes: 0},
		{at: base.Add(4 * time.Second), state: "PLAYING", pos: 62, bytes: 4_000_000},
	}
	f.baseWall, f.basePos, f.baseSet = base, 60, true
	// Wall ran 4s, media 2s: 2s behind.
	if d, ok := f.drift(); !ok || d < 1.9 || d > 2.1 {
		t.Fatalf("drift = %v, %v; want ~2s", d, ok)
	}
	// 4 MB in 4s = 8 Mbps.
	if mbps, ok := f.bitrate(); !ok || mbps < 7.9 || mbps > 8.1 {
		t.Fatalf("bitrate = %v, %v; want ~8 Mbps", mbps, ok)
	}
	if g := f.bitrateSpark(20); g == "" {
		t.Fatal("sparkline should draw from proxied samples")
	}
	if r := f.stateRibbon(20); !strings.Contains(r, glyphMeterFilled) {
		t.Fatalf("ribbon should mark playing: %q", r)
	}
}

func TestCastDirectHasNoBitrate(t *testing.T) {
	f := newCastForm(60, "")
	f.caster = &cast.Caster{}
	f.casting = true
	f.proxy = nil // direct: TV pulls the origin itself
	f.samples = []castSample{{at: time.Now(), state: "PLAYING", pos: 10}}
	if _, ok := f.bitrate(); ok {
		t.Fatal("direct casts must not report bitrate")
	}
	if g := f.bitrateSpark(20); g != "" {
		t.Fatalf("direct casts get no graph: %q", g)
	}
	if out := f.statsView(60); !strings.Contains(out, "direct cast: no bitrate") {
		t.Fatalf("dashboard should say so: %q", out)
	}
}

func TestCastViaLineNamesPulls(t *testing.T) {
	f := newCastForm(60, "")
	f.caster = &cast.Caster{}
	f.casting = true
	f.proxy = &cast.Proxy{}
	f.samples = []castSample{{at: time.Now(), state: "IDLE", pos: 0}}
	out := f.statsView(60)
	if !strings.Contains(out, "TV pulls 0") {
		t.Fatalf("dashboard should show pull count: %q", out)
	}
	if !strings.Contains(out, "hasn't pulled yet") {
		t.Fatalf("dashboard should flag an unpulled proxy: %q", out)
	}
}

func TestCastScanSelectsLastTV(t *testing.T) {
	a := newTestApp()
	a.cfg.Cfg.Settings.CastTV = "192.168.1.182"
	a.openCast()
	a.onScanMsg(castScanMsg{devices: []cast.Device{
		{Name: "Bedroom", Addr: "192.168.1.99"},
		{Name: "Toot", Addr: "192.168.1.182"},
	}, done: true})
	if a.castForm.sel != 1 {
		t.Fatalf("scan should highlight the last TV, sel=%d", a.castForm.sel)
	}
	if out := a.View(); !strings.Contains(out, "Toot (192.168.1.182)") {
		t.Fatalf("sheet should list Toot: %q", out)
	}
	// Up/down wrap around the found list.
	a.Update(keyMsg("down"))
	if a.castForm.sel != 0 {
		t.Fatalf("down should wrap to 0, got %d", a.castForm.sel)
	}
	a.Update(keyMsg("up"))
	if a.castForm.sel != 1 {
		t.Fatalf("up should wrap to 1, got %d", a.castForm.sel)
	}
}

func TestCastScanFailureKeepsManual(t *testing.T) {
	a := newTestApp()
	a.openCast()
	a.onScanMsg(castScanMsg{err: errors.New("no multicast"), done: true})
	if a.castForm.scanning || len(a.castForm.found) != 0 {
		t.Fatal("failed scan must leave manual entry as the only path")
	}
	if out := a.View(); !strings.Contains(out, "type the address") {
		t.Fatalf("sheet should fall back to manual entry: %q", out)
	}
}

func TestCastKnowsChannels(t *testing.T) {
	a := newTestApp()
	ld := &store.Loaded{
		Source:   &store.Source{ID: "a", Name: "demo", Kind: "m3u"},
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	a.cfg.AddSource(*ld.Source)
	ld.Channels[store.Live] = []store.Channel{{Key: "k", Name: "Cast Me", URL: "http://x/y.m3u8"}}
	a.loaded = []*store.Loaded{ld}
	a.selectSource(0)
	a.openCast()
	a.castForm.input.SetValue("192.168.1.99")
	// Enter with no TV behind the address starts the attempt (busy) rather
	// than failing synchronously; the failure arrives as castDoneMsg.
	a.Update(keyMsg("enter"))
	if !a.castForm.busy {
		t.Fatal("enter on a channel should start the cast attempt")
	}
}
