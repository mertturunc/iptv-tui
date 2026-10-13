package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestHelpScrollsWithArrows(t *testing.T) {
	a := newTestApp()
	a.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	a.mode = modeHelp
	a.helpScroll = 0
	before := a.View()
	if strings.Contains(before, "more below, resize to read") {
		t.Fatal("obsolete resize hint still present")
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyDown})
	after := a.View()
	if after == before {
		t.Fatal("down arrow did not scroll the help sheet")
	}
	a.handleKey(tea.KeyMsg{Type: tea.KeyUp})
	if restored := a.View(); restored != before {
		t.Fatal("up arrow did not scroll the help sheet back")
	}
	// Short terminals keep the frame exact while scrolling.
	a.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if got := lipgloss.Height(a.View()); got != 20 {
		t.Fatalf("help frame height = %d, want 20", got)
	}
}
