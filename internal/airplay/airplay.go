// Package airplay sends the selected channel to an Apple TV over AirPlay:
// plain HTTP to port 7000 (no pairing), a POST of the stream URL to /play,
// and polling of /playback-info while it runs. Only receivers with no
// password (or "Allow Everyone") work: a 401 surfaces as an auth error
// telling the operator what to change on the TV.
//
// Like the cast package this needs only the standard library: discovery
// rides on the already-vendored zeroconf resolver, and proxied streams reuse
// cast.StartProxy so provider headers keep working.
package airplay

import (
	"context"
	"crypto/rand"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/mertturunc/iptv-tui/internal/dbg"
)

// DefaultPort is the AirPlay video port every Apple TV listens on.
const DefaultPort = "7000"

// Device is an AirPlay target: the TV's friendly name plus its LAN address
// (bare host; host:port is honored when given).
type Device struct {
	Name string
	Addr string
}

// PlaybackInfo is the receiver's answer to /playback-info: where playback
// stands, normalized to the same vocabulary the cast sheet uses.
type PlaybackInfo struct {
	State    string  // PLAYING, PAUSED, IDLE
	Position float64 // seconds into the media
	Duration float64 // seconds; 0 when unreported (typical for live)
	Rate     float64 // playback rate, usually 1
}

// ShortState trims a playback state for the one-row playbar.
func ShortState(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch s {
	case "PLAYING", "PAUSED", "IDLE":
		return s
	case "":
		return "IDLE"
	default:
		return s
	}
}

// splitAddr separates an optional :port, falling back to defPort.
func splitAddr(addr, defPort string) (host, port string) {
	addr = strings.TrimSpace(addr)
	if h, p, err := net.SplitHostPort(addr); err == nil {
		if p == "" {
			p = defPort
		}
		return h, p
	}
	return addr, defPort
}

// Client holds one AirPlay session with a TV. AirPlay video is stateless
// plain HTTP (no TLS, no persistent connection), so the client is just the
// TV's address plus a shared HTTP client.
type Client struct {
	host    string
	port    string
	http    *http.Client
	device  string
	session string
}

// userAgent mimics a first-party client (captured from a real one): picky
// receivers refuse requests that do not look like Apple software.
const userAgent = "AirPlay/425.1"

// newSessionID mints the per-client session ID first-party clients send on
// every request.
func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Dial returns a client for the TV at addr (bare host; host:port is honored
// when given). The /server-info handshake only learns the friendly name, so
// a TV that gates it (403 on protected sets) still yields a client: the
// real verdict comes from Play, not the handshake.
func Dial(ctx context.Context, addr string) (*Client, error) {
	host, port := splitAddr(addr, DefaultPort)
	c := &Client{
		host:    host,
		port:    port,
		http:    &http.Client{Timeout: 10 * time.Second},
		session: newSessionID(),
	}
	name, err := c.serverInfo(ctx)
	if err != nil {
		dbg.Log("airplay: dial %s:%s handshake failed (%s), continuing without a name",
			host, port, scrubErr(err))
	} else {
		dbg.Log("airplay: dial %s:%s ok (%q)", host, port, name)
	}
	c.device = name
	return c, nil
}

// Addr is the TV host this client talks to.
func (c *Client) Addr() string { return c.host }

// base returns the http://host:port root of the receiver.
func (c *Client) base() string {
	return "http://" + net.JoinHostPort(c.host, c.port)
}

// httpClient tolerates the zero value: a Client built by hand (as the TUI
// tests do to stand in for a live session) still fails as an error, never
// a nil-pointer panic.
func (c *Client) httpClient() *http.Client {
	if c.http != nil {
		return c.http
	}
	return &http.Client{Timeout: 10 * time.Second}
}

// statusError is a non-2xx answer with its coordinates attached, so callers
// that know an endpoint's semantics (Play) can translate codes into guidance
// instead of printing a bare status.
type statusError struct {
	method string
	path   string
	code   int
	status string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("airplay: %s %s: %s", e.method, e.path, e.status)
}

// do sends one AirPlay request, mapping a 401 to an auth error that tells
// the operator the fix lives on the TV, not here.
func (c *Client) do(ctx context.Context, method, path, contentType string, body io.Reader) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	if c.session != "" {
		req.Header.Set("X-Apple-Session-ID", c.session)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("airplay: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("airplay: tv requires a password: allow everyone (or disable the password) in the Apple TV AirPlay settings, then retry")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &statusError{method: method, path: path, code: resp.StatusCode, status: resp.Status}
	}
	return raw, nil
}

// playPlist builds the /play body: the stream URL plus a start fraction.
// The URL is XML-escaped so provider query strings (with & and friends)
// survive the plist.
func playPlist(streamURL string, start float64) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">` + "\n")
	b.WriteString(`<plist version="1.0"><dict>`)
	b.WriteString(`<key>Content-Location</key><string>`)
	xml.EscapeText(&b, []byte(streamURL))
	b.WriteString(`</string>`)
	fmt.Fprintf(&b, `<key>Start-Position</key><real>%f</real>`, start)
	b.WriteString(`</dict></plist>`)
	return b.String()
}

// Play starts one stream URL on the TV, replacing whatever is playing.
// start is the start fraction (0 = beginning); resume of IPTV streams is
// not meaningful, so callers pass 0.
func (c *Client) Play(ctx context.Context, streamURL string, start float64) error {
	dbg.Log("airplay: PLAY %s", dbg.ShortURL(streamURL))
	_, err := c.do(ctx, http.MethodPost, "/play", "text/x-apple-plist+xml",
		strings.NewReader(playPlist(streamURL, start)))
	if se, ok := err.(*statusError); ok {
		switch se.code {
		case http.StatusForbidden:
			return fmt.Errorf("airplay: tv refused to play: it likely needs device verification, an on-screen code, or a password — set AirPlay to allow everyone (or allow the first-time code) in the TV's AirPlay settings, then retry")
		case http.StatusNotFound:
			return fmt.Errorf("airplay: tv has no /play endpoint: it probably speaks AirPlay 2 only (common on Roku/TCL/Google TV sets), which this client does not implement")
		}
	}
	return err
}

// Stop ends playback on the TV.
func (c *Client) Stop(ctx context.Context) error {
	dbg.Log("airplay: STOP sent")
	_, err := c.do(ctx, http.MethodPost, "/stop", "text/x-apple-plist+xml", nil)
	return err
}

// Status asks the receiver how playback is going. The round-trip measures
// the control link only, never stream bitrate — the TV pulls the stream
// itself, which is what the local proxy measures instead (approach A, same
// as Chromecast).
func (c *Client) Status(ctx context.Context) (*PlaybackInfo, time.Duration, error) {
	start := time.Now()
	raw, err := c.do(ctx, http.MethodGet, "/playback-info", "", nil)
	rtt := time.Since(start)
	if err != nil {
		return nil, 0, err
	}
	st, err := parsePlaybackInfo(raw)
	if err != nil {
		return nil, 0, err
	}
	dbg.Log("airplay: status %s pos=%.1f dur=%.1f rate=%.2f rtt=%dms",
		st.State, st.Position, st.Duration, st.Rate, int(rtt/time.Millisecond))
	return st, rtt, nil
}

// parsePlaybackInfo reads a /playback-info plist into a PlaybackInfo.
// Missing keys stay zero: a receiver with nothing loaded answers nearly
// empty, which reads as IDLE.
func parsePlaybackInfo(raw []byte) (*PlaybackInfo, error) {
	// Walk the dict's elements in order, pairing each <key> with the value
	// that follows it (real, string, integer, true, or false).
	vals := map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(string(raw)))
	dec.Strict = false
	inDict := false
	depth := 0
	key := ""
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "dict":
				if !inDict {
					inDict = true
				} else {
					depth++
				}
			case "key":
				if inDict && depth == 0 {
					var k string
					if err := dec.DecodeElement(&k, &t); err == nil {
						key = k
					}
				}
			case "real", "string", "integer":
				if inDict && depth == 0 && key != "" {
					var v string
					if err := dec.DecodeElement(&v, &t); err == nil {
						vals[key] = v
					}
					key = ""
				}
			case "true", "false":
				if inDict && depth == 0 && key != "" {
					if t.Name.Local == "true" {
						vals[key] = "true"
					} else {
						vals[key] = "false"
					}
					key = ""
				}
				// true/false are empty elements; no EndElement follows, so
				// nothing more to do here.
			default:
				if inDict {
					depth++
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "dict":
				if depth == 0 {
					inDict = false
				} else {
					depth--
				}
			default:
				if inDict && depth > 0 {
					depth--
				}
			}
		}
	}
	if len(vals) == 0 && len(strings.TrimSpace(string(raw))) > 0 {
		// A body that decoded to nothing is suspect, but an empty dict is a
		// legal idle answer: only error when the body is not even XML.
		var probe any
		if err := xml.Unmarshal(raw, &probe); err != nil {
			return nil, fmt.Errorf("airplay: playback-info: %w", err)
		}
	}
	out := &PlaybackInfo{
		Position: numVal(vals, "position"),
		Duration: numVal(vals, "duration"),
		Rate:     numVal(vals, "rate"),
	}
	ready := vals["readyToPlay"] == "true"
	switch {
	case ready && out.Rate != 0:
		out.State = "PLAYING"
	case ready:
		out.State = "PAUSED"
	default:
		out.State = "IDLE"
	}
	return out, nil
}

func numVal(vals map[string]string, key string) float64 {
	var f float64
	fmt.Sscanf(vals[key], "%f", &f)
	return f
}

// serverInfo asks the TV for its plist name (port 7000 /server-info).
func (c *Client) serverInfo(ctx context.Context) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/server-info", "", nil)
	if err != nil {
		return "", err
	}
	// The name sits in <key>Name</key><string>...</string>; a small search
	// beats a full plist parser for one field.
	s := string(raw)
	const open = "<key>Name</key>"
	i := strings.Index(s, open)
	if i < 0 {
		return "", nil
	}
	rest := s[i+len(open):]
	si := strings.Index(rest, "<string>")
	ei := strings.Index(rest, "</string>")
	if si < 0 || ei < 0 || ei <= si {
		return "", nil
	}
	return strings.TrimSpace(rest[si+len("<string>") : ei]), nil
}

// Probe asks a TV for its friendly name over /server-info. When the TV does
// not answer, the address itself stands in as the name so playback can still
// be attempted. An explicit host:port survives in the returned Addr so Dial
// honors it; a bare host dials the default port.
func Probe(addr string) Device {
	trimmed := strings.TrimSpace(addr)
	host, _ := splitAddr(trimmed, DefaultPort)
	c := &Client{host: host, http: &http.Client{Timeout: 4 * time.Second}}
	if _, p, err := net.SplitHostPort(trimmed); err == nil && p != "" {
		c.port = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	name, err := c.serverInfo(ctx)
	if err != nil || name == "" {
		dbg.Log("airplay: probe %s: %s, using address as name", host, scrubErr(err))
		name = host
	} else {
		dbg.Log("airplay: probe %s -> %q", host, name)
	}
	if _, _, err := net.SplitHostPort(trimmed); err == nil {
		return Device{Name: name, Addr: trimmed}
	}
	return Device{Name: name, Addr: host}
}

func scrubErr(err error) string {
	if err == nil {
		return "no name"
	}
	return dbg.Scrub(err.Error())
}
