package dbg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnabled(t *testing.T) {
	t.Setenv("IPTV_TUI_DEBUG", "")
	if Enabled() {
		t.Fatal("empty env must disable")
	}
	t.Setenv("IPTV_TUI_DEBUG", "1")
	if !Enabled() {
		t.Fatal("1 must enable")
	}
	for _, v := range []string{"true", "yes", "on", " TRUE ", "On"} {
		t.Setenv("IPTV_TUI_DEBUG", v)
		if !Enabled() {
			t.Fatalf("%q must enable", v)
		}
	}
	for _, v := range []string{"0", "false", "off", "no", "banana", "2", "maybe"} {
		t.Setenv("IPTV_TUI_DEBUG", v)
		if Enabled() {
			t.Fatalf("%q must not enable", v)
		}
	}
}

func TestShortURLStripsSecrets(t *testing.T) {
	got := ShortURL("https://user:pass@provider.example:8080/live/user/pass/123.m3u8?token=abc#frag")
	if strings.Contains(got, "user") || strings.Contains(got, "pass") || strings.Contains(got, "token") {
		t.Fatalf("credentials leaked: %q", got)
	}
	for _, want := range []string{"https://", "provider.example", ".m3u8"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if got := ShortURL("not a url at all@@"); !strings.Contains(got, "chars)") {
		t.Fatalf("garbage should degrade gracefully: %q", got)
	}
}

func TestScrubRedactsErrors(t *testing.T) {
	in := `Get "https://u:p@host.example/x.ts?tok=1": dial tcp: connection refused`
	out := Scrub(in)
	if strings.Contains(out, "u:p") || strings.Contains(out, "tok=1") {
		t.Fatalf("url leaked through scrub: %q", out)
	}
	if !strings.Contains(out, "host.example") || !strings.Contains(out, "connection refused") {
		t.Fatalf("scrub ate the context: %q", out)
	}
	if Scrub("plain text, no urls") != "plain text, no urls" {
		t.Fatal("non-url text must pass through")
	}
}

func TestInitClearsLog(t *testing.T) {
	t.Setenv("IPTV_TUI_DEBUG", "1")
	dir := t.TempDir()
	// os.UserConfigDir obeys these on their platforms.
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
	t.Setenv("HOME", dir)
	Init("test", []string{"iptv-tui"})
	p := Path()
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(p) != filepath.Join(cfgDir, "iptv-tui") {
		t.Fatalf("unexpected log path %q", p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("log not created: %v", err)
	}
	Log("hello %d", 1)
	data, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(data), "hello 1") {
		t.Fatalf("log unwritten: %q %v", data, err)
	}
	// A second Init must truncate, not append.
	Init("test", []string{"iptv-tui"})
	data, _ = os.ReadFile(p)
	if strings.Contains(string(data), "hello 1") {
		t.Fatal("re-init should clear the log")
	}
	// Reset package state for other tests (same-process suite).
	mu.Lock()
	if file != nil {
		file.Close()
		file = nil
	}
	path = ""
	mu.Unlock()
	t.Setenv("IPTV_TUI_DEBUG", "")
}
