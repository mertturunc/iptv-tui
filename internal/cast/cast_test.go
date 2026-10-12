package cast

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

func TestGuessContentType(t *testing.T) {
	cases := []struct{ url, want string }{
		{"http://x/y.m3u8", "application/x-mpegURL"},
		{"http://x/live/u/p/1.m3u8?token=abc", "application/x-mpegURL"},
		{"http://x/y.mpd", "application/dash+xml"},
		{"http://x/movie.mp4", "video/mp4"},
		{"http://x/e.mkv", "video/x-matroska"},
		{"http://x/s.ts", "video/mp2t"},
		{"http://x/a.mp3", "audio/mpeg"},
		{"http://x/live/u/p/123", "video/mp4"}, // bare Xtream-style URL
	}
	for _, c := range cases {
		if got := GuessContentType(c.url); got != c.want {
			t.Errorf("GuessContentType(%q) = %q, want %q", c.url, got, c.want)
		}
	}
	if GuessStreamType(true) != "LIVE" || GuessStreamType(false) != "BUFFERED" {
		t.Fatal("stream types wrong")
	}
}

func TestFrameRoundTrip(t *testing.T) {
	in := message{source: "sender-0", dest: "receiver-0", namespace: nsReceiver, payload: `{"type":"GET_STATUS"}`}
	out, err := decodeMessage(encodeMessage(in))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if out != in {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
	// A real receiver answers with protocol_version 0 (CASTV2_1_0); the
	// framing must accept it, not just our own bytes.
	raw := encodeMessage(in)
	if len(raw) < 2 || raw[1] != 0 {
		t.Fatalf("encoder should write protocol version 0, got %v", raw[:2])
	}
}

func TestDecodeBadFrame(t *testing.T) {
	if _, err := decodeMessage([]byte{0xff, 0xff}); err == nil {
		t.Fatal("corrupt frame must fail")
	}
}

func TestCollectDedupesAndSorts(t *testing.T) {
	entries := []*zeroconf.ServiceEntry{
		{Instance: "uuid-2", HostName: "b.local.", AddrIPv4: []net.IP{net.ParseIP("192.168.1.99")},
			Text: []string{"fn=Bedroom"}},
		{Instance: "uuid-1", HostName: "a.local.", AddrIPv4: []net.IP{net.ParseIP("192.168.1.182")},
			Text: []string{"md=Smart TV Pro", "fn=Toot"}},
		// Same TV answering on a second interface: deduped by address.
		{Instance: "uuid-1", HostName: "a.local.", AddrIPv4: []net.IP{net.ParseIP("192.168.1.182")},
			Text: []string{"fn=Toot"}},
	}
	devs := collect(entries)
	if len(devs) != 2 {
		t.Fatalf("got %d devices, want 2: %+v", len(devs), devs)
	}
	if devs[0].Name != "Bedroom" || devs[1].Name != "Toot" || devs[1].Addr != "192.168.1.182" {
		t.Fatalf("sorted/named wrong: %+v", devs)
	}
}

func TestDeviceNameFallbacks(t *testing.T) {
	if n := deviceName(&zeroconf.ServiceEntry{Instance: "Kitchen"}); n != "Kitchen" {
		t.Fatalf("instance fallback = %q", n)
	}
	if n := deviceName(&zeroconf.ServiceEntry{HostName: "x.local."}); n != "x" {
		t.Fatalf("hostname fallback = %q", n)
	}
}

func TestProbeEureka(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/setup/eureka_info" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Write([]byte(`{"name":"Living Room TV"}`))
	}))
	defer srv.Close()
	name, err := probeURL(srv.URL + "/setup/eureka_info")
	if err != nil || name != "Living Room TV" {
		t.Fatalf("probe = %q, %v", name, err)
	}
}

// testListener serves one TLS endpoint with a throwaway self-signed cert.
func testListener(t *testing.T) net.Listener {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "cast-test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func readFrame(t *testing.T, conn net.Conn) message {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hdr [4]byte
	if _, err := io.ReadFull(conn, hdr[:]); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	buf := make([]byte, binary.BigEndian.Uint32(hdr[:]))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read frame: %v", err)
	}
	m, err := decodeMessage(buf)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return m
}

func writeFrame(t *testing.T, conn net.Conn, m message) {
	t.Helper()
	frame := encodeMessage(m)
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(frame)))
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(append(hdr[:], frame...)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// fakeTV answers just enough of the Cast protocol for Launch+Load: it sends
// a PING first (the client must answer PONG, not choke), announces the
// Default Media Receiver after LAUNCH, and acks the LOAD.
func fakeTV(t *testing.T, ln net.Listener, loadReply string) {
	t.Helper()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		ping, _ := json.Marshal(map[string]string{"type": "PING"})
		writeFrame(t, conn, message{source: "receiver-0", dest: senderID, namespace: nsHeartbeat, payload: string(ping)})
		for {
			m := readFrame(t, conn)
			var pl struct {
				Type  string `json:"type"`
				AppID string `json:"appId"`
			}
			_ = json.Unmarshal([]byte(m.payload), &pl)
			switch {
			case m.namespace == nsHeartbeat:
				// PONG from the client; nothing to do.
			case pl.Type == "LAUNCH":
				if pl.AppID != DefaultReceiver {
					t.Errorf("launched %q, want %q", pl.AppID, DefaultReceiver)
				}
				st := `{"type":"RECEIVER_STATUS","status":{"applications":[` +
					`{"appId":"` + DefaultReceiver + `","sessionId":"sess-1",` +
					`"transportId":"tr-1","namespaces":[{"name":"` + nsMedia + `"}]}]}}`
				writeFrame(t, conn, message{source: receiverID, dest: senderID, namespace: nsReceiver, payload: st})
			case pl.Type == "LOAD":
				if !strings.Contains(m.payload, "http://demo.example/x.m3u8") {
					t.Errorf("LOAD missing stream url: %q", m.payload)
				}
				writeFrame(t, conn, message{source: "tr-1", dest: senderID, namespace: nsMedia, payload: loadReply})
				return
			}
		}
	}()
}

func dialTest(t *testing.T, ln net.Listener) *Caster {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Dial(ctx, ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// stopFake is a stateful TV for stop tests: it reports the receiver app
// until a bare receiver STOP arrives, then reports an empty (idle) screen.
// It never calls FailNow: all reads happen off the test goroutine, so I/O
// errors just end the session and findings go through t.Errorf.
type stopFake struct {
	clearOnStop  bool
	sawBareStop  bool
	sawMediaStop bool
}

func (f *stopFake) serve(t *testing.T, ln net.Listener) {
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		stopped := false
		appStatus := `{"type":"RECEIVER_STATUS","status":{"applications":[` +
			`{"appId":"` + DefaultReceiver + `","sessionId":"sess-1",` +
			`"transportId":"tr-1","namespaces":[{"name":"` + nsMedia + `"}]}]}}`
		idleStatus := `{"type":"RECEIVER_STATUS","status":{"applications":[]}}`
		read := func() (message, bool) {
			var hdr [4]byte
			conn.SetReadDeadline(time.Now().Add(10 * time.Second))
			if _, err := io.ReadFull(conn, hdr[:]); err != nil {
				return message{}, false
			}
			buf := make([]byte, binary.BigEndian.Uint32(hdr[:]))
			if _, err := io.ReadFull(conn, buf); err != nil {
				return message{}, false
			}
			m, err := decodeMessage(buf)
			if err != nil {
				t.Errorf("fake decode: %v", err)
				return message{}, false
			}
			return m, true
		}
		write := func(m message) {
			frame := encodeMessage(m)
			var hdr [4]byte
			binary.BigEndian.PutUint32(hdr[:], uint32(len(frame)))
			conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			conn.Write(append(hdr[:], frame...))
		}
		for {
			m, ok := read()
			if !ok {
				return
			}
			var pl struct {
				Type           string `json:"type"`
				AppID          string `json:"appId"`
				SessionID      string `json:"sessionId"`
				MediaSessionID int64  `json:"mediaSessionId"`
			}
			_ = json.Unmarshal([]byte(m.payload), &pl)
			from := message{source: receiverID, dest: senderID}
			switch {
			case m.namespace == nsHeartbeat:
				// PONG; nothing to do.
			case pl.Type == "LAUNCH":
				from.namespace, from.payload = nsReceiver, appStatus
				write(from)
			case pl.Type == "GET_STATUS" && m.namespace == nsReceiver:
				from.namespace = nsReceiver
				if stopped {
					from.payload = idleStatus
				} else {
					from.payload = appStatus
				}
				write(from)
			case pl.Type == "GET_STATUS" && m.namespace == nsMedia:
				from.namespace = nsMedia
				if stopped {
					from.payload = `{"type":"MEDIA_STATUS","status":[]}`
				} else {
					from.payload = `{"type":"MEDIA_STATUS","status":[{"mediaSessionId":7,"playerState":"PLAYING","currentTime":61.5,"playbackRate":1.0,"media":{"duration":3600}}]}`
				}
				write(from)
			case pl.Type == "STOP" && m.namespace == nsMedia:
				f.sawMediaStop = true
			case pl.Type == "STOP" && m.namespace == nsReceiver:
				if pl.SessionID != "" {
					t.Errorf("scoped STOP with sessionId %q; want bare STOP", pl.SessionID)
				}
				f.sawBareStop = true
				if f.clearOnStop {
					stopped = true
				}
			}
		}
	}()
}

func launchTest(t *testing.T, c *Caster) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Launch(ctx); err != nil {
		t.Fatalf("launch: %v", err)
	}
}

func TestStopClearsTV(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	f := &stopFake{clearOnStop: true}
	f.serve(t, ln)
	c := dialTest(t, ln)
	launchTest(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.StopApp(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if !f.sawBareStop {
		t.Error("expected a bare receiver STOP")
	}
	if !f.sawMediaStop {
		t.Error("expected a media-level STOP first")
	}
}

func TestStopNothingRunning(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	f := &stopFake{clearOnStop: true}
	f.serve(t, ln)
	c := dialTest(t, ln)
	// No launch: stopping an idle TV must succeed quietly.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.StopApp(ctx); err != nil {
		t.Fatalf("stop idle: %v", err)
	}
}

func TestStopIgnoredSurfaces(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	f := &stopFake{clearOnStop: false} // TV never leaves the app
	f.serve(t, ln)
	c := dialTest(t, ln)
	launchTest(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.StopApp(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Fatalf("ignored STOP should surface, got %v", err)
	}
}

func TestStatusPoll(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	f := &stopFake{clearOnStop: true}
	f.serve(t, ln)
	c := dialTest(t, ln)
	launchTest(t, c)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	st, rtt, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.PlayerState != "PLAYING" || st.CurrentTime != 61.5 || st.Duration != 3600 || st.Rate != 1.0 {
		t.Fatalf("status = %+v", st)
	}
	if rtt < 0 || ShortState("playing") != "PLAYING" || ShortState("") != "IDLE" {
		t.Fatalf("rtt/state helpers off: %v", rtt)
	}
}

func TestLaunchAndLoad(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	fakeTV(t, ln, `{"type":"MEDIA_STATUS","status":[]}`)
	c := dialTest(t, ln)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Launch(ctx); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if c.SessionID() != "sess-1" {
		t.Fatalf("session = %q, want sess-1", c.SessionID())
	}
	if err := c.Load(ctx, "http://demo.example/x.m3u8", "Demo", GuessContentType("http://demo.example/x.m3u8"), GuessStreamType(true)); err != nil {
		t.Fatalf("load: %v", err)
	}
}

func TestLoadRefused(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	fakeTV(t, ln, `{"type":"LOAD_FAILED","reason":"INVALID_MEDIA"}`)
	c := dialTest(t, ln)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Launch(ctx); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if err := c.Load(ctx, "http://demo.example/x.m3u8", "Demo", "video/mp4", "LIVE"); err == nil {
		t.Fatal("refused LOAD must fail")
	}
}

func TestLoadWithoutLaunch(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	// Accept and hold; the client must fail before speaking media.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		readFrame(t, conn)
	}()
	c := dialTest(t, ln)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Load(ctx, "http://demo.example/x.m3u8", "Demo", "video/mp4", "LIVE"); err == nil {
		t.Fatal("LOAD without launch must fail")
	}
}

// TestUseAfterClose is the regression test for the stop-during-poll panic:
// racing Close against send/recv must surface "closed", never a nil-pointer
// dereference, even under the race detector.
func TestUseAfterClose(t *testing.T) {
	ln := testListener(t)
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Drain until the client goes away; then exit, no leak.
		buf := make([]byte, 4096)
		for {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()
	c := dialTest(t, ln)
	if err := c.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("close must be idempotent: %v", err)
	}
	if err := c.send(payloadType{Type: "GET_STATUS"}, nsReceiver, receiverID); err == nil {
		t.Fatal("send after close must fail")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := c.Status(ctx); err == nil {
		t.Fatal("status after close must fail")
	}
	if _, err := c.receiverApps(ctx); err == nil {
		t.Fatal("apps after close must fail")
	}
}
