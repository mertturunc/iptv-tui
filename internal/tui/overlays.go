package tui

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// ---------- Add source form ----------

type addForm struct {
	active int
	labels []string
	fields []textinput.Model
	width  int
	kind   store.SourceKind // m3u | xtream
}

func newAddForm(kind store.SourceKind, width int) *addForm {
	if width < 24 {
		width = 24
	}
	f := &addForm{kind: kind, width: width}
	switch kind {
	case store.SourceM3U:
		f.labels = []string{"Name", "URL or file path", "User-Agent (optional)"}
		f.fields = []textinput.Model{
			newField("My M3U playlist", width, true),
			newField("https://"+glEllipsis+"/playlist.m3u or C:\\path\\list.m3u", width, false),
			newField("custom UA", width, false),
		}
	default:
		f.labels = []string{"Name", "Server URL", "Username", "Password"}
		f.fields = []textinput.Model{
			newField("My XC provider", width, true),
			newField("https://x.example.com:8080", width, false),
			newField("username", width, false),
			newField("password", width, false),
		}
	}
	if len(f.fields) > 0 {
		f.fields[0].Focus()
	}
	return f
}

// Resize applies a new available width to the input fields.
func (f *addForm) Resize(width int) {
	if width <= 0 {
		return
	}
	f.width = width
	for i := range f.fields {
		if width-6 > 0 {
			f.fields[i].Width = width - 6
		}
	}
}

func newField(placeholder string, width int, focus bool) textinput.Model {
	t := textinput.New()
	t.Placeholder = placeholder
	t.Width = width - 6
	if focus {
		t.Focus()
	}
	return t
}

func (f *addForm) Submit() store.Source {
	get := func(i int) string { return strings.TrimSpace(f.fields[i].Value()) }
	src := store.Source{Name: get(0)}
	if src.Name == "" {
		if f.kind == store.SourceM3U {
			src.Name = "M3U playlist"
		} else {
			src.Name = "Xtream portal"
		}
	}
	if f.kind == store.SourceM3U {
		src.Kind = store.SourceM3U
		src.URL = get(1)
		src.UserAgent = get(2)
	} else {
		src.Kind = store.SourceXtream
		src.Server = get(1)
		src.Username = get(2)
		src.Password = get(3)
	}
	src.ID = store.RandomID()
	return src
}

type addFormResult struct {
	src       store.Source
	submitted bool
	cancelled bool
}

func (f *addForm) Update(msg tea.Msg) (addFormResult, tea.Cmd) {
	var cmd tea.Cmd
	var res addFormResult
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		if len(f.fields) > f.active {
			f.fields[f.active], cmd = f.fields[f.active].Update(msg)
		}
		return res, cmd
	}
	switch key.String() {
	case "esc":
		res.cancelled = true
		return res, nil
	case "tab", "down", "enter":
		f.fields[f.active].Blur()
		if f.active < len(f.fields)-1 {
			f.active++
			f.fields[f.active].Focus()
			return res, nil
		}
		if key.String() == "enter" || key.String() == "tab" {
			res.src = f.Submit()
			res.submitted = true
			return res, nil
		}
		return res, nil
	case "up", "shift+tab":
		f.fields[f.active].Blur()
		if f.active > 0 {
			f.active--
			f.fields[f.active].Focus()
		} else {
			f.fields[0].Focus()
		}
		return res, nil
	}
	f.fields[f.active], cmd = f.fields[f.active].Update(msg)
	return res, cmd
}

// View renders the form. A roomy frame stacks the label above its input; a
// short one puts each field on a single line instead, because a form whose last
// field is cut off by the frame is a form you cannot finish filling in.
func (f *addForm) View(width, height int) string {
	// Panel chrome costs four rows: two borders and a line of padding.
	const chrome = 4
	head := 2 // title plus its blank line
	stacked := height >= head+f.stackedRows()+chrome

	// The label column is sized to the frame so nothing wraps mid-phrase.
	labelW := clamp(width/3, 10, 14)

	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", panelStyle.Bold(true).Render("Add playlist / source"))
	for i, fl := range f.fields {
		marker := "  "
		if i == f.active {
			marker = sig(colSignal, glCursor) + " "
		}
		if stacked {
			fmt.Fprintf(&b, "  %s%s\n  %s", marker, f.labels[i], fl.View())
			if i < len(f.fields)-1 {
				b.WriteString("\n\n")
			}
			continue
		}
		// One line per field: the input keeps the rest of the row and is told
		// its new width, so its own padding and cursor still line up.
		room := width - 6 - labelW - 3
		fl.Width = clamp(room, 8, room)
		fmt.Fprintf(&b, "  %s%-*s %s", marker, labelW, f.labels[i], fl.View())
		if i < len(f.fields)-1 {
			b.WriteString("\n")
		}
	}
	content := b.String()
	return panelBox(width, lipgloss.Height(content)+2, content)
}

// stackedRows is how many rows the roomy layout spends on the fields: two each
// plus a blank between neighbours.
func (f *addForm) stackedRows() int {
	if len(f.fields) == 0 {
		return 0
	}
	return len(f.fields)*3 - 1
}

// ---------- EPG panel ----------

type epgView struct {
	title   string
	progs   []store.Program
	scroll  int
	loading bool
	err     string
}

func (e *epgView) View(width, height int) string {
	var b strings.Builder
	if e.loading {
		b.WriteString("loading EPG" + glEllipsis)
	} else if e.err != "" {
		b.WriteString("EPG unavailable: " + e.err)
	} else if len(e.progs) == 0 {
		b.WriteString("No EPG data for this channel.")
	} else {
		now := nowFunc()
		count := 0
		start := e.scroll
		for i := start; i < len(e.progs) && count < height-3; i++ {
			p := e.progs[i]
			title := fmt.Sprintf("%02d:%02d-%02d:%02d  %s",
				p.Start.Hour(), p.Start.Minute(), p.Stop.Hour(), p.Stop.Minute(), ellipsize(p.Title, 40))
			line := "  " + title
			if p.Start.Before(now) && p.Stop.After(now) {
				line = sig(colLive, glPlay) + " " + title
			}
			b.WriteString(line + "\n")
			if p.Desc != "" {
				b.WriteString("    " + ellipsize(strings.ReplaceAll(p.Desc, "\n", " "), width-6) + "\n")
			}
			count++
		}
	}
	inner := b.String()
	return panelBox(width, lipgloss.Height(inner)+2, panelStyle.Bold(true).Render("EPG "+glSep+" "+ellipsize(e.title, 30))+"\n\n"+inner+"  "+dim("esc=back  "+glUpDown+"=scroll"))
}

// ---------- Help ----------

func helpView(width, height int) string {
	rows := []struct{ k, what string }{
		{glUpDown + "/pgup/pgdn", "navigate list"},
		{"tab / " + glLeft + " " + glRight, "switch group " + glAngleOpen + glAngleClose + " channel panes"},
		{"enter", "play channel / expand series"},
		{"o", "play channel"},
		{"c", "send channel to Google TV (Chromecast)"},
		{"f", "toggle favorite"},
		{"/", "filter channels"},
		{"g", "jump to group pane"},
		{"backspace", "back to series list"},
		{"e", "EPG guide for selected channel"},
		{"0-9", "pick channel by number (then enter)"},
		{"1 2 3", "media tabs: Live / Movies / Series (XC)"},
		{"s", "manage sources"},
		{"a", "add source"},
		{"r", "reload current source"},
		{"x", "stop playback (mpv, TV)"},
		{"m", "toggle mpv keep-on-quit"},
		{"?", "this help"},
		{"q / esc", "quit"},
		{glLeft + " " + glRight, "switch theme"},
	}
	// A short terminal cannot hold the whole sheet. Show what fits, in the order
	// it matters, and count the rest in the footer: cutting bindings off in
	// silence is how a console loses trust, saying so is how it keeps it.
	shown, hidden := rows, 0
	if avail := height - 7; avail > 0 && avail < len(rows) {
		shown, hidden = rows[:avail], len(rows)-avail
	}
	// Size the key column to the sheet so no description wraps mid-phrase, and
	// clip any that still do not fit rather than letting them break the frame.
	keyW := clamp(width/3, 10, 22)
	room := width - keyW - 5
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", panelStyle.Bold(true).Render("Key bindings"))
	for _, r := range shown {
		fmt.Fprintf(&b, "  %-*s %s\n", keyW, sigBold(colSignal, r.k), ellipsize(r.what, room))
	}
	keys := "esc=back  " + glSep + "  " + glLeft + " " + glRight + " theme: " + CurrentThemeName()
	if hidden > 0 {
		keys += fmt.Sprintf("  %s  %d more below, resize to read", glSep, hidden)
	}
	return panelBox(width, lipgloss.Height(b.String())+2, b.String()+"  "+dim(keys))
}

// ---------- mpv setup ----------

// mpvForm asks for a player binary path when none of mpv, VLC, or MPC-HC is
// found. It shows how to install mpv on the current OS; entering a path saves
// it to settings, skipping leaves browsing working and playback failing loudly.
type mpvForm struct {
	path  textinput.Model
	width int
}

func mpvPlaceholder() string {
	if runtime.GOOS == "windows" {
		return `C:\Program Files\mpv\mpv.exe`
	}
	return "/usr/bin/mpv"
}

func newMpvForm(width int) *mpvForm {
	if width < 24 {
		width = 24
	}
	f := &mpvForm{width: width}
	f.path = newField(mpvPlaceholder(), width, true)
	f.path.Focus()
	return f
}

// Resize applies a new available width to the input field.
func (f *mpvForm) Resize(width int) {
	if width <= 0 {
		return
	}
	f.width = width
	if width-6 > 0 {
		f.path.Width = width - 6
	}
}

func (f *mpvForm) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	f.path, cmd = f.path.Update(msg)
	return cmd
}

// View renders install guidance plus the path field. Every line comes from
// player.InstallHelp so the CLI and the TUI never disagree.
func (f *mpvForm) View(width, height int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", panelStyle.Bold(true).Render("no player found"))
	for _, line := range player.InstallHelp() {
		fmt.Fprintf(&b, "  %s\n", ellipsize(line, width-4))
	}
	fmt.Fprintf(&b, "\n  %s\n  %s", "Already have mpv, VLC, or MPC-HC somewhere unusual? Paste its full path:",
		f.path.View())
	content := b.String()
	_ = height
	return panelBox(width, lipgloss.Height(content)+2,
		content+"\n  "+dim("enter=save path  esc=skip (browse without playback)"))
}
