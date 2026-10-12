package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/store"
)

func noSend(tea.Msg) {}

func newTestApp() *App {
	cfg := &store.Store{Cfg: store.DefaultConfig()}
	a := New(cfg)
	a.SetSend(noSend)
	msg := tea.WindowSizeMsg{Width: 100, Height: 30}
	a.Update(msg)
	return a
}

func TestViewMainEmpty(t *testing.T) {
	a := newTestApp()
	out := a.View()
	if out == "" {
		t.Fatal("empty view")
	}
	if !strings.Contains(strings.ToUpper(out), "IPTVTUI") {
		t.Fatalf("missing title bar: %q", out)
	}
}

func TestViewOverlays(t *testing.T) {
	a := newTestApp()
	// help
	a.mode = modeHelp
	if out := a.View(); !strings.Contains(out, "Key bindings") {
		t.Fatalf("help: %q", out)
	}
	// add form
	a.openAdd("m3u")
	if out := a.View(); !strings.Contains(out, "Add playlist") {
		t.Fatalf("add: %q", out)
	}
	// sources overlay
	a.cfg.AddSource(store.Source{ID: "a", Name: "demo", Kind: "m3u", URL: "x"})
	a.openSources()
	if out := a.View(); !strings.Contains(out, "demo") {
		t.Fatalf("sources: %q", out)
	}
}

func TestViewWithSource(t *testing.T) {
	a := newTestApp()
	ld := &store.Loaded{
		Source:   &store.Source{ID: "a", Name: "demo", Kind: "m3u"},
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	a.cfg.AddSource(*ld.Source)
	ld.Channels[store.Live] = []store.Channel{{Key: "k", Name: "Test One"}, {Key: "k2", Name: "Test Two"}}
	ld.Groups[store.Live] = []string{"Ungrouped"}
	a.loaded = []*store.Loaded{ld}
	a.selectSource(0)
	for i := 0; i < 5; i++ {
		if out := a.View(); out == "" {
			t.Fatal("empty view")
		}
	}
	if !strings.Contains(a.View(), "Test One") {
		t.Fatalf("channels missing: %q", a.View())
	}
}

func TestSeriesView(t *testing.T) {
	a := newTestApp()
	ld := &store.Loaded{
		Source:   &store.Source{ID: "a", Name: "demo", Kind: "xtream"},
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	a.cfg.AddSource(*ld.Source)
	ld.Channels[store.Series] = []store.Channel{{Key: "s", Name: "Top Show", Type: store.Series, SeriesID: 9}}
	ld.Groups[store.Series] = []string{"All"}
	a.loaded = []*store.Loaded{ld}
	a.selectSource(0)
	a.mediaType = store.Series
	a.rebuildItems()
	a.inSeries = true
	a.episodeChans = []store.Channel{{Key: "e", Name: "S01E01 · Pilot", URL: "u"}}
	a.rebuildItems()
	a.View()
}

func TestAdaptiveLayout(t *testing.T) {
	a := newTestApp()
	// Narrow window → stacked, wide → columns.
	a.Update(tea.WindowSizeMsg{Width: 50, Height: 30})
	if !a.vertical {
		t.Fatalf("expected stacked layout for narrow width, got horizontal")
	}
	a.Update(tea.WindowSizeMsg{Width: 200, Height: 50})
	if a.vertical {
		t.Fatal("expected columns for wide terminal")
	}
	if a.chW <= 0 || a.chH <= 0 {
		t.Fatalf("bad channel pane dims: %dx%d", a.chW, a.chH)
	}
	// Zero-size message should fall back to sensible defaults without panics.
	a.Update(tea.WindowSizeMsg{})
	if a.width <= 0 || a.height <= 0 {
		t.Fatalf("missing size fallback: %dx%d", a.width, a.height)
	}
}

func TestFooterResponsive(t *testing.T) {
	a := newTestApp()
	a.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	narrow := a.keyHints()
	a.Update(tea.WindowSizeMsg{Width: 140, Height: 50})
	wide := a.keyHints()
	if narrow == wide {
		t.Fatalf("expected different key hints for narrow vs wide, got %q", narrow)
	}
	foot := a.renderFooter()
	if !strings.Contains(foot, "q quit") {
		t.Fatalf("footer missing quit hint: %q", foot)
	}
}

func TestNoChannelsView(t *testing.T) {
	a := newTestApp()
	ld := &store.Loaded{
		Source:   &store.Source{ID: "a", Name: "demo", Kind: "m3u"},
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	a.cfg.AddSource(*ld.Source)
	a.loaded = []*store.Loaded{ld}
	a.selectSource(0)
	for i := 0; i < 3; i++ {
		if out := a.View(); out == "" {
			t.Fatal("empty view with no channels")
		}
	}
}
