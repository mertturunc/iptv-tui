//go:build !short

package tui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mertturunc/iptv-tui/internal/store"
)

// keyScript replays keystrokes the way a terminal delivers them: one short
// read per press, so escape sequences never fuse with their neighbours
// (a lone ESC followed by "q" in one buffer would parse as alt+q).
// After the script it blocks instead of EOF, which would tear down input.
type keyScript struct {
	keys [][]byte
	i    int
}

func (k *keyScript) Read(p []byte) (int, error) {
	if k.i >= len(k.keys) {
		select {}
	}
	time.Sleep(20 * time.Millisecond)
	n := copy(p, k.keys[k.i])
	k.i++
	return n, nil
}

// TestHeadlessThemeSwitchE2E launches the real program headless and drives
// the exact user flow — help, right, right, esc, quit — through raw bytes,
// renderer included. Unit tests call Update with pre-parsed keys; this one
// proves the bytes survive input parsing and reach the screen.
func TestHeadlessThemeSwitchE2E(t *testing.T) {
	t.Chdir(t.TempDir()) // cycleTheme saves settings; keep it out of the repo
	defer ApplyThemeIndex(0)
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)
	ApplyThemeIndex(0)

	cfg := &store.Store{Cfg: store.DefaultConfig()}
	app := New(cfg)
	src := store.Source{ID: "a", Name: "demo", Kind: "m3u"}
	cfg.AddSource(src)
	ld := &store.Loaded{
		Source:   &cfg.Cfg.Sources[0],
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	ld.Channels[store.Live] = []store.Channel{{Key: "k", Name: "Test One", Group: "Ungrouped"}}
	ld.Groups[store.Live] = []string{"Ungrouped"}
	app.loaded = []*store.Loaded{ld}

	keys := [][]byte{[]byte("?"), []byte("\x1b[C"), []byte("\x1b[C"), []byte("\x1b"), []byte("q")}
	var out bytes.Buffer
	p := tea.NewProgram(app, tea.WithInput(&keyScript{keys: keys}), tea.WithOutput(&out), tea.WithAltScreen())
	app.SetSend(p.Send)

	done := make(chan struct{})
	go func() {
		_, _ = p.Run()
		close(done)
	}()
	// Size first, synchronously: Send returns once the event loop takes it,
	// while the first keystroke is still 20ms out, so every rendered frame
	// below is a real themed surface, never the pre-ready splash.
	p.Send(tea.WindowSizeMsg{Width: 160, Height: 40})
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("program did not quit on the scripted keys")
	}

	if got := CurrentThemeName(); got != "Nord" {
		t.Fatalf("end theme = %q, want Nord after two rights", got)
	}
	if got := app.cfg.Cfg.Settings.Theme; got != "Nord" {
		t.Fatalf("saved theme = %q, want Nord", got)
	}
	rendered := out.String()
	if !strings.Contains(rendered, "48;2;136;192;208") {
		t.Fatal("renderer output never carried the nord signal color")
	}

	// Final surface under the switched palette, sized deterministically.
	app.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	assertFullBleed(t, "e2e/nord-main", app.View())
}
