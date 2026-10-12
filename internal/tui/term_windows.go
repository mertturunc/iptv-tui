//go:build windows

package tui

import (
	"golang.org/x/sys/windows"
)

// currentSize returns the live terminal window size via the console API.
// Used as a fallback when bubbletea's WindowSizeMsg reports 0 (some shells).
func currentSize() (w, h int) {
	hw, err := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE)
	if err != nil {
		return 0, 0
	}
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(hw, &info); err != nil {
		return 0, 0
	}
	return int(info.Window.Right - info.Window.Left + 1),
		int(info.Window.Bottom - info.Window.Top + 1)
}
