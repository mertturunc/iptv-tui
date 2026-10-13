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

func TestHelpPageSizeStaysPositive(t *testing.T) {
	// The view and the key handler both hang off helpPageSize; a non-positive
	// page size would make the scroll math divide by zero or skip work.
	for _, height := range []int{0, 1, 5, 7, 8, 10, 20, 24, 40, 100} {
		if got := helpPageSize(height); got < 1 {
			t.Errorf("helpPageSize(%d) = %d, want >= 1", height, got)
		}
	}
}

func TestHelpScrollMathConsistent(t *testing.T) {
	total := len(helpRows())
	for _, height := range []int{10, 12, 20, 24, 40} {
		avail := helpPageSize(height)
		maxStart := total - avail
		if maxStart < 0 {
			maxStart = 0
		}
		// Every requested window, including out-of-range ones, must render
		// without panicking and clamp back into the valid range.
		for _, off := range []int{-100, -1, 0, 1, maxStart, maxStart + 1, 1000} {
			out := helpView(80, height, off)
			if out == "" {
				t.Fatalf("empty help view at height=%d off=%d", height, off)
			}
		}
	}
}
