package player

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFake(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	return p
}

func TestDetectCustomKind(t *testing.T) {
	cases := []struct {
		name string
		want PlayerKind
	}{
		{"my-mpv", KindMpv},
		{"vlc-portable", KindVLC},
		{"mpc-hc64.exe", KindMPCHC},
	}
	for _, c := range cases {
		p := writeFake(t, c.name)
		got, kind, ok := Detect(p)
		if !ok || kind != c.want || got != p {
			t.Errorf("Detect(%s) = %q, %q, %v; want %q, %q, true",
				c.name, got, kind, ok, p, c.want)
		}
	}
}

func TestDetectBogusCustom(t *testing.T) {
	if _, _, ok := Detect(filepath.Join(t.TempDir(), "no-such-player")); ok {
		t.Fatal("bogus custom path must not detect")
	}
}

func TestAvailableBogus(t *testing.T) {
	if Available(filepath.Join(t.TempDir(), "no-such-player")) {
		t.Fatal("bogus path must be unavailable")
	}
}

func TestInstallHelp(t *testing.T) {
	lines := InstallHelp()
	if len(lines) == 0 {
		t.Fatal("empty install help")
	}
	if !strings.Contains(strings.ToLower(lines[0]), "mpv") {
		t.Fatalf("first line should name mpv: %q", lines[0])
	}
}

func TestBuildArgsVLC(t *testing.T) {
	r := Request{URL: "https://demo.example.com/x.m3u8", Title: "Demo", UserAgent: "ua", Start: 90_000_000_000}
	args := buildArgs(r, "", KindVLC)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--play-and-exit", "--http-user-agent=ua", "--input-title-format=Demo", "--start-time=90"} {
		if !strings.Contains(joined, want) {
			t.Errorf("vlc args %q missing %q", joined, want)
		}
	}
	if args[len(args)-1] != r.URL {
		t.Errorf("vlc args should end with the URL: %q", joined)
	}
	for _, bad := range []string{"--input-ipc-server", "--ytdl"} {
		if strings.Contains(joined, bad) {
			t.Errorf("vlc args must not carry mpv flags (%q): %q", bad, joined)
		}
	}
}

func TestBuildArgsMPCHC(t *testing.T) {
	r := Request{URL: "https://demo.example.com/x.m3u8", CustomArgs: []string{"/fullscreen"}}
	args := buildArgs(r, "", KindMPCHC)
	if len(args) != 3 || args[0] != "/fullscreen" || args[1] != r.URL || args[2] != "/play" {
		t.Fatalf("mpc-hc args = %q, want [/fullscreen url /play]", args)
	}
}

func TestBuildArgsMpvUnchanged(t *testing.T) {
	r := Request{URL: "https://demo.example.com/x.m3u8", Title: "Demo", UserAgent: "ua"}
	args := buildArgs(r, "sock", KindMpv)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--input-ipc-server=sock", "--ytdl=no", "--user-agent=ua", "--force-media-title=Demo"} {
		if !strings.Contains(joined, want) {
			t.Errorf("mpv args %q missing %q", joined, want)
		}
	}
}

func TestPlayerNameDefault(t *testing.T) {
	if (&Process{}).PlayerName() != "mpv" {
		t.Fatal("zero Process should report mpv")
	}
	if (&Process{kind: KindVLC}).PlayerName() != "vlc" {
		t.Fatal("vlc process should report vlc")
	}
}

func TestConnectedDefaultsFalse(t *testing.T) {
	if (&Process{}).Connected() {
		t.Fatal("fresh process must not report connected")
	}
}

func TestPollOnceWithoutIPC(t *testing.T) {
	p := &Process{}
	p.pollOnce() // no link: must return without blocking or writing state
	if pos, dur := p.Position(); pos != 0 || dur != 0 {
		t.Fatalf("no-ipc poll wrote position %v/%v", pos, dur)
	}
}

func TestCommandSkipsEvents(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := &Process{ipc: client, ipcJSON: json.NewDecoder(client)}
	go func() {
		r := bufio.NewReader(server)
		if _, err := r.ReadBytes('\n'); err != nil {
			return
		}
		// An async event arrives before the real answer; it must be
		// skipped, not mistaken for the response.
		server.Write([]byte("{\"event\":\"tick\"}\n"))
		server.Write([]byte("{\"error\":\"success\",\"data\":12.5}\n"))
	}()
	data, err := p.command("get_property", "time-pos")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	var f float64
	if err := json.Unmarshal(data, &f); err != nil || f != 12.5 {
		t.Fatalf("got data %q err %v, want 12.5", string(data), err)
	}
}

func TestCommandErrorResponse(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	p := &Process{ipc: client, ipcJSON: json.NewDecoder(client)}
	go func() {
		r := bufio.NewReader(server)
		if _, err := r.ReadBytes('\n'); err != nil {
			return
		}
		server.Write([]byte("{\"error\":\"property unavailable\"}\n"))
	}()
	if _, err := p.command("get_property", "time-pos"); err == nil {
		t.Fatal("expected an mpv error, got success")
	}
}
