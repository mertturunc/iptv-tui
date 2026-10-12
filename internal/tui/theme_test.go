package tui

import (
	"bytes"
	"math"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/mertturunc/iptv-tui/internal/store"
)

func TestThemeRing(t *testing.T) {
	defer ApplyThemeIndex(0)
	if len(Themes) != 8 {
		t.Fatalf("want 8 themes, got %d", len(Themes))
	}
	if Themes[0].Name != "Gruvbox Dark Hard" {
		t.Fatalf("default must stay Gruvbox Dark Hard, got %q", Themes[0].Name)
	}
	seen := map[string]bool{}
	darks, lights := 0, 0
	for _, th := range Themes {
		if th.Name == "" {
			t.Fatal("theme with empty name")
		}
		if seen[th.Name] {
			t.Fatalf("duplicate theme %q", th.Name)
		}
		seen[th.Name] = true
		if th.Dark {
			darks++
		} else {
			lights++
		}
		for _, tok := range []string{th.Ground, th.Face, th.Rule, th.Ink, th.Dim, th.Signal, th.Live, th.Fault, th.Warn} {
			if tok == "" {
				t.Fatalf("%s has an empty token", th.Name)
			}
		}
		if len(th.Rotation) != 8 {
			t.Fatalf("%s rotation has %d hues, want 8", th.Name, len(th.Rotation))
		}
	}
	if darks != 4 || lights != 4 {
		t.Fatalf("want 4 dark + 4 light, got %d + %d", darks, lights)
	}
}

func TestThemeCycleWraps(t *testing.T) {
	defer ApplyThemeIndex(0)
	ApplyThemeIndex(0)
	if got := CycleTheme(-1); got != len(Themes)-1 {
		t.Fatalf("cycle -1 from 0 = %d, want last", got)
	}
	if got := CycleTheme(1); got != 0 {
		t.Fatalf("cycle +1 from last = %d, want 0", got)
	}
	if name := CurrentThemeName(); name != Themes[0].Name {
		t.Fatalf("current = %q, want default", name)
	}
}

func TestApplyStoredTheme(t *testing.T) {
	defer ApplyThemeIndex(0)
	ApplyStoredTheme("")
	if CurrentThemeName() != Themes[0].Name {
		t.Fatalf("empty name should boot default, got %q", CurrentThemeName())
	}
	ApplyStoredTheme("No Such Theme")
	if CurrentThemeName() != Themes[0].Name {
		t.Fatalf("unknown name should boot default, got %q", CurrentThemeName())
	}
	ApplyStoredTheme("Dracula")
	if CurrentThemeName() != "Dracula" {
		t.Fatalf("stored Dracula should boot, got %q", CurrentThemeName())
	}
	if got := string(colSignal); got != "#bd93f9" {
		t.Fatalf("dracula signal = %q, want #bd93f9", got)
	}
}

func TestHelpThemeKeys(t *testing.T) {
	t.Chdir(t.TempDir()) // cycleTheme saves settings; keep it out of the repo
	defer ApplyThemeIndex(0)
	ApplyThemeIndex(0)
	a := newTestApp()
	a.mode = modeHelp
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRight})
	got := m.(*App)
	if got.mode != modeHelp {
		t.Fatal("right arrow should stay in help while previewing")
	}
	if CurrentThemeName() == Themes[0].Name {
		t.Fatal("right arrow should advance the palette")
	}
	if got.cfg.Cfg.Settings.Theme != CurrentThemeName() {
		t.Fatalf("choice not remembered: %q", got.cfg.Cfg.Settings.Theme)
	}
	m, _ = got.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if CurrentThemeName() != Themes[0].Name {
		t.Fatalf("left arrow should step back, at %q", CurrentThemeName())
	}
	m, _ = m.(*App).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.(*App).mode != modeMain {
		t.Fatal("esc should leave help")
	}
}

func TestSourcesOverlayRepaintsOnTheme(t *testing.T) {
	t.Chdir(t.TempDir()) // cycleTheme saves settings; keep it out of the repo
	defer ApplyThemeIndex(0)
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(prev)
	ApplyThemeIndex(0)
	a := newTestApp()
	a.cfg.AddSource(store.Source{ID: "a", Name: "demo", Kind: "m3u", URL: "x"})
	a.openSources() // overlay built under Gruvbox Dark Hard
	a.mode = modeHelp
	m, _ := a.Update(tea.KeyMsg{Type: tea.KeyRight}) // → Dracula
	got := m.(*App)
	if CurrentThemeName() != "Dracula" {
		t.Fatalf("at %q, want Dracula", CurrentThemeName())
	}
	got.openSources() // reopen under the new palette
	if bg := colorName(got.srcOverlay.Styles.Title.GetBackground()); bg != "#bd93f9" {
		t.Fatalf("overlay title bg = %q, want dracula signal", bg)
	}
	// The selected row wears the delegate's selection bar: dracula bg in,
	// gruvbox bg out.
	view := got.srcOverlay.View()
	if !strings.Contains(view, "48;2;189;147;249") {
		t.Fatal("overlay rows missing the dracula selection color")
	}
	if strings.Contains(view, "48;2;250;189;47") {
		t.Fatal("stale gruvbox selection survived the theme switch")
	}
}

func TestWriteTermBackground(t *testing.T) {
	defer ApplyThemeIndex(0)
	prev := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(prev)

	ApplyThemeIndex(0) // Gruvbox Dark Hard ground #1d2021
	lipgloss.SetColorProfile(termenv.TrueColor)
	var buf bytes.Buffer
	writeTermBackground(&buf)
	if got := buf.String(); got != "\x1b]11;rgb:1d/20/21\x07" {
		t.Fatalf("OSC 11 = %q", got)
	}

	lipgloss.SetColorProfile(termenv.Ascii)
	buf.Reset()
	writeTermBackground(&buf)
	if buf.Len() != 0 {
		t.Fatalf("ascii profile must not tint the terminal, got %q", buf.String())
	}
}

// ---------- WCAG contrast ----------

// relLuminance returns the WCAG relative luminance of an sRGB hex color.
func relLuminance(hex string) float64 {
	c, ok := hexRGB(hex)
	if !ok {
		return -1
	}
	lin := func(v int) float64 {
		s := float64(v) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c[0]) + 0.7152*lin(c[1]) + 0.0722*lin(c[2])
}

// contrastRatio returns the WCAG contrast ratio of two hex colors.
func contrastRatio(a, b string) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	if la < 0 || lb < 0 {
		return -1
	}
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestThemeContrast pins every palette to accessible numbers: body/secondary
// text and the signal at AA normal-text level (4.5), status and per-source
// accents at AA large-text level (3.0) since they also serve as fills.
func TestThemeContrast(t *testing.T) {
	for _, th := range Themes {
		if got := contrastRatio(th.Ink, th.Ground); got < 4.5 {
			t.Errorf("%s: ink/ground = %.2f, want >= 4.5", th.Name, got)
		}
		if got := contrastRatio(th.Dim, th.Ground); got < 4.5 {
			t.Errorf("%s: dim/ground = %.2f, want >= 4.5", th.Name, got)
		}
		if got := contrastRatio(th.Signal, th.Ground); got < 4.5 {
			t.Errorf("%s: signal/ground = %.2f, want >= 4.5", th.Name, got)
		}
		for _, role := range []struct {
			name, color string
		}{{"live", th.Live}, {"fault", th.Fault}, {"warn", th.Warn}} {
			if got := contrastRatio(role.color, th.Ground); got < 3.0 {
				t.Errorf("%s: %s/ground = %.2f, want >= 3.0", th.Name, role.name, got)
			}
		}
		for i, h := range th.Rotation {
			if got := contrastRatio(h, th.Ground); got < 3.0 {
				t.Errorf("%s: rotation[%d] %s/ground = %.2f, want >= 3.0", th.Name, i, h, got)
			}
		}
	}
}
