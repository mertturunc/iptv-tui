package tui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/mertturunc/iptv-tui/internal/player"
	"github.com/mertturunc/iptv-tui/internal/store"
)

// ---------- playback ----------
// Everything that starts, stops, or records mpv (and its VLC/MPC-HC
// fallbacks) lives here: resume math, header fallback, launch/stop, player
// lifecycle messages, and the setup form shown when no backend is runnable.

// playLabel names what mpv is playing, folding a series episode into its
// SxxExx code the way the rack labels channels.
func playLabel(ch store.Channel) string {
	if ch.Season > 0 {
		return fmt.Sprintf("S%02dE%02d %s", ch.Season, ch.Episode, ch.Name)
	}
	return ch.Name
}

func (m *App) resumePosition(ch store.Channel) time.Duration {
	if !ch.IsPositionTrackable() {
		return 0
	}
	h, ok := m.cfg.GetHistory(ch.Key)
	if !ok {
		return 0
	}
	if h.Position <= 30*time.Second {
		return 0
	}
	if h.Duration > 0 && h.Position > h.Duration-30*time.Second {
		return 0
	}
	return h.Position
}

// resolveHeader implements the channel → source → global fallback shared by
// the User-Agent and Referer lookups below.
func (m *App) resolveHeader(ch store.Channel, fromChannel string, fromSource func(*store.Source) string, fallback string) string {
	if fromChannel != "" {
		return fromChannel
	}
	if src := m.cfg.FindSource(ch.SourceID); src != nil {
		if v := fromSource(src); v != "" {
			return v
		}
	}
	return fallback
}

func (m *App) effectiveUA(ch store.Channel) string {
	return m.resolveHeader(ch, ch.UserAgent,
		func(src *store.Source) string { return src.UserAgent },
		m.cfg.DefaultUserAgent())
}

func (m *App) effectiveReferer(ch store.Channel) string {
	return m.resolveHeader(ch, ch.Referer,
		func(src *store.Source) string { return src.Referer }, "")
}

func (m *App) launchChannel(ch store.Channel) {
	_, kind, ok := player.Detect(m.cfg.MpvPath())
	if !ok {
		m.openMpvSetup()
		return
	}
	if m.playing != nil {
		m.stopMpv()
	}
	start := m.resumePosition(ch)
	if kind != player.KindMpv {
		// Only mpv reports position over IPC, so resume has nothing to
		// record for VLC/MPC-HC: they always start at 0 by design.
		start = 0
	}
	customArgs := player.SplitArgs(m.cfg.MpvArgs())
	req := player.Request{
		URL:           ch.URL,
		Title:         ch.Name,
		UserAgent:     m.effectiveUA(ch),
		Referer:       m.effectiveReferer(ch),
		Start:         start,
		CustomArgs:    customArgs,
		MpvPath:       m.cfg.MpvPath(),
		TrackPosition: ch.IsPositionTrackable(),
	}
	m.busy = true
	m.busyMsg = "launching mpv" + glyphEllipsis
	m.status = ""
	go func() {
		proc, err := player.Launch(req)
		m.send(playerMsg{ch: ch, proc: proc, err: err})
	}()
}

func (m *App) stopMpv() {
	if m.playing != nil {
		m.playing.Stop()
	}
}

// playerName names the running backend for status lines.
func (m *App) playerName() string {
	if m.playing != nil {
		return m.playing.PlayerName()
	}
	return "mpv"
}

func (m *App) onPlayerMsg(v playerMsg) {
	m.setBusy(false, "")
	if v.err != nil {
		m.setStatus("mpv error: "+v.err.Error(), true)
		return
	}
	p := v.proc
	m.playing = p
	m.playingCh = v.ch
	ch := v.ch
	p.OnLog = func(line string, isErr bool) {
		m.send(playerLogMsg{line: line, isErr: isErr})
	}
	p.OnExit = func() {
		m.send(playerExitedMsg{ch: ch, proc: p})
	}
	label := playLabel(ch)
	m.setStatus(glyphPlaying+" "+v.proc.PlayerName()+": "+label, false)
}

func (m *App) onPlayerLog(v playerLogMsg) {
	if v.isErr {
		m.setStatus(m.playerName()+": "+ellipsize(v.line, 90), true)
	}
}

func (m *App) onPlayerExited(v playerExitedMsg) {
	pos, dur := v.proc.Position()
	if pos > 0 {
		m.cfg.RecordPosition(v.ch.Key, pos, dur)
	}
	m.cfg.Save()
	if m.playing == v.proc {
		m.playing = nil
		m.playingCh = store.Channel{}
		m.playPos, m.playDur = 0, 0
	}
	m.setStatus("mpv closed"+withPos(pos), false)
}

func withPos(d time.Duration) string {
	if d <= 0 {
		return ""
	}
	return fmt.Sprintf(" at %02d:%02d", int(d/time.Minute), int(d/time.Second)%60)
}

func (m *App) stopMpvIfNotKept() {
	if m.playing == nil || m.cfg.KeepMpvOnQuit() {
		return
	}
	pos, dur := m.playing.Position()
	if pos > 0 {
		m.cfg.RecordPosition(m.playingCh.Key, pos, dur)
		m.cfg.Save()
	}
	m.playing.Stop()
}

func (m *App) flushAndSave() {
	if m.playing != nil {
		pos, dur := m.playing.Position()
		if pos > 0 {
			m.cfg.RecordPosition(m.playingCh.Key, pos, dur)
		}
	}
	m.cfg.Save()
}

// openMpvSetup shows the player setup form: no mpv, VLC, or MPC-HC was found,
// so playback needs either an install or an explicit binary path.
func (m *App) openMpvSetup() {
	m.mpvForm = newMpvForm(m.width - 10)
	m.mode = modeMpv
	m.layout()
	m.setStatus("no player found "+glyphDash+" install mpv or set its path", true)
}

// mpvStep drives the player setup form. Enter saves a pasted binary path (an
// empty field just re-checks, in case mpv was installed meanwhile); esc skips
// and leaves browsing working without playback.
func (m *App) mpvStep(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.mode = modeMain
		m.setStatus("player setup skipped "+glyphDash+" press enter on a channel to retry", false)
		return m, nil
	case "enter":
		if path := strings.TrimSpace(m.mpvForm.path.Value()); path != "" {
			m.cfg.SetMpvPath(path)
			m.cfg.Save()
		}
		if exe, kind, ok := player.Detect(m.cfg.MpvPath()); ok {
			m.mode = modeMain
			m.setStatus(string(kind)+": "+exe, false)
		} else {
			m.setStatus("still no player found "+glyphDash+" install mpv or paste its full path", true)
		}
		return m, nil
	}
	return m, m.mpvForm.Update(msg)
}
