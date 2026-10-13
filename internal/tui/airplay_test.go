package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mertturunc/iptv-tui/internal/airplay"
	"github.com/mertturunc/iptv-tui/internal/cast"
	"github.com/mertturunc/iptv-tui/internal/store"
)

func TestAirplayOverlayOpenClose(t *testing.T) {
	a := newTestApp()
	a.Update(keyMsg("v"))
	if a.mode != modeAirplay || a.airForm == nil {
		t.Fatal("v should open the AirPlay sheet")
	}
	if out := a.View(); !strings.Contains(out, "AirPlay") {
		t.Fatalf("airplay sheet missing: %q", out)
	}
	a.Update(keyMsg("esc"))
	if a.mode != modeMain {
		t.Fatal("esc should close the AirPlay sheet")
	}
}

func TestAirplayEnterWithoutAddress(t *testing.T) {
	a := newTestApp()
	a.openAirplay()
	// Empty field and no known TV: must fail loudly, not dial anything.
	a.Update(keyMsg("enter"))
	if !a.statusErr {
		t.Fatal("airplay with no address should set an error status")
	}
	if a.mode != modeAirplay {
		t.Fatal("failed airplay must stay on the sheet")
	}
}

func TestAirplayDoneAndStop(t *testing.T) {
	a := newTestApp()
	a.openAirplay()
	a.onAirDone(airDoneMsg{addr: "192.168.1.60", name: "Living Room", title: "Demo"})
	if a.airForm == nil || !a.airForm.casting {
		t.Fatal("airplay done should mark the sheet casting")
	}
	if a.cfg.Cfg.Settings.AirPlayTV != "192.168.1.60" {
		t.Fatalf("last TV not remembered: %q", a.cfg.Cfg.Settings.AirPlayTV)
	}
	if out := a.renderPlaybar(); !strings.Contains(out, "Demo") {
		t.Fatalf("playbar should name the TV program: %q", out)
	}
	a.onAirStop(airStopMsg{})
	if a.airForm.casting {
		t.Fatal("stop should clear casting")
	}
	// A failed play clears state and complains.
	a.onAirDone(airDoneMsg{err: errors.New("boom")})
	if a.airForm.casting || !a.statusErr {
		t.Fatal("failed airplay should clear casting with an error status")
	}
}

func TestMainXStopsAirplay(t *testing.T) {
	a := newTestApp()
	a.openAirplay()
	a.mode = modeMain
	a.airForm.client = &airplay.Client{}
	// x with no local player but a live AirPlay session stops the TV.
	a.Update(keyMsg("x"))
	if !a.airForm.busy {
		t.Fatal("stop should have started (busy flag set)")
	}
}

func TestAirplayStatsRecord(t *testing.T) {
	a := newTestApp()
	a.openAirplay()
	a.airForm.client = &airplay.Client{}
	a.airForm.proxy = &cast.Proxy{}
	a.airForm.casting = true
	feed := func(state string, pos float64, bytes uint64) {
		a.onAirStat(airStatMsg{
			st:  &airplay.PlaybackInfo{State: state, Position: pos, Duration: 3600, Rate: 1},
			rtt: 20 * time.Millisecond, bytes: bytes,
		})
		time.Sleep(10 * time.Millisecond) // separate wall-clock samples
	}
	feed("PLAYING", 60, 1<<20)
	feed("PLAYING", 62, 3<<20)
	feed("IDLE", 62, 3<<20)
	if len(a.airForm.samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(a.airForm.samples))
	}
	out := a.View()
	for _, want := range []string{"state: IDLE", "Mbps"} {
		if !strings.Contains(out, want) {
			t.Fatalf("dashboard missing %q in:\n%s", want, out)
		}
	}
	if bar := a.renderPlaybar(); !strings.Contains(bar, "IDLE") {
		t.Fatalf("playbar should carry receiver state: %q", bar)
	}
	// A failed poll keeps old samples and notes the error.
	a.onAirStat(airStatMsg{err: errors.New("timeout")})
	if len(a.airForm.samples) != 3 || a.airForm.statErr == "" {
		t.Fatal("failed poll must preserve samples and note the error")
	}
}

func TestAirplayRemembersLastTV(t *testing.T) {
	a := newTestApp()
	a.cfg.Cfg.Settings.AirPlayTV = "192.168.1.60"
	a.openAirplay()
	if a.airForm.lastAddr != "192.168.1.60" {
		t.Fatalf("sheet should know last TV, got %q", a.airForm.lastAddr)
	}
	if out := a.View(); !strings.Contains(out, "last TV") {
		t.Fatalf("sheet should mention last TV: %q", out)
	}
}

func TestAirplayScanSelectsLastTV(t *testing.T) {
	a := newTestApp()
	a.cfg.Cfg.Settings.AirPlayTV = "192.168.1.61"
	a.openAirplay()
	a.onAirScan(airScanMsg{devices: []airplay.Device{
		{Name: "Bedroom", Addr: "192.168.1.62"},
		{Name: "Living Room", Addr: "192.168.1.61"},
	}, done: true})
	if a.airForm.sel != 1 {
		t.Fatalf("scan should highlight the last TV, sel=%d", a.airForm.sel)
	}
	if out := a.View(); !strings.Contains(out, "Living Room (192.168.1.61)") {
		t.Fatalf("sheet should list Living Room: %q", out)
	}
}

func TestAirplayPinsChannelAtOpen(t *testing.T) {
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
	a.openAirplay()
	if !a.airForm.hasPending || a.airForm.pending.Name != "Pinned" {
		t.Fatal("open should pin the channel under the cursor")
	}
	// Focus moves away afterwards; enter must still play the pinned channel.
	a.focus = "groups"
	a.airForm.lastAddr = "192.168.1.60"
	a.Update(keyMsg("enter"))
	if !a.airForm.busy {
		t.Fatal("enter should play the pinned channel regardless of focus")
	}
}
