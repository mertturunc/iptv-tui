package tui

import (
	"fmt"
	"io"
	"os"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// HEADEND world. The structure stays — graphite faces on a near-black ground,
// hairline rules, tabular numerals, per-source signal hue — while the tokens
// below swap the whole room between 8 palettes: Gruvbox Dark Hard (default),
// 3 more vim-popular darks, and 4 lights.
type Theme struct {
	Name string
	Dark bool
	// Ground/face/rule/ink/dim are the room; signal is the one accent;
	// live/fault/warn are status only. Rotation gives each source its hue.
	Ground, Face, Rule, Ink, Dim, Signal, Live, Fault, Warn string
	Rotation                                                []string
}

// Themes is the ←/→ ring in the help sheet, darks first. Index 0 is the
// default the app boots with when no theme was saved yet.
var Themes = []Theme{
	{
		Name: "Gruvbox Dark Hard", Dark: true,
		Ground: "#1d2021", Face: "#282828", Rule: "#504945",
		Ink: "#ebdbb2", Dim: "#a89984",
		Signal: "#fabd2f", Live: "#b8bb26", Fault: "#fb4934", Warn: "#fe8019",
		Rotation: []string{"#fabd2f", "#83a598", "#8ec07c", "#d3869b", "#fe8019", "#fb4934", "#b8bb26", "#d5c4a1"},
	},
	{
		Name: "Dracula", Dark: true,
		Ground: "#282a36", Face: "#44475a", Rule: "#6272a4",
		Ink: "#f8f8f2", Dim: "#8390b7",
		Signal: "#bd93f9", Live: "#50fa7b", Fault: "#ff5555", Warn: "#ffb86c",
		Rotation: []string{"#bd93f9", "#ff79c6", "#8be9fd", "#50fa7b", "#f1fa8c", "#ffb86c", "#ff5555", "#6272a4"},
	},
	{
		Name: "Nord", Dark: true,
		Ground: "#2e3440", Face: "#3b4252", Rule: "#4c566a",
		Ink: "#eceff4", Dim: "#909cb4",
		Signal: "#88c0d0", Live: "#a3be8c", Fault: "#bf616a", Warn: "#d08770",
		Rotation: []string{"#88c0d0", "#81a1c1", "#8fbcbb", "#a3be8c", "#ebcb8b", "#b48ead", "#bf616a", "#d08770"},
	},
	{
		Name: "Tokyo Night", Dark: true,
		Ground: "#1a1b26", Face: "#292e42", Rule: "#3b4261",
		Ink: "#c0caf5", Dim: "#a9b1d6",
		Signal: "#7aa2f7", Live: "#9ece6a", Fault: "#f7768b", Warn: "#ff9e64",
		Rotation: []string{"#7aa2f7", "#bb9af7", "#7dcfff", "#9ece6a", "#e0af68", "#ff9e64", "#f7768b", "#a9b1d6"},
	},
	{
		Name: "Gruvbox Light Hard", Dark: false,
		Ground: "#f9f5d7", Face: "#fbf1c7", Rule: "#d5c4a1",
		Ink: "#3c3836", Dim: "#7a6d62",
		Signal: "#9a6411", Live: "#79740e", Fault: "#9d0006", Warn: "#af3a03",
		Rotation: []string{"#9a6411", "#076678", "#427b58", "#8f3f71", "#af3a03", "#9d0006", "#79740e", "#7a6d62"},
	},
	{
		Name: "Solarized Light", Dark: false,
		Ground: "#fdf6e3", Face: "#eee8d5", Rule: "#93a1a1",
		Ink: "#586e75", Dim: "#687272",
		Signal: "#2076b2", Live: "#849700", Fault: "#dc322f", Warn: "#cb4b16",
		Rotation: []string{"#2076b2", "#299e95", "#849700", "#b38800", "#cb4b16", "#d33682", "#6c71c4", "#dc322f"},
	},
	{
		Name: "One Light", Dark: false,
		Ground: "#fafafa", Face: "#f0f0f0", Rule: "#d0d0d0",
		Ink: "#383a42", Dim: "#727277",
		Signal: "#3a6ddc", Live: "#50a14f", Fault: "#e45649", Warn: "#c18401",
		Rotation: []string{"#3a6ddc", "#0184bc", "#50a14f", "#c18401", "#a626a4", "#e45649", "#383a42", "#727277"},
	},
	{
		Name: "Catppuccin Latte", Dark: false,
		Ground: "#eff1f5", Face: "#e6e9ef", Rule: "#bcc0cc",
		Ink: "#4c4f69", Dim: "#6a6d82",
		Signal: "#8839ef", Live: "#3f9e2b", Fault: "#d20f39", Warn: "#ec5d0a",
		Rotation: []string{"#8839ef", "#cc67b1", "#0494ce", "#179299", "#3f9e2b", "#c07a19", "#ec5d0a", "#d20f39"},
	},
}

// themeIdx is the active ring position. Everything (colors, derived styles,
// signal rotation) follows it via ApplyThemeIndex.
var themeIdx int

// CurrentThemeName names the active palette for the help footer and status.
func CurrentThemeName() string { return Themes[themeIdx].Name }

// ThemeIndexByName resolves a saved name, or -1 when it names no palette.
func ThemeIndexByName(name string) int {
	for i, t := range Themes {
		if t.Name == name {
			return i
		}
	}
	return -1
}

// ApplyThemeIndex paints the room with Themes[i] (wrapping) and rebuilds
// every style derived from the tokens, so a switch mid-session repaints the
// whole console on the next frame. Callers invalidate cached chrome too.
func ApplyThemeIndex(i int) {
	themeIdx = ((i % len(Themes)) + len(Themes)) % len(Themes)
	t := Themes[themeIdx]
	colGround = lipgloss.Color(t.Ground)
	colFace = lipgloss.Color(t.Face)
	colRule = lipgloss.Color(t.Rule)
	colInk = lipgloss.Color(t.Ink)
	colDim = lipgloss.Color(t.Dim)
	colSignal = lipgloss.Color(t.Signal)
	colLive = lipgloss.Color(t.Live)
	colFault = lipgloss.Color(t.Fault)
	colWarn = lipgloss.Color(t.Warn)
	headendRule = lipgloss.NewStyle().Foreground(colRule)
	panelStyle = lipgloss.NewStyle().Foreground(colInk)
	statusStyle = lipgloss.NewStyle().Foreground(colDim)
}

// ApplyStoredTheme boots the saved palette, falling back to the default
// (index 0) for empty or unknown names. Called once at startup, before New.
func ApplyStoredTheme(name string) {
	if i := ThemeIndexByName(name); i >= 0 {
		ApplyThemeIndex(i)
		return
	}
	ApplyThemeIndex(0)
}

// CycleTheme steps the ring and reports the new index.
func CycleTheme(delta int) int {
	ApplyThemeIndex(themeIdx + delta)
	SetTermBackground()
	return themeIdx
}

// SetTermBackground tints the whole terminal window to the active ground via
// OSC 11, so even cells outside our frame match the palette. Unsupported
// terminals ignore the sequence; Ascii profile skips it entirely.
func SetTermBackground() { writeTermBackground(os.Stdout) }

// ResetTermBackground restores the terminal's default background (OSC 111).
// Call it once the TUI exits so no tint leaks into the shell.
func ResetTermBackground() {
	if asciiMode || lipgloss.ColorProfile() == termenv.Ascii {
		return
	}
	fmt.Fprint(os.Stdout, "\x1b]111\x07")
}

func writeTermBackground(w io.Writer) {
	if asciiMode || lipgloss.ColorProfile() == termenv.Ascii {
		return
	}
	c, ok := hexRGB(Themes[themeIdx].Ground)
	if !ok {
		return
	}
	fmt.Fprintf(w, "\x1b]11;rgb:%02x/%02x/%02x\x07", c[0], c[1], c[2])
}

var (
	colGround, colFace, colRule, colInk, colDim lipgloss.Color
	colSignal, colLive, colFault, colWarn       lipgloss.Color
)

// Per-source signal hue: each source owns a channel-strip color drawn from
// the active theme's rotation (PER-SOURCE SIGNAL). Index -1 falls back to
// the base signal.
func signalFor(idx int) lipgloss.Color {
	if idx < 0 {
		return colSignal
	}
	hues := Themes[themeIdx].Rotation
	return lipgloss.Color(hues[idx%len(hues)])
}

func init() { ApplyThemeIndex(0) }

// headendRule renders horizontal/vertical hairlines (1px rules).
// Rebuilt by ApplyThemeIndex; never constructed here so a switch repaints it.
var headendRule lipgloss.Style

// Focus chrome (FOCUS CHROME).
//
// A console with two rails has to answer one question at a glance: which rail
// takes my next keystroke? The focused rail wears the source's signal — a
// filled badge over its title and a filled selection bar — while the rail
// without focus drops to an unlit graphite bar so only the live rail shouts.
// The hue itself still roves per source, so this is one accent in two
// intensities, never a second color.
//
// selectStyle is the selection bar: lit for the focused rail, unlit for the
// other one. Both keep the same geometry so a selection never shifts a column.
func selectStyle(hue lipgloss.Color, focused bool) lipgloss.Style {
	if !focused {
		return lipgloss.NewStyle().
			Background(colFace).
			Foreground(colInk).
			Padding(0, 1)
	}
	return lipgloss.NewStyle().
		Background(hue).
		Foreground(colGround).
		Bold(true).
		Padding(0, 1)
}

// paneTitleStyle is a pane's title badge. The focused rail gets the signal
// fill; the idle rail is engraved on the ground with no fill, so the title
// recedes instead of competing for attention. Both keep the same padding, so
// moving focus never shifts a column.
func paneTitleStyle(hue lipgloss.Color, focused bool) lipgloss.Style {
	if !focused {
		return lipgloss.NewStyle().Foreground(colDim).Padding(0, 1)
	}
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(colGround).
		Background(hue).
		Padding(0, 1)
}

// paneRuleStyle is the hairline between the rails: signal where the two panes
// meet and focus continues, plain rule elsewhere.
func paneRuleStyle(hue lipgloss.Color, focused bool) lipgloss.Style {
	if !focused {
		return headendRule
	}
	return lipgloss.NewStyle().Foreground(hue)
}

// signal returns the active source's hue (falls back to base yellow).
func (m *App) signal() lipgloss.Color { return signalFor(m.activeIdx) }

// Glyph tokens (GLYPH TOKENS).
//
// A terminal chooses its own font; the app can only send characters. Box
// drawing survives almost everywhere, but a few marks do not: U+259A (the
// nameplate flag), U+27E8/9 (the help sheet's angle brackets) and the geometric
// triangles are missing from stock Windows Terminal faces such as Consolas,
// where they fall back to tofu boxes. SetASCII swaps every token for a
// plain-ASCII equivalent so the console stays legible on a font we do not get
// to pick. Unicode stays the default: it is what the world is drawn with.
//
// Nothing here is styling. These are the only places a character is chosen.
var (
	glyphFlag           = "▚" // nameplate flag
	glyphPlaying        = "▶" // on-air, playing
	glyphFavorite       = "★" // favorite
	glyphCursor         = "▸" // form field cursor, channel readout
	glyphEmptySlot      = "○" // unused EPG slot
	glyphHorizontalRule = "─" // hairline
	glyphVerticalRule   = "│" // pane divider
	glyphMeterFilled    = "━" // progress meter, played
	glyphMeterEmpty     = "─" // progress meter, remaining
	glyphSeparator      = "·" // hairline dot between hints
	glyphEllipsis       = "…" // truncation mark
	glyphDash           = "—" // em dash in status copy
	glyphArrowUp        = "↑"
	glyphArrowDown      = "↓"
	glyphArrowUpDown    = glyphArrowUp + glyphArrowDown
	glyphArrowLeft      = "◄"
	glyphArrowRight     = "►"
	glyphAngleOpen      = "⟨"
	glyphAngleClose     = "⟩"
	glyphSparkLevels    = "▁▂▃▄▅▆▇█" // sparkline levels, ascending
)

// SetASCII puts the console in plain ASCII. Called once at startup, before any
// view is built, so the tokens are settled by the time the first frame renders.
func SetASCII(on bool) {
	asciiMode = on
	if on {
		glyphFlag, glyphPlaying, glyphFavorite, glyphCursor, glyphEmptySlot = "=", ">", "*", ">", "-"
		glyphHorizontalRule, glyphVerticalRule, glyphMeterFilled, glyphMeterEmpty, glyphSeparator = "-", "|", "#", "-", "-"
		glyphEllipsis, glyphDash = "...", "--"
		glyphArrowUp, glyphArrowDown = "^", "v"
		glyphArrowUpDown = glyphArrowUp + glyphArrowDown
		glyphArrowLeft, glyphArrowRight = "<", ">"
		glyphAngleOpen, glyphAngleClose = "<", ">"
		glyphSparkLevels = ".:-=+*#@"
		return
	}
	glyphFlag, glyphPlaying, glyphFavorite, glyphCursor, glyphEmptySlot = "▚", "▶", "★", "▸", "○"
	glyphHorizontalRule, glyphVerticalRule, glyphMeterFilled, glyphMeterEmpty, glyphSeparator = "─", "│", "━", "─", "·"
	glyphEllipsis, glyphDash = "…", "—"
	glyphArrowUp, glyphArrowDown = "↑", "↓"
	glyphArrowUpDown = glyphArrowUp + glyphArrowDown
	glyphArrowLeft, glyphArrowRight = "◄", "►"
	glyphAngleOpen, glyphAngleClose = "⟨", "⟩"
}

// spinnerASCII replaces bubbles' default Dot spinner. Dot is braille, which is
// exactly as exotic as it looks and as likely to be absent from the font; the
// classic terminal rotation is the one thing every console can draw.
var spinnerASCII = spinner.Spinner{
	Frames: []string{"|", "/", "-", "\\"},
	FPS:    spinner.Dot.FPS,
}

// ---- shared helpers (consumed across view/overlays) ----

var (
	// Rebuilt by ApplyThemeIndex for the same reason as headendRule.
	panelStyle  lipgloss.Style
	statusStyle lipgloss.Style

	// asciiMode also decides how panels draw their frame: box-drawing corners
	// are safe, but the promise of --ascii is "nothing outside 7-bit", so the
	// border swaps with the glyphs.
	asciiMode bool
)

func dim(s string) string     { return statusStyle.Render(s) }
func muted(s string) string   { return lipgloss.NewStyle().Foreground(colDim).Render(s) }
func success(s string) string { return lipgloss.NewStyle().Foreground(colLive).Render(s) }
func failure(s string) string { return lipgloss.NewStyle().Foreground(colFault).Render(s) }
func warning(s string) string { return lipgloss.NewStyle().Foreground(colWarn).Render(s) }

// panelBox wraps content in a hairline 1px frame with the signal accent.
func panelBox(width, height int, content string) string {
	border := lipgloss.NormalBorder()
	if asciiMode {
		border = lipgloss.ASCIIBorder()
	}
	return lipgloss.NewStyle().
		Border(border).
		BorderForeground(colSignal).
		Width(width-2).
		Height(height-2).
		Padding(1, 1).
		Render(content)
}

// cycleTheme steps the palette ring, repaints cached chrome, and remembers
// the choice in settings so the next boot opens on it.
func (m *App) cycleTheme(delta int) {
	CycleTheme(delta)
	m.chromeHue = "" // force applyChrome to repaint for the new signal
	m.spinner.Style = lipgloss.NewStyle().Foreground(colSignal)
	m.cfg.SetTheme(CurrentThemeName())
	m.cfg.Save()
	m.setStatus("theme: "+CurrentThemeName(), false)
}

// sig renders s in the given signal hue.
func sig(hue lipgloss.Color, s string) string {
	return lipgloss.NewStyle().Foreground(hue).Bold(false).Render(s)
}

// sigBold renders s bold in the given signal hue.
func sigBold(hue lipgloss.Color, s string) string {
	return lipgloss.NewStyle().Foreground(hue).Bold(true).Render(s)
}
