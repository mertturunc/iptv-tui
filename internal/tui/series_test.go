package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/store"
)

func seriesTestApp(t *testing.T) *App {
	t.Helper()
	cfg := &store.Store{Cfg: store.DefaultConfig()}
	a := New(cfg)
	a.SetSend(noSend)
	src := store.Source{ID: "x", Name: "demo", Kind: "xtream", Server: "https://demo.example.com"}
	cfg.AddSource(src)
	ld := &store.Loaded{
		Source:   &cfg.Cfg.Sources[0],
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	ld.Channels[store.Series] = []store.Channel{{Key: "s", Name: "Show", Type: store.Series, SeriesID: 7}}
	ld.Groups[store.Series] = []string{"Drama"}
	a.loaded = []*store.Loaded{ld}
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	a.selectSource(0)
	a.mediaType = store.Series
	a.seriesTitle = "Show"
	a.inSeries = true
	a.episodeChans = []store.Channel{
		{Key: "e1", Name: "S01 E01 · Pilot", URL: "u1", Group: "Season 1", Season: 1, Episode: 1},
		{Key: "e2", Name: "S01 E02 · Low Water", URL: "u2", Group: "Season 1", Season: 1, Episode: 2},
		{Key: "e3", Name: "S02 E01 · Return", URL: "u3", Group: "Season 2", Season: 2, Episode: 1},
	}
	a.groupIdx = 0
	a.rebuildItems()
	return a
}

func TestSeriesSeasonGroups(t *testing.T) {
	a := seriesTestApp(t)
	if len(a.groupEntries) != 3 {
		t.Fatalf("want All + 2 seasons, got %+v", a.groupEntries)
	}
	if a.groupEntries[1].label != "Season 1" || a.groupEntries[1].count != 2 {
		t.Fatalf("bad season entry: %+v", a.groupEntries[1])
	}
	if len(a.visible) != 3 {
		t.Fatalf("All should show every episode, got %d", len(a.visible))
	}
	// Pick Season 2 from the groups pane.
	a.focus = "groups"
	a.grpList.Select(2)
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(*App)
	if got.groupIdx != 2 {
		t.Fatalf("groupIdx = %d, want 2", got.groupIdx)
	}
	if len(got.visible) != 1 || got.visible[0].Key != "e3" {
		t.Fatalf("season filter wrong: %+v", got.visible)
	}
	// Enter on the episode plays it (launch would need a player; select-only here).
	ch, ok := got.selectedChannel()
	if !ok || ch.Key != "e3" {
		t.Fatalf("selected = %+v %v, want e3", ch, ok)
	}
}

func TestBackspaceExitsSeries(t *testing.T) {
	a := seriesTestApp(t)
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	got := m.(*App)
	if got.inSeries {
		t.Fatal("backspace should leave the series view")
	}
	if got.episodeChans != nil {
		t.Fatal("backspace should drop episode state")
	}
}

func TestEscExitsSeries(t *testing.T) {
	a := seriesTestApp(t)
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.(*App).inSeries {
		t.Fatal("esc should leave the series view")
	}
}

func TestSeriesSelectedChannelUsesVisible(t *testing.T) {
	a := seriesTestApp(t)
	a.groupIdx = 2 // Season 2
	a.rebuildItems()
	a.focus = "channels"
	a.chList.Select(0)
	ch, ok := a.selectedChannel()
	if !ok || ch.Key != "e3" {
		t.Fatalf("selection must follow the filtered list, got %+v %v", ch, ok)
	}
}

func TestOpenSeriesSetsTitle(t *testing.T) {
	a := seriesTestApp(t)
	a.inSeries = false
	a.openSeries(store.Channel{Name: "Show", Type: store.Series, SeriesID: 7, SourceID: "x"})
	if a.seriesTitle != "Show" {
		t.Fatalf("seriesTitle = %q, want Show", a.seriesTitle)
	}
}
