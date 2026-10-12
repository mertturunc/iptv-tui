// Package dbg is the single debug-log sink: with IPTV_TUI_DEBUG=1 every run
// starts from a cleared file, so a shared log always tells one story.
// Stream URLs routinely embed provider credentials, so nothing logs raw
// URLs — ShortURL keeps host plus shape, Scrub redacts URLs out of errors.
package dbg

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const logName = "player-debug.log"

var (
	mu   sync.Mutex
	file *os.File
	path string
)

// Enabled reports whether debug logging is on. The documented form is
// IPTV_TUI_DEBUG=1; "true"/"yes"/"on" are accepted as well, while empty,
// "0", "false", "no", and "off" stay off.
func Enabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("IPTV_TUI_DEBUG")))
	switch v {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

// Path is the log file location (empty when disabled).
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// Init clears the log for a fresh run and records the build header. It is a
// no-op unless debugging is enabled; main calls it before anything else.
func Init(version string, args []string) {
	if !Enabled() {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if file != nil {
		file.Close()
		file = nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	path = filepath.Join(dir, "iptv-tui", logName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		path = ""
		return
	}
	// Truncate: yesterday's failures must not pollute today's report.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		path = ""
		return
	}
	file = f
	fmt.Fprintf(file, "=== iptv-tui %s run %s ===\n", version, time.Now().Format(time.RFC3339))
	fmt.Fprintf(file, "args: %s\n", Scrub(fmt.Sprintf("%q", args)))
}

// Log appends one timestamped line. Silent unless enabled.
func Log(format string, args ...interface{}) {
	if !Enabled() {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	if file == nil {
		return
	}
	fmt.Fprintf(file, time.Now().Format("15:04:05 ")+format+"\n", args...)
}

var urlRe = regexp.MustCompile(`https?://[^\s"']+`)

// Scrub replaces any URLs in free text with their ShortURL form, so error
// strings from http/tls (which echo the URL, credentials included) are safe
// to log. Non-URL text passes through untouched.
func Scrub(s string) string {
	return urlRe.ReplaceAllStringFunc(s, func(raw string) string {
		// Trim trailing punctuation the regex may have swallowed.
		trimmed := raw
		for len(trimmed) > 0 {
			c := trimmed[len(trimmed)-1]
			if c == '.' || c == ',' || c == ';' || c == ':' || c == ')' || c == ']' {
				trimmed = trimmed[:len(trimmed)-1]
				continue
			}
			break
		}
		return ShortURL(trimmed) + raw[len(trimmed):]
	})
}

// ShortURL keeps scheme://host plus the payload shape (extension, length)
// and drops path, query, and fragment — the parts that carry usernames,
// passwords, and tokens.
func ShortURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Sprintf("(url %d chars)", len(raw))
	}
	ext := ""
	if i := strings.LastIndex(u.Path, "."); i >= 0 {
		ext = u.Path[i:]
		if len(ext) > 8 || strings.Contains(ext, "/") {
			ext = ""
		}
	}
	host := u.Host
	if h, _, err := net.SplitHostPort(u.Host); err == nil {
		host = h
	}
	return fmt.Sprintf("%s://%s/...%s (%d chars)", u.Scheme, host, ext, len(raw))
}
