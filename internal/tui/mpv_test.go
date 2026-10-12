package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
)

func TestMpvSetupSkip(t *testing.T) {
	a := newTestApp()
	a.openMpvSetup()
	if a.mode != modeMpv || a.mpvForm == nil {
		t.Fatalf("setup not opened: mode=%d form=%v", a.mode, a.mpvForm != nil)
	}
	m, _ := a.mpvStep(tea.KeyMsg{Type: tea.KeyEsc})
	if m.(*App).mode != modeMain {
		t.Fatal("esc should return to main")
	}
}

func TestMpvSetupBogusPathStays(t *testing.T) {
	t.Chdir(t.TempDir()) // keep Store.Save side effects out of the repo
	a := newTestApp()
	a.openMpvSetup()
	a.mpvForm.path.SetValue(filepath.Join(t.TempDir(), "no-such-player"))
	m, _ := a.mpvStep(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(*App)
	if got.mode != modeMpv {
		t.Fatal("bogus path should keep the setup open")
	}
	if !got.statusErr {
		t.Fatal("bogus path should set an error status")
	}
}

func TestMpvSetupEmptyRechecks(t *testing.T) {
	a := newTestApp()
	a.openMpvSetup()
	m, _ := a.mpvStep(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(*App)
	// Empty field only re-checks: open on machines without a player,
	// back to main where one exists. Either way it must not crash.
	if got.mode != modeMpv && got.mode != modeMain {
		t.Fatalf("unexpected mode %d", got.mode)
	}
}

func TestPlaybarWaitingForPlayer(t *testing.T) {
	a := newTestApp()
	a.playing = &player.Process{}
	a.playingCh = store.Channel{Name: "Demo Movie", Type: store.Movie}
	a.playPos, a.playDur = 0, 0
	out := ansiRE.ReplaceAllString(a.renderPlaybar(), "")
	if !strings.Contains(out, "waiting for player") {
		t.Fatalf("trackable mpv item with no data should link-watch, got %q", out)
	}
	// Live has nothing to measure, so it must never claim to be linking.
	a.playingCh = store.Channel{Name: "Demo Live", Type: store.Live}
	if out := ansiRE.ReplaceAllString(a.renderPlaybar(), ""); strings.Contains(out, "waiting for player") {
		t.Fatalf("live must not show the linking state: %q", out)
	}
}
