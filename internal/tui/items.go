package tui

import (
	"fmt"
	"strings"

	"github.com/mertturunc/iptv-tui/internal/store"
)

type groupItem struct {
	label string
	count int
	fav   bool
	// labelW is the rune width reserved for the label. layout() sets it from
	// the group column width, so a wide terminal shows wide group names
	// instead of padding a fixed-width field with empty space.
	labelW int
}

func (g groupItem) Title() string {
	prefix := " "
	if g.fav {
		prefix = glStar
	}
	w := g.labelW
	if w <= 0 {
		// Before the first layout pass there is no column width yet; 10 keeps
		// the row readable rather than collapsing to nothing.
		w = 10
	}
	// Measurable rows: label left filling the column, tabular count right
	// (instrument readout). The count column stays aligned because the label
	// field is a fixed width computed from the pane.
	return fmt.Sprintf("%s%-*s %5d", prefix, w, ellipsize(g.label, w), g.count)
}

func (g groupItem) Description() string { return "" }
func (g groupItem) FilterValue() string { return g.label }

type channelItem struct {
	ch  store.Channel
	fav bool
	now string
	num int
}

func (i channelItem) Title() string {
	name := i.ch.Name
	numStr := ""
	if i.num > 0 {
		numStr = fmt.Sprintf("[%03d] ", i.num)
	}
	prefix := " "
	if i.fav {
		prefix = glStar
	}
	return numStr + prefix + name
}

func (i channelItem) Description() string {
	parts := []string{}
	if i.ch.Group != "" {
		parts = append(parts, i.ch.Group)
	}
	if i.now != "" {
		parts = append(parts, i.now)
	}
	return strings.Join(parts, " "+glSep+" ")
}

func (i channelItem) FilterValue() string { return i.ch.Name }

type episodeItem struct {
	ch store.Channel
}

func (e episodeItem) Title() string {
	prefix := " "
	if e.ch.Season > 0 {
		prefix = fmt.Sprintf("S%02d E%02d ", e.ch.Season, e.ch.Episode)
	}
	return prefix + e.ch.Name
}

func (e episodeItem) Description() string { return "" }
func (e episodeItem) FilterValue() string { return e.ch.Name }

type sourceListItem struct {
	src  store.Source
	idx  int
	load bool
}

func (s sourceListItem) Title() string {
	typ := "m3u"
	if s.src.Kind.IsXtream() {
		typ = "xtream"
	}
	label := fmt.Sprintf("[%s] %s", typ, s.src.Name)
	if s.load {
		return label + " ..."
	}
	return label
}

func (s sourceListItem) Description() string {
	if s.src.Kind.IsM3U() {
		u := s.src.URL
		if len(u) > 40 {
			u = u[:37] + "..."
		}
		return u
	}
	return s.src.Server
}
func (s sourceListItem) FilterValue() string { return s.src.Name }

type epgProgramItem struct {
	prog   store.Program
	now    bool
	future bool
}

func (e epgProgramItem) Title() string {
	if e.now {
		return fmt.Sprintf(glPlay+" %02d:%02d-%02d:%02d  %s", e.prog.Start.Hour(), e.prog.Start.Minute(),
			e.prog.Stop.Hour(), e.prog.Stop.Minute(), e.prog.Title)
	}
	prefix := "  "
	if e.future {
		prefix = glSlot
	}
	return fmt.Sprintf("%s %02d:%02d-%02d:%02d  %s", prefix, e.prog.Start.Hour(), e.prog.Start.Minute(),
		e.prog.Stop.Hour(), e.prog.Stop.Minute(), e.prog.Title)
}

func (e epgProgramItem) Description() string {
	if e.prog.Desc == "" {
		return ""
	}
	d := e.prog.Desc
	if len(d) > 80 {
		d = d[:77] + "..."
	}
	return "  " + d
}
func (e epgProgramItem) FilterValue() string { return e.prog.Title }

// ellipsize truncates s to maxRunes UTF-8 runes, appending the truncation mark
// if needed. The mark is a token, so ASCII mode spends three columns where
// Unicode spends one; either way the result is exactly maxRunes wide, which is
// what the fixed-width label fields above assume.
func ellipsize(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	mark := []rune(glEllipsis)
	if len(mark) >= maxRunes {
		return string(mark[:maxRunes])
	}
	return string(runes[:maxRunes-len(mark)]) + glEllipsis
}
