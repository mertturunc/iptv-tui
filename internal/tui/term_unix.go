//go:build !windows

package tui

// currentSize is a stub on non-Windows; bubbletea reports the size reliably.
func currentSize() (w, h int) {
	return 0, 0
}
