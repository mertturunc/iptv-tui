package airplay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeTV is a minimal AirPlay video receiver: it records the /play body and
// answers /stop, /playback-info, and /server-info like an Apple TV with no
// password does.
type fakeTV struct {
	playBody string
	playHits int
	stopHits int
	info     string
	name     string
	// noInfo makes /server-info 404, for the Probe fallback test.
	noInfo bool
	// auth makes /play 401 like a password-protected TV.
	auth bool
}

func (f *fakeTV) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/play":
		if f.auth {
			w.Header().Set("WWW-Authenticate", `Digest realm="AirPlay", nonce="abc"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		f.playBody, f.playHits = string(body), f.playHits+1
		w.WriteHeader(http.StatusOK)
	case "/stop":
		f.stopHits++
		w.WriteHeader(http.StatusOK)
	case "/playback-info":
		w.Header().Set("Content-Type", "text/x-apple-plist+xml")
		_, _ = io.WriteString(w, f.info)
	case "/server-info":
		if f.noInfo {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/x-apple-plist+xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>`+
			`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`+
			`<plist version="1.0"><dict><key>Name</key><string>`+f.name+`</string></dict></plist>`)
	default:
		http.NotFound(w, r)
	}
}

func testClient(t *testing.T, tv *fakeTV) (*Client, string) {
	t.Helper()
	srv := httptest.NewServer(tv)
	t.Cleanup(srv.Close)
	addr := strings.TrimPrefix(srv.URL, "http://")
	c, err := Dial(context.Background(), addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return c, addr
}

func TestPlayPostsContentLocation(t *testing.T) {
	tv := &fakeTV{}
	c, _ := testClient(t, tv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Play(ctx, "http://provider.example/live/123.m3u8", 0); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if tv.playHits != 1 {
		t.Fatalf("playHits = %d, want 1", tv.playHits)
	}
	if !strings.Contains(tv.playBody, "<key>Content-Location</key>") {
		t.Fatalf("play body lacks Content-Location:\n%s", tv.playBody)
	}
	if !strings.Contains(tv.playBody, "<string>http://provider.example/live/123.m3u8</string>") {
		t.Fatalf("play body lacks stream URL:\n%s", tv.playBody)
	}
}

func TestPlayBodyEscapesXML(t *testing.T) {
	tv := &fakeTV{}
	c, _ := testClient(t, tv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "http://provider.example/live?user=a&pass=b<tail>"
	if err := c.Play(ctx, url, 0); err != nil {
		t.Fatalf("Play: %v", err)
	}
	if strings.Contains(tv.playBody, "user=a&pass") {
		t.Fatalf("play body has unescaped &:\n%s", tv.playBody)
	}
	if !strings.Contains(tv.playBody, "user=a&amp;pass=b&lt;tail&gt;") {
		t.Fatalf("play body lacks escaped URL:\n%s", tv.playBody)
	}
}

func TestPlayRefusedWhenAuthRequired(t *testing.T) {
	tv := &fakeTV{auth: true}
	c, _ := testClient(t, tv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := c.Play(ctx, "http://provider.example/live/1.m3u8", 0)
	if err == nil {
		t.Fatal("Play with 401 = nil, want auth error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "password") &&
		!strings.Contains(strings.ToLower(err.Error()), "auth") {
		t.Fatalf("error %q does not explain auth", err)
	}
}

func TestStopPostsStop(t *testing.T) {
	tv := &fakeTV{}
	c, _ := testClient(t, tv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if tv.stopHits != 1 {
		t.Fatalf("stopHits = %d, want 1", tv.stopHits)
	}
}

func TestPlaybackInfoParsesPosition(t *testing.T) {
	tv := &fakeTV{info: `<?xml version="1.0" encoding="UTF-8"?>` +
		`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` +
		`<plist version="1.0"><dict>` +
		`<key>duration</key><real>3600</real>` +
		`<key>position</key><real>61.5</real>` +
		`<key>rate</key><real>1</real>` +
		`<key>readyToPlay</key><true/>` +
		`</dict></plist>`}
	c, _ := testClient(t, tv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, _, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.Position != 61.5 || st.Duration != 3600 || st.Rate != 1 {
		t.Fatalf("Status = %+v, want pos=61.5 dur=3600 rate=1", st)
	}
	if st.State != "PLAYING" {
		t.Fatalf("State = %q, want PLAYING (readyToPlay + rate 1)", st.State)
	}
}

func TestPlaybackInfoIdleWhenNotReady(t *testing.T) {
	tv := &fakeTV{info: `<plist version="1.0"><dict>` +
		`<key>position</key><real>0</real>` +
		`<key>rate</key><real>0</real>` +
		`</dict></plist>`}
	c, _ := testClient(t, tv)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, _, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if st.State != "IDLE" {
		t.Fatalf("State = %q, want IDLE", st.State)
	}
}

func TestProbeUsesServerInfoName(t *testing.T) {
	tv := &fakeTV{name: "Living Room"}
	srv := httptest.NewServer(tv)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	d := Probe(addr)
	if d.Name != "Living Room" {
		t.Fatalf("Name = %q, want Living Room", d.Name)
	}
	if d.Addr != addr {
		t.Fatalf("Addr = %q, want %q (explicit port must survive for Dial)", d.Addr, addr)
	}
}

func TestProbeFallsBackToAddress(t *testing.T) {
	tv := &fakeTV{noInfo: true} // /server-info 404s
	srv := httptest.NewServer(tv)
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")
	d := Probe(addr)
	if d.Name == "" || d.Addr == "" {
		t.Fatalf("Probe = %+v, want address fallback", d)
	}
}
