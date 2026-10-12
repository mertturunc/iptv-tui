package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// assertFullBleed proves a rendered frame is fully painted: every escape is
// SGR, and every cell outside newlines carries an explicit background while
// every visible rune also carries an explicit foreground. Anything less and
// a palette switch leaves terminal-default holes behind.
func assertFullBleed(t *testing.T, what, out string) {
	t.Helper()
	fgSet, bgSet := false, false
	i := 0
	for i < len(out) {
		if out[i] == '\x1b' {
			m := sgrRe.FindStringSubmatch(out[i:])
			if m == nil {
				end := i + 12
				if end > len(out) {
					end = len(out)
				}
				t.Fatalf("%s: non-SGR escape at byte %d (%q)", what, i, out[i:end])
				return
			}
			trackSGR(m[1], &fgSet, &bgSet)
			i += len(m[0])
			continue
		}
		j := i
		for j < len(out) && out[j] != '\x1b' {
			j++
		}
		for _, r := range out[i:j] {
			if r == '\n' {
				continue
			}
			if !bgSet {
				t.Fatalf("%s: cell %q without background", what, r)
				return
			}
			if r != ' ' && !fgSet {
				t.Fatalf("%s: cell %q without foreground", what, r)
				return
			}
		}
		i = j
	}
}

func bleedApp() *App {
	a := layoutApp()
	a.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	return a
}

// TestFullBleedPaint is the "do colors fully switch" test: for every palette
// and every color-capable profile, every surface must come out with no
// unpainted cell.
func TestFullBleedPaint(t *testing.T) {
	prev := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(prev)
	defer ApplyThemeIndex(0)
	for _, prof := range []termenv.Profile{termenv.TrueColor, termenv.ANSI256, termenv.ANSI} {
		lipgloss.SetColorProfile(prof)
		for ti := range Themes {
			ApplyThemeIndex(ti)
			what := Themes[ti].Name + "/" + prof.Name()
			assertFullBleed(t, what+"/main", bleedApp().View())

			playing := bleedApp()
			playing.playing = &player.Process{}
			playing.playingCh = store.Channel{Name: "Some Movie", Type: store.Movie}
			playing.playPos, playing.playDur = 12*time.Minute, 58*time.Minute
			assertFullBleed(t, what+"/playing", playing.View())

			help := bleedApp()
			help.mode = modeHelp
			assertFullBleed(t, what+"/help", help.View())

			sources := bleedApp()
			sources.openSources()
			assertFullBleed(t, what+"/sources", sources.View())

			add := bleedApp()
			add.openAdd("m3u")
			assertFullBleed(t, what+"/add", add.View())

			epg := bleedApp()
			epg.epg = &epgView{title: "Demo", progs: []store.Program{{Title: "News at Ten"}}}
			epg.mode = modeEpg
			assertFullBleed(t, what+"/epg", epg.View())

			mpv := bleedApp()
			mpv.openMpvSetup()
			assertFullBleed(t, what+"/mpv", mpv.View())

			series := bleedApp()
			series.inSeries = true
			series.seriesTitle = "Show"
			series.episodeChans = []store.Channel{
				{Key: "e", Name: "S01 E01 · Pilot", URL: "u", Group: "Season 1", Season: 1, Episode: 1},
			}
			series.groupIdx = 0
			series.rebuildItems()
			assertFullBleed(t, what+"/series", series.View())

			search := bleedApp()
			search.mode = modeSearch
			assertFullBleed(t, what+"/search", search.View())

			empty := newTestApp()
			assertFullBleed(t, what+"/empty", empty.View())
		}
	}
}

// TestAsciiEmitsNothing is the negative control: without color support the
// frame must stay plain — no half-painted truecolor leaks for dumb terminals.
func TestAsciiEmitsNothing(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.Ascii)
	defer lipgloss.SetColorProfile(prev)
	defer ApplyThemeIndex(0)
	ApplyThemeIndex(4) // light theme: even then, nothing may leak
	out := bleedApp().View()
	if strings.Contains(out, "\x1b") {
		t.Fatal("ascii profile must not emit escape codes")
	}
}

func TestNearestColors(t *testing.T) {
	if got := nearest256([3]int{0, 0, 0}); got != 0 {
		t.Fatalf("black 256 = %d, want 0", got)
	}
	if got := nearest256([3]int{255, 255, 255}); got != 15 {
		t.Fatalf("white 256 = %d, want 15", got)
	}
	if got := nearest256([3]int{255, 0, 0}); got != 9 {
		t.Fatalf("red 256 = %d, want 9", got)
	}
	if got := nearest16([3]int{0, 0, 0}); got != 0 {
		t.Fatalf("black 16 = %d, want 0", got)
	}
	if got := nearest16([3]int{255, 255, 255}); got != 15 {
		t.Fatalf("white 16 = %d, want 15", got)
	}
	if got := nearest16([3]int{255, 0, 0}); got != 9 {
		t.Fatalf("red 16 = %d, want 9", got)
	}
	// Every theme token must survive the round trip to a valid code.
	for _, th := range Themes {
		for _, tok := range []string{th.Ink, th.Ground} {
			if _, ok := hexRGB(tok); !ok {
				t.Fatalf("%s has an unparsable token %q", th.Name, tok)
			}
		}
	}
}
