package tui

// Screenshot captures for docs/screenshots.
//
// Run with IPTV_TUI_GEN_SHOTS=1 to (re)generate the .ansi frames:
//
//	IPTV_TUI_GEN_SHOTS=1 go test ./internal/tui/ -run TestGenShots -count=1
//
// The frames are rendered to PNG/GIF by docs/screenshots/render.py.
// The test skips without the env var so normal `go test ./...` stays clean.

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
)

const (
	shotW = 160
	shotH = 40
)

func shotDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "docs", "screenshots")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir shots: %v", err)
	}
	return dir
}

func writeShot(t *testing.T, dir, name, view string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name+".ansi"), []byte(view), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// demoLiveChannels returns clearly-fake Xtream live channels across groups.
func demoLiveChannels(srcID string) ([]store.Channel, []string) {
	groups := []string{"Sports", "News", "Kids", "Music", "Documentary"}
	names := map[string][]string{
		"Sports":      {"Demo Football 1", "Demo Football 2", "Demo Basketball", "Demo Tennis"},
		"News":        {"Demo News 24", "Demo World News", "Demo Business"},
		"Kids":        {"Demo Toons", "Demo Kids Jr", "Demo Learning"},
		"Music":       {"Demo Hits", "Demo Classical", "Demo Jazz"},
		"Documentary": {"Demo Nature", "Demo History", "Demo Science"},
	}
	var out []store.Channel
	num := 101
	sid := 1001
	for _, g := range groups {
		for _, n := range names[g] {
			url := "https://demo.example.com/live/" + itoa(sid) + ".m3u8"
			out = append(out, store.Channel{
				Key:        store.ChannelKey(srcID, store.Live, url),
				SourceID:   srcID,
				SourceName: "Demo XC",
				Type:       store.Live,
				Number:     num,
				Name:       n,
				URL:        url,
				Group:      g,
				TvgID:      "demo." + slug(n),
				StreamID:   sid,
			})
			num++
			sid++
		}
	}
	return out, groups
}

func demoMovies(srcID string) ([]store.Channel, []string) {
	groups := []string{"Action", "Comedy", "Drama"}
	names := map[string][]string{
		"Action": {"Harbor Lights", "Neon Tide", "Winter Signal", "Salt & Cinder"},
		"Comedy": {"Midnight Diner", "Paper Moons", "Slow Parade"},
		"Drama":  {"The Last Ferry", "Static Bloom", "The Cartographer", "Glass Orchard", "Echo Meridian"},
	}
	var out []store.Channel
	num := 201
	sid := 2001
	for _, g := range groups {
		for _, n := range names[g] {
			url := "https://demo.example.com/movie/" + itoa(sid) + ".mp4"
			out = append(out, store.Channel{
				Key:        store.ChannelKey(srcID, store.Movie, url),
				SourceID:   srcID,
				SourceName: "Demo XC",
				Type:       store.Movie,
				Number:     num,
				Name:       n + " (2024)",
				URL:        url,
				Group:      g,
				StreamID:   sid,
			})
			num++
			sid++
		}
	}
	return out, groups
}

func demoSeries(srcID string) ([]store.Channel, []string) {
	groups := []string{"Drama", "Sci-Fi"}
	type show struct {
		name  string
		group string
		id    int
	}
	shows := []show{
		{"Harbor Bay", "Drama", 301},
		{"The Night Shift", "Drama", 302},
		{"Saltline", "Sci-Fi", 303},
		{"Meridian", "Sci-Fi", 304},
	}
	var out []store.Channel
	for i, s := range shows {
		url := "series:" + itoa(s.id)
		out = append(out, store.Channel{
			Key:        store.ChannelKey(srcID, store.Series, url),
			SourceID:   srcID,
			SourceName: "Demo XC",
			Type:       store.Series,
			Number:     301 + i,
			Name:       s.name,
			Group:      s.group,
			SeriesID:   s.id,
		})
	}
	return out, groups
}

func demoEpisodes(srcID string) []store.Channel {
	titles := []string{"Pilot", "Low Water", "The Crossing", "Signal Loss", "High Tide", "Homecoming"}
	var out []store.Channel
	for i, title := range titles {
		ep := i + 1
		url := "https://demo.example.com/series/301/" + itoa(ep) + ".mp4"
		out = append(out, store.Channel{
			Key:        store.ChannelKey(srcID, store.Series, url),
			SourceID:   srcID,
			SourceName: "Demo XC",
			Type:       store.Series,
			Name:       "S01 E" + two(ep) + " · " + title,
			URL:        url,
			Group:      "Season 1",
			SeriesID:   301,
			Season:     1,
			Episode:    ep,
		})
	}
	return out
}

// demoEPG attaches now/next programmes to a few live channels.
func demoEPG(ld *store.Loaded, now time.Time) {
	progs := map[string][]string{
		"demo.demo-football-1": {"Morning Kickoff", "Halftime Report", "Evening Derby"},
		"demo.demo-news-24":    {"Headlines", "World Briefing", "Night Desk"},
		"demo.demo-toons":      {"Pixel Pals", "Captain Comet", "Lunch Bunch"},
		"demo.demo-hits":       {"Top 40 Countdown", "New Drops", "Request Hour"},
		"demo.demo-nature":     {"Reef Kingdom", "Desert Nights", "Alpine Tales"},
	}
	byID := map[string][]store.Program{}
	for id, titles := range progs {
		byID[id] = []store.Program{
			{Start: now.Add(-90 * time.Minute), Stop: now.Add(-30 * time.Minute), Channel: id, Title: titles[0], Desc: "Demo listing"},
			{Start: now.Add(-30 * time.Minute), Stop: now.Add(30 * time.Minute), Channel: id, Title: titles[1], Desc: "Demo listing, on now"},
			{Start: now.Add(30 * time.Minute), Stop: now.Add(90 * time.Minute), Channel: id, Title: titles[2], Desc: "Demo listing, up next"},
		}
	}
	ld.EPG = &store.ProgramIndex{ByID: byID}
}

func demoGuide(channel string, now time.Time) []store.Program {
	// Anchored around now so one row carries the live marker.
	titles := []string{
		"Sunrise Sports", "Morning Kickoff", "Halftime Report", "Transfer Talk",
		"Midday Match", "Classic Replay", "Evening Derby", "Post-Match Analysis",
	}
	var out []store.Program
	t := now.Truncate(90 * time.Minute).Add(-3 * time.Hour)
	for _, title := range titles {
		out = append(out, store.Program{
			Start:   t,
			Stop:    t.Add(90 * time.Minute),
			Channel: channel,
			Title:   title,
			Desc:    "Demo EPG entry.",
		})
		t = t.Add(90 * time.Minute)
	}
	return out
}

func slug(s string) string {
	out := ""
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out += string(r)
		case r >= 'A' && r <= 'Z':
			out += string(r + ('a' - 'A'))
		case r == ' ' || r == '&':
			out += "-"
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [16]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func two(n int) string {
	if n < 10 {
		return "0" + itoa(n)
	}
	return itoa(n)
}

// newDemoApp builds the pseudo-Xtream app shared by every capture.
func newDemoApp() *App {
	cfg := &store.Store{Cfg: store.DefaultConfig()}
	a := New(cfg)
	a.SetSend(noSend)
	src := store.Source{ID: "demo-xc", Name: "Demo XC", Kind: "xtream", Server: "https://demo.example.com"}
	cfg.AddSource(src)

	live, liveGroups := demoLiveChannels(src.ID)
	movies, movieGroups := demoMovies(src.ID)
	series, seriesGroups := demoSeries(src.ID)

	ld := &store.Loaded{
		Source:   &cfg.Cfg.Sources[0],
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		Episodes: map[string][]store.Channel{},
	}
	ld.Channels[store.Live] = live
	ld.Groups[store.Live] = liveGroups
	ld.Channels[store.Movie] = movies
	ld.Groups[store.Movie] = movieGroups
	ld.Channels[store.Series] = series
	ld.Groups[store.Series] = seriesGroups
	demoEPG(ld, time.Now())

	// A few favorites so the star + Favorites row render.
	for _, ch := range live {
		if ch.Name == "Demo Football 1" || ch.Name == "Demo News 24" {
			cfg.Cfg.Favorites[ch.Key] = true
		}
	}
	for _, ch := range movies {
		if ch.Name == "Harbor Lights (2024)" {
			cfg.Cfg.Favorites[ch.Key] = true
		}
	}

	a.loaded = []*store.Loaded{ld}
	a.Update(tea.WindowSizeMsg{Width: shotW, Height: shotH})
	a.selectSource(0)
	a.setStatus("loaded Demo XC", false)
	return a
}

// focusGroup selects a group row by label, then highlights a channel row.
func focusGroup(a *App, label string, chanIdx int) {
	idx := 0
	for i, e := range a.groupEntries {
		if e.label == label {
			idx = i
			break
		}
	}
	a.groupIdx = idx
	a.grpList.Select(idx)
	a.rebuildItems()
	if chanIdx < len(a.visible) {
		a.chList.Select(chanIdx)
	}
}

func TestGenShots(t *testing.T) {
	if os.Getenv("IPTV_TUI_GEN_SHOTS") == "" {
		t.Skip("set IPTV_TUI_GEN_SHOTS=1 to generate docs screenshots")
	}
	// Force truecolor so captures carry the theme instead of plain text.
	// IPTV_TUI_GEN_PROFILE=ansi256|ansi downgrades the capture to model a
	// degraded terminal for visual verification (never used for docs).
	switch os.Getenv("IPTV_TUI_GEN_PROFILE") {
	case "ansi256":
		lipgloss.SetColorProfile(termenv.ANSI256)
	case "ansi":
		lipgloss.SetColorProfile(termenv.ANSI)
	default:
		lipgloss.SetColorProfile(termenv.TrueColor)
	}
	dir := shotDir(t)

	// 01: live rack with groups, favorites, now/next, playing channel.
	a := newDemoApp()
	focusGroup(a, "Sports", 1)
	if len(a.visible) > 1 {
		ch := a.visible[1]
		a.playing = &player.Process{}
		a.playingCh = ch
		a.playPos, a.playDur = 14*time.Minute+22*time.Second, 0 // live: elapsed clock
	}
	writeShot(t, dir, "01-live", a.View())

	// 02: movies tab with resume meter.
	b := newDemoApp()
	b.mediaType = store.Movie
	b.groupIdx = 0
	b.rebuildItems()
	focusGroup(b, "Action", 0)
	if len(b.visible) > 0 {
		ch := b.visible[0]
		b.playing = &player.Process{}
		b.playingCh = ch
		b.playPos, b.playDur = 12*time.Minute+4*time.Second, 58*time.Minute
	}
	b.setStatus("resuming Harbor Lights at 12:04", false)
	writeShot(t, dir, "02-movies", b.View())

	// 03: EPG overlay for a sports channel.
	c := newDemoApp()
	focusGroup(c, "Sports", 0)
	c.epg = &epgView{title: "Demo Football 1", progs: demoGuide("Demo Football 1", time.Now())}
	c.mode = modeEpg
	writeShot(t, dir, "03-epg", c.View())

	// 04: series episodes.
	d := newDemoApp()
	d.mediaType = store.Series
	d.groupIdx = 0
	d.rebuildItems()
	d.inSeries = true
	d.seriesTitle = "Harbor Bay"
	d.episodeChans = demoEpisodes("demo-xc")
	d.focus = "channels"
	d.rebuildItems()
	d.chList.Select(2)
	d.setStatus("6 episodes", false)
	writeShot(t, dir, "04-series", d.View())

	// 05: help overlay (GIF frame only, also useful on its own).
	e := newDemoApp()
	focusGroup(e, "News", 0)
	e.mode = modeHelp
	writeShot(t, dir, "05-help", e.View())

	// 06: player setup shown when no mpv, VLC, or MPC-HC is found.
	f := newDemoApp()
	focusGroup(f, "Sports", 0)
	f.openMpvSetup()
	writeShot(t, dir, "06-mpv", f.View())

	// 07: the same live rack under the light counterpart.
	g := newDemoApp()
	focusGroup(g, "Sports", 1)
	ApplyThemeIndex(ThemeIndexByName("Gruvbox Light Hard"))
	writeShot(t, dir, "07-light", g.View())
	ApplyThemeIndex(0)
}

// themeSlug maps a theme name to its screenshot file stem.
func themeSlug(name string) string {
	s := ""
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			s += string(r)
		case r >= 'A' && r <= 'Z':
			s += string(r + ('a' - 'A'))
		case r == ' ' || r == '-':
			s += "-"
		}
	}
	return "theme-" + s
}

// TestGenThemeShots captures the live rack once per palette for the README
// theme table. Same env gate as TestGenShots.
func TestGenThemeShots(t *testing.T) {
	if os.Getenv("IPTV_TUI_GEN_SHOTS") == "" {
		t.Skip("set IPTV_TUI_GEN_SHOTS=1 to generate docs screenshots")
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer ApplyThemeIndex(0)
	dir := shotDir(t)
	for i := range Themes {
		ApplyThemeIndex(i)
		a := newDemoApp()
		focusGroup(a, "Sports", 1)
		writeShot(t, dir, themeSlug(Themes[i].Name), a.View())
	}
}
