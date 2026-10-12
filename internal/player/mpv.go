// Package player launches mpv for a channel, mirroring iptvnator's external
// MPV flow: resolved binary path, `--ytdl=no`, optional per-channel
// User-Agent/Referer/http-header-fields, `--force-media-title`, `--start`
// resume, and a JSON-over-IPC socket bridge for playback position tracking.
package player

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mertturunc/iptv-tui/internal/dbg"
)

type Request struct {
	URL       string
	Title     string
	UserAgent string
	Referer   string
	// Origin and ExtraHeaders are honored by the mpv backend
	// (--http-header-fields) for providers that need more than
	// User-Agent/Referer. The TUI currently only fills UserAgent/Referer,
	// so these stay empty in normal use; they exist for API callers and
	// future provider fields, not dead code.
	Origin        string
	ExtraHeaders  map[string]string
	Start         time.Duration
	CustomArgs    []string
	MpvPath       string
	TrackPosition bool
}

// PlayerKind names the playback backend. mpv is preferred (headers, resume
// tracking); VLC and MPC-HC are best-effort fallbacks.
type PlayerKind string

const (
	KindMpv   PlayerKind = "mpv"
	KindVLC   PlayerKind = "vlc"
	KindMPCHC PlayerKind = "mpc-hc"
)

// Process is a live player child with optional mpv IPC connection.
type Process struct {
	cmd      *exec.Cmd
	mu       sync.Mutex
	ipc      net.Conn
	socket   string
	ipcJSON  *json.Decoder
	kind     PlayerKind
	closed   bool
	quitSent bool
	// cmdMu serializes whole request/response pairs on the pipe.
	cmdMu sync.Mutex
	// connected turns true once the IPC link is dialed. exited turns true
	// once Wait returns. Both are guarded by mu: exec.Cmd.ProcessState must
	// not be touched from another goroutine while Wait may write it.
	connected bool
	exited    bool

	OnLog  func(line string, isErr bool)
	OnExit func()

	position time.Duration
	duration time.Duration
	done     chan struct{}
	doneOnce sync.Once
}

// PlayerName reports the backend behind a process ("mpv" for zero values,
// which is what tests construct).
func (p *Process) PlayerName() string {
	if p.kind == "" {
		return string(KindMpv)
	}
	return string(p.kind)
}

// ResolvePath mirrors iptvnator getDefaultMpvPath: custom path first, then
// platform defaults, finally the bare "mpv" command.
func ResolvePath(custom string) string {
	if strings.TrimSpace(custom) != "" {
		return strings.TrimSpace(custom)
	}
	if p := firstRunnable(mpvDefaults()...); p != "" {
		return p
	}
	return "mpv"
}

func mpvDefaults() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			filepath.Join("C:", "Program Files", "mpv", "mpv.exe"),
			filepath.Join("C:", "Program Files (x86)", "mpv", "mpv.exe"),
			"mpv",
		}
	case "linux":
		return []string{"/usr/bin/mpv", "/usr/local/bin/mpv", "/snap/bin/mpv", "mpv"}
	case "darwin":
		return []string{
			"/Applications/mpv.app/Contents/MacOS/mpv",
			"/opt/homebrew/bin/mpv",
			"/usr/local/bin/mpv",
			"mpv",
		}
	default:
		return []string{"mpv"}
	}
}

func vlcDefaults() []string {
	switch runtime.GOOS {
	case "windows":
		return []string{
			filepath.Join("C:", "Program Files", "VideoLAN", "VLC", "vlc.exe"),
			filepath.Join("C:", "Program Files (x86)", "VideoLAN", "VLC", "vlc.exe"),
			"vlc",
		}
	case "darwin":
		return []string{"/Applications/VLC.app/Contents/MacOS/VLC", "vlc"}
	default: // linux and friends
		return []string{"/usr/bin/vlc", "/usr/local/bin/vlc", "vlc"}
	}
}

func mpchcDefaults() []string {
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join("C:", "Program Files", "MPC-HC", "mpc-hc64.exe"),
			filepath.Join("C:", "Program Files", "MPC-HC", "mpc-hc.exe"),
			filepath.Join("C:", "Program Files (x86)", "MPC-HC", "mpc-hc64.exe"),
			filepath.Join("C:", "Program Files (x86)", "MPC-HC", "mpc-hc.exe"),
			"mpc-hc64",
			"mpc-hc",
		}
	}
	return []string{"mpc-hc64", "mpc-hc"}
}

// firstRunnable returns the first candidate that exists: absolute or
// path-like entries are stated on disk, bare names go through PATH.
func firstRunnable(candidates ...string) string {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if filepath.IsAbs(c) || strings.ContainsAny(c, `/\`) {
			if st, err := os.Stat(c); err == nil && !st.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func kindFromName(p string) PlayerKind {
	base := strings.ToLower(filepath.Base(p))
	switch {
	case strings.Contains(base, "vlc"):
		return KindVLC
	case strings.Contains(base, "mpc"):
		return KindMPCHC
	default:
		return KindMpv
	}
}

// Detect picks the playback backend: an explicit custom binary first, then
// mpv, VLC, and MPC-HC in that order. ok is false when nothing is runnable.
func Detect(custom string) (path string, kind PlayerKind, ok bool) {
	if strings.TrimSpace(custom) != "" {
		if p := firstRunnable(strings.TrimSpace(custom)); p != "" {
			return p, kindFromName(p), true
		}
		return "", "", false
	}
	if p := firstRunnable(mpvDefaults()...); p != "" {
		return p, KindMpv, true
	}
	if p := firstRunnable(vlcDefaults()...); p != "" {
		return p, KindVLC, true
	}
	if p := firstRunnable(mpchcDefaults()...); p != "" {
		return p, KindMPCHC, true
	}
	return "", "", false
}

// Available reports whether any playback backend (mpv, VLC, MPC-HC) can be
// executed. It resolves custom the same way Launch does.
func Available(custom string) bool {
	_, _, ok := Detect(custom)
	return ok
}

// InstallHelp tells the operator how to get mpv on the current OS. The first
// line names the problem; the rest are install commands or pointers.
func InstallHelp() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"mpv was not found on PATH, and iptv-tui needs it for playback.",
			"Install it with Homebrew:",
			"  brew install mpv",
		}
	case "windows":
		return []string{
			"mpv was not found on PATH, and iptv-tui needs it for playback.",
			"Download a Windows build from https://mpv.io/installation/,",
			"then either add its folder to PATH or paste the full",
			"mpv.exe path below (e.g. C:\\Program Files\\mpv\\mpv.exe).",
		}
	default: // linux and friends
		return []string{
			"mpv was not found on PATH, and iptv-tui needs it for playback.",
			"Install it from your package manager, e.g.:",
			"  sudo apt install mpv      # Debian/Ubuntu",
			"  sudo dnf install mpv      # Fedora",
			"  sudo pacman -S mpv        # Arch",
		}
	}
}

// escapeHeaderEscape mirrors iptvnator's escapeMpvStringListValue for the
// comma-separated http-header-fields option.
func escapeHeaderValue(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), ",", `\,`)
}

// SplitArgs parses newline-separated player args from settings, one per line.
func SplitArgs(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func buildArgs(r Request, socket string, kind PlayerKind) []string {
	args := make([]string, 0, 16)
	args = append(args, r.CustomArgs...)
	switch kind {
	case KindVLC:
		return buildVLCArgs(r, args)
	case KindMPCHC:
		return buildMPCHCArgs(r, args)
	default:
		return buildMpvArgs(r, socket, args)
	}
}

// buildVLCArgs covers what VLC accepts: quit after playback, a user agent,
// a display title, and a start offset. Referer/headers have no VLC
// equivalent and are dropped. Unknown flags would only warn, never fail.
// Note: the TUI zeroes Start for non-mpv backends (see launchChannel), so a
// start offset here only ever comes from direct API callers.
func buildVLCArgs(r Request, args []string) []string {
	args = append(args, "--play-and-exit")
	if r.UserAgent != "" {
		args = append(args, "--http-user-agent="+r.UserAgent)
	}
	if r.Title != "" {
		args = append(args, "--input-title-format="+r.Title)
	}
	if r.Start > 0 {
		args = append(args, "--start-time="+fmt.Sprintf("%.0f", r.Start.Seconds()))
	}
	args = append(args, r.URL)
	return args
}

// buildMPCHCArgs is minimal on purpose: MPC-HC takes a file/URL plus
// switches, with no user-agent or resume support to map onto.
func buildMPCHCArgs(r Request, args []string) []string {
	args = append(args, r.URL, "/play")
	return args
}

func buildMpvArgs(r Request, socket string, args []string) []string {
	if socket != "" {
		args = append(args, "--input-ipc-server="+socket, "--idle=no")
	}
	args = append(args, "--ytdl=no")
	if r.UserAgent != "" {
		args = append(args, "--user-agent="+r.UserAgent)
	}
	if r.Referer != "" {
		args = append(args, "--referrer="+r.Referer)
	}
	var fields []string
	if r.Origin != "" {
		found := false
		for k := range r.ExtraHeaders {
			if strings.EqualFold(k, "Origin") {
				found = true
			}
		}
		if !found {
			fields = append(fields, "Origin: "+r.Origin)
		}
	}
	for name, value := range r.ExtraHeaders {
		if name == "" || strings.TrimSpace(value) == "" {
			continue
		}
		fields = append(fields, name+": "+strings.TrimSpace(value))
	}
	if len(fields) > 0 {
		escaped := make([]string, 0, len(fields))
		for _, f := range fields {
			escaped = append(escaped, escapeHeaderValue(f))
		}
		args = append(args, "--http-header-fields="+strings.Join(escaped, ","))
	}
	if r.Title != "" {
		args = append(args, "--force-media-title="+r.Title)
	}
	if r.Start > 0 {
		args = append(args, "--start="+fmt.Sprintf("%.0f", r.Start.Seconds()))
	}
	args = append(args, r.URL)
	return args
}

// Debugf collects player diagnostics through the shared debug sink (cleared
// per run when IPTV_TUI_DEBUG=1). Never logs full args: stream URLs can
// embed Xtream credentials.
func Debugf(format string, args ...interface{}) {
	dbg.Log("player: "+format, args...)
}

func ipcSocketName() string {
	uniq := fmt.Sprintf("%d%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\mpvtui-%s`, uniq)
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("mpvtui-%s.sock", uniq))
}

var dialTimeout = 3 * time.Second

func dialIPC(socket string) (net.Conn, error) {
	if runtime.GOOS == "windows" {
		return dialPipe(socket)
	}
	return net.DialTimeout("unix", socket, dialTimeout)
}

func isErrorLine(base string) bool {
	l := strings.ToLower(base)
	for _, frag := range []string{
		"failed to open", "error opening", "protocol not found",
		"connection refused", "errors when loading file", "403", "404",
		" error ", " - error -",
	} {
		if strings.Contains(l, frag) {
			return true
		}
	}
	return strings.HasPrefix(l, "error")
}

// Launch starts the detected player (mpv, VLC, or MPC-HC). Only mpv gets an
// IPC socket for position tracking; the fallbacks play best-effort, so resume
// has nothing to record for them and the next launch starts at 0 by design.
func Launch(r Request) (*Process, error) {
	exe, kind, ok := Detect(r.MpvPath)
	if !ok {
		return nil, fmt.Errorf("no player found (looked for mpv, vlc, mpc-hc)")
	}
	socket := ""
	track := r.TrackPosition && kind == KindMpv
	if track {
		socket = ipcSocketName()
	}
	args := buildArgs(r, socket, kind)

	cmd := exec.Command(exe, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		Debugf("start failed kind=%s exe=%s: %v", kind, exe, err)
		return nil, fmt.Errorf("%s failed to start: %w", kind, err)
	}
	Debugf("launch kind=%s exe=%s track=%v socket=%q", kind, exe, track, socket)

	p := &Process{cmd: cmd, socket: socket, kind: kind, done: make(chan struct{})}
	if track {
		go p.connectIPC()
	}
	go p.watch(stdout, stderr)
	go p.waitAndReport()
	return p, nil
}

func (p *Process) watch(stdout, stderr io.Reader) {
	scan := func(r io.Reader, isErr bool) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			if p.OnLog != nil {
				// AV: progress lines are noise; skip like iptvnator.
				if strings.HasPrefix(line, "AV:") || strings.HasPrefix(line, "(Paused) AV:") {
					continue
				}
				p.OnLog(line, isErr || isErrorLine(line))
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); scan(stdout, false) }()
	go func() { defer wg.Done(); scan(stderr, true) }()
	wg.Wait()
}

func (p *Process) connectIPC() {
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := dialIPC(p.socket)
		if err == nil {
			p.mu.Lock()
			p.ipc = conn
			p.ipcJSON = json.NewDecoder(conn)
			p.connected = true
			p.mu.Unlock()
			Debugf("ipc connected socket=%q", p.socket)
			go p.pollPosition()
			return
		}
		Debugf("ipc dial socket=%q: %v", p.socket, err)
		p.mu.Lock()
		exited := p.exited || p.closed
		p.mu.Unlock()
		if exited || time.Now().After(deadline) {
			Debugf("ipc give up socket=%q exited=%v", p.socket, exited)
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

type ipcRequest struct {
	Command []interface{} `json:"command"`
}

type ipcResponse struct {
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

func (p *Process) command(name string, args ...interface{}) (json.RawMessage, error) {
	// Serialized: Stop's "quit" must not interleave with the poller's
	// request/response pairs, or responses get crossed.
	p.cmdMu.Lock()
	defer p.cmdMu.Unlock()
	p.mu.Lock()
	conn, dec := p.ipc, p.ipcJSON
	p.mu.Unlock()
	if conn == nil || dec == nil {
		return nil, fmt.Errorf("no ipc")
	}
	req, _ := json.Marshal(ipcRequest{Command: append([]interface{}{name}, args...)})
	conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return nil, err
	}
	// mpv multiplexes async events (tick, file-loaded, ...) on the same pipe.
	// They carry no "error" key, so skip them until our command's real
	// response arrives instead of mistaking an event for the answer.
	// Startup/seek storms can burst dozens of events, so the cap is generous;
	// each iteration returns as soon as any message arrives.
	for skipped := 0; ; skipped++ {
		if skipped > 200 {
			return nil, fmt.Errorf("mpv sent no response to %s", name)
		}
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		var probe struct {
			Error *string `json:"error"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil || probe.Error == nil {
			Debugf("ipc skip non-response: %q", trunc(string(raw), 120))
			continue
		}
		if *probe.Error != "" && *probe.Error != "success" {
			return nil, fmt.Errorf("mpv error: %s", *probe.Error)
		}
		var resp ipcResponse
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		return resp.Data, nil
	}
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// pollInterval is how often the player is asked for position. It matches the
// TUI's 1s playbar tick so the readout moves every second instead of jumping
// in 5s steps. Two local get_property calls per second is negligible traffic.
const pollInterval = time.Second

func (p *Process) pollPosition() {
	// Poll once right away so the readout appears within a second of the
	// link coming up instead of a full tick later.
	p.pollOnce()
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for range t.C {
		if !p.alive() {
			return
		}
		p.pollOnce()
	}
}

func (p *Process) pollOnce() {
	data, err := p.command("get_property", "time-pos")
	if err != nil {
		Debugf("time-pos: %v", err)
		return
	}
	if len(data) == 0 {
		Debugf("time-pos: empty response")
		return
	}
	var pos float64
	if err := json.Unmarshal(data, &pos); err != nil {
		Debugf("time-pos unmarshal %q: %v", string(data), err)
		return
	}
	posD := time.Duration(pos * float64(time.Second))
	durD := time.Duration(0)
	if ddata, derr := p.command("get_property", "duration"); derr != nil {
		Debugf("duration: %v", derr)
	} else {
		var dur float64
		if err := json.Unmarshal(ddata, &dur); err != nil {
			Debugf("duration unmarshal %q: %v", string(ddata), err)
		} else {
			durD = time.Duration(dur * float64(time.Second))
		}
	}
	p.mu.Lock()
	p.position, p.duration = posD, durD
	p.mu.Unlock()
	Debugf("poll pos=%v dur=%v", posD, durD)
}

func (p *Process) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return !p.closed && !p.exited
}

func (p *Process) waitAndReport() {
	p.cmd.Wait()
	// Give the position poller a last chance to record.
	time.Sleep(300 * time.Millisecond)
	p.mu.Lock()
	p.closed = true
	p.exited = true
	conn := p.ipc
	p.mu.Unlock()
	if conn != nil {
		conn.Close()
	}
	if p.socket != "" && runtime.GOOS != "windows" {
		os.Remove(p.socket)
	}
	if p.OnExit != nil {
		p.OnExit()
	}
	p.doneOnce.Do(func() { close(p.done) })
}

// Done returns a channel closed once the mpv process has exited.
func (p *Process) Done() <-chan struct{} {
	return p.done
}

// Position returns the last known playback position and duration.
func (p *Process) Position() (time.Duration, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.position, p.duration
}

// Stop sends a graceful "quit" command over IPC if available.
func (p *Process) Stop() {
	p.mu.Lock()
	if p.quitSent {
		p.mu.Unlock()
		return
	}
	p.quitSent = true
	exited := p.exited
	p.mu.Unlock()
	if p.ipc != nil {
		p.command("quit")
	}
	if p.cmd != nil && p.cmd.Process != nil && !exited {
		p.cmd.Process.Kill()
	}
}

// Connected reports whether the IPC link to the player is up. Only mpv gets
// one; VLC and MPC-HC play without position tracking by design.
func (p *Process) Connected() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.connected
}
