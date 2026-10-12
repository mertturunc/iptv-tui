// Package cast sends the selected channel to a Google TV over Chromecast:
// a TLS connection to port 8009, a Default Media Receiver launch, and a
// LOAD of the stream URL. The CastMessage framing is hand-rolled (six small
// protobuf fields), so this needs only the standard library and adds no new
// module dependencies.
package cast

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mertturunc/iptv-tui/internal/dbg"
)

// DefaultReceiver is the prebuilt Chromecast app that plays plain media URLs
// without any registration.
const DefaultReceiver = "CC1AD845"

const (
	nsConnection = "urn:x-cast:com.google.cast.tp.connection"
	nsReceiver   = "urn:x-cast:com.google.cast.receiver"
	nsMedia      = "urn:x-cast:com.google.cast.media"
	nsHeartbeat  = "urn:x-cast:com.google.cast.tp.heartbeat"

	senderID   = "sender-0"
	receiverID = "receiver-0"
)

// Device is a Chromecast target: the TV's friendly name plus its LAN address
// (bare host; the DIAL and Cast ports are fixed).
type Device struct {
	Name string
	Addr string
}

// stripPort drops any :port suffix so "tv.lan:8008" and "tv.lan" agree.
func stripPort(addr string) string {
	host, _ := splitAddr(addr, "")
	return host
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

// Probe asks a TV for its friendly name over DIAL (port 8008). When the TV
// does not answer, the address itself stands in as the name so a cast can
// still be attempted.
func Probe(addr string) Device {
	host, port := splitAddr(addr, "8008")
	name, err := probeURL("http://" + net.JoinHostPort(host, port) + "/setup/eureka_info")
	if err != nil || name == "" {
		dbg.Log("cast: probe %s: %s, using address as name", host, dbg.Scrub(errString(err)))
		name = host
	} else {
		dbg.Log("cast: probe %s -> %q", host, name)
	}
	return Device{Name: name, Addr: host}
}

func errString(err error) string {
	if err == nil {
		return "no name"
	}
	return err.Error()
}

func probeURL(url string) (string, error) {
	client := http.Client{Timeout: 4 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("eureka: %s", resp.Status)
	}
	var info struct {
		Name string `json:"name"`
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", err
	}
	return strings.TrimSpace(info.Name), nil
}

// GuessContentType maps a stream URL to the MIME type the Default Media
// Receiver wants. The query string is ignored so Xtream URLs carrying
// credentials still match on their path suffix.
func GuessContentType(raw string) string {
	u := strings.ToLower(raw)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	switch {
	case strings.HasSuffix(u, ".m3u8"):
		return "application/x-mpegURL"
	case strings.HasSuffix(u, ".mpd"):
		return "application/dash+xml"
	case strings.HasSuffix(u, ".mp4"), strings.HasSuffix(u, ".m4v"),
		strings.HasSuffix(u, ".mov"):
		return "video/mp4"
	case strings.HasSuffix(u, ".webm"):
		return "video/webm"
	case strings.HasSuffix(u, ".mkv"):
		return "video/x-matroska"
	case strings.HasSuffix(u, ".avi"):
		return "video/x-msvideo"
	case strings.HasSuffix(u, ".ts"), strings.HasSuffix(u, ".m2ts"):
		return "video/mp2t"
	case strings.HasSuffix(u, ".mp3"):
		return "audio/mpeg"
	case strings.HasSuffix(u, ".m4a"), strings.HasSuffix(u, ".aac"):
		return "audio/mp4"
	case strings.HasSuffix(u, ".flac"):
		return "audio/flac"
	case strings.HasSuffix(u, ".wav"):
		return "audio/wav"
	case strings.HasSuffix(u, ".jpg"), strings.HasSuffix(u, ".jpeg"):
		return "image/jpeg"
	case strings.HasSuffix(u, ".png"):
		return "image/png"
	case strings.HasSuffix(u, ".gif"):
		return "image/gif"
	case strings.HasSuffix(u, ".webp"):
		return "image/webp"
	default:
		return "video/mp4"
	}
}

// GuessStreamType tells the receiver whether the URL is endless (live TV) or
// seekable (movies, episodes).
func GuessStreamType(live bool) string {
	if live {
		return "LIVE"
	}
	return "BUFFERED"
}

// ---------- framing ----------

// message is one decoded CastMessage: routing plus a JSON payload.
type message struct {
	source    string
	dest      string
	namespace string
	payload   string
}

func appendVarint(b []byte, v uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	return append(b, tmp[:binary.PutUvarint(tmp[:], v)]...)
}

func appendField(b []byte, num int, s string) []byte {
	b = appendVarint(b, uint64(num<<3|2)) // length-delimited
	b = appendVarint(b, uint64(len(s)))
	return append(b, s...)
}

// encodeMessage serializes the six CastMessage fields. Only field 1
// (protocol_version, always CASTV2_1_0 = 0) and field 5 (payload_type,
// always 0 = string) are varints; the rest are length-delimited strings.
func encodeMessage(m message) []byte {
	b := make([]byte, 0, 128)
	b = appendVarint(b, uint64(1<<3|0))
	b = appendVarint(b, 0)
	b = appendField(b, 2, m.source)
	b = appendField(b, 3, m.dest)
	b = appendField(b, 4, m.namespace)
	b = appendVarint(b, uint64(5<<3|0))
	b = appendVarint(b, 0)
	b = appendField(b, 6, m.payload)
	return b
}

func decodeMessage(b []byte) (message, error) {
	var m message
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			return m, fmt.Errorf("cast: bad field key")
		}
		b = b[n:]
		num, wire := int(key>>3), int(key&7)
		switch wire {
		case 0: // varint
			v, n := binary.Uvarint(b)
			if n <= 0 {
				return m, fmt.Errorf("cast: bad varint")
			}
			b = b[n:]
			// ProtocolVersion is CASTV2_1_0 = 0; anything else is a
			// protocol we do not speak.
			if num == 1 && v != 0 {
				return m, fmt.Errorf("cast: protocol version %d", v)
			}
		case 2: // length-delimited
			ln, n := binary.Uvarint(b)
			if n <= 0 || uint64(len(b[n:])) < ln {
				return m, fmt.Errorf("cast: bad length")
			}
			s := string(b[n : n+int(ln)])
			b = b[n+int(ln):]
			switch num {
			case 2:
				m.source = s
			case 3:
				m.dest = s
			case 4:
				m.namespace = s
			case 6:
				m.payload = s
			}
		default:
			return m, fmt.Errorf("cast: wire type %d", wire)
		}
	}
	return m, nil
}

// ---------- payloads ----------

type payloadType struct {
	Type      string `json:"type"`
	RequestID int    `json:"requestId,omitempty"`
}

type receiverApp struct {
	AppID       string `json:"appId"`
	SessionID   string `json:"sessionId"`
	TransportID string `json:"transportId"`
	Namespaces  []struct {
		Name string `json:"name"`
	} `json:"namespaces"`
}

type receiverStatus struct {
	Type   string `json:"type"`
	Status *struct {
		Applications []receiverApp `json:"applications"`
	} `json:"status"`
}

// ---------- sender ----------

// Caster holds one live TLS session to a TV. Calls serialize on a mutex;
// the TUI drives it from a single goroutine with stop arriving from another.
type Caster struct {
	mu          sync.Mutex
	conn        net.Conn
	addr        string
	port        string
	reqID       int
	sessionID   string
	transportID string
}

// Dial opens the TLS session (Chromecast serves a self-signed cert, hence
// the skip) and says hello on the receiver channel. addr is a bare host;
// host:port is honored when given.
func Dial(ctx context.Context, addr string) (*Caster, error) {
	host, port := splitAddr(addr, "8009")
	d := &tls.Dialer{Config: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // Chromecast certs are self-signed by design
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("cast: dial %s: %w", host, err)
	}
	dbg.Log("cast: dial %s:%s TLS ok", host, port)
	c := &Caster{conn: conn, addr: host, port: port}
	if err := c.send(payloadType{Type: "CONNECT"}, nsConnection, receiverID); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

// Addr is the TV host this caster talks to.
func (c *Caster) Addr() string { return c.addr }

// SessionID names the running receiver app, empty until Launch succeeds.
func (c *Caster) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

func (c *Caster) nextID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reqID++
	return c.reqID
}

func (c *Caster) liveConn() (net.Conn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil, fmt.Errorf("cast: closed")
	}
	return c.conn, nil
}

func (c *Caster) send(pl any, namespace, dest string) error {
	raw, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	// The connection is copied under lock and used afterwards: Close may
	// nil it concurrently (stop racing a poll), which must surface as an
	// error, never a nil-pointer panic.
	conn, err := c.liveConn()
	if err != nil {
		return err
	}
	frame := encodeMessage(message{
		source: senderID, dest: dest, namespace: namespace, payload: string(raw),
	})
	out := make([]byte, 4+len(frame))
	binary.BigEndian.PutUint32(out, uint32(len(frame)))
	copy(out[4:], frame)
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write(out)
	return err
}

func (c *Caster) recv(deadline time.Time) (message, error) {
	conn, err := c.liveConn()
	if err != nil {
		return message{}, err
	}
	var hdr [4]byte
	for {
		conn.SetReadDeadline(deadline)
		if _, err := io.ReadFull(conn, hdr[:]); err != nil {
			return message{}, fmt.Errorf("cast: read: %w", err)
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > 1<<20 {
			return message{}, fmt.Errorf("cast: bad frame size %d", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return message{}, fmt.Errorf("cast: read: %w", err)
		}
		m, err := decodeMessage(buf)
		if err != nil {
			return message{}, err
		}
		dbg.Log("cast: rx ns=%s dest=%s %s", shortNS(m.namespace), m.dest, shortType(m.payload))
		// Heartbeats are housekeeping: answer and keep listening for the
		// real reply instead of handing a PING to the caller.
		if m.namespace == nsHeartbeat && strings.Contains(m.payload, "PING") {
			_ = c.send(payloadType{Type: "PONG"}, nsHeartbeat, m.source)
			continue
		}
		return m, nil
	}
}

// shortNS trims the long urn:x-cast:... namespaces for readable logs.
func shortNS(ns string) string {
	if i := strings.LastIndex(ns, ":"); i >= 0 {
		return ns[i+1:]
	}
	return ns
}

// shortType names a payload's message type without dumping URLs that may
// carry credentials (LOAD/MEDIA_STATUS embed contentId).
func shortType(payload string) string {
	var pl struct {
		Type   string `json:"type"`
		AppID  string `json:"appId"`
		Reason string `json:"reason,omitempty"`
	}
	if err := json.Unmarshal([]byte(payload), &pl); err != nil {
		return fmt.Sprintf("(%d bytes)", len(payload))
	}
	s := pl.Type
	if pl.AppID != "" {
		s += " app=" + pl.AppID
	}
	if pl.Reason != "" {
		s += " reason=" + pl.Reason
	}
	return s
}

func deadlineOf(ctx context.Context, fallback time.Duration) time.Time {
	if dl, ok := ctx.Deadline(); ok {
		return dl
	}
	return time.Now().Add(fallback)
}

// Launch starts the Default Media Receiver and waits for its media channel.
// The receiver announces the app (with a transport ID) in a RECEIVER_STATUS
// broadcast; a GET_STATUS nudge covers receivers that stay quiet.
func (c *Caster) Launch(ctx context.Context) error {
	type launchPayload struct {
		payloadType
		AppID string `json:"appId"`
	}
	if err := c.send(launchPayload{payloadType: payloadType{Type: "LAUNCH", RequestID: c.nextID()}, AppID: DefaultReceiver}, nsReceiver, receiverID); err != nil {
		return err
	}
	dbg.Log("cast: LAUNCH %s sent", DefaultReceiver)
	if err := c.send(payloadType{Type: "GET_STATUS", RequestID: c.nextID()}, nsReceiver, receiverID); err != nil {
		return err
	}
	deadline := deadlineOf(ctx, 15*time.Second)
	for {
		m, err := c.recv(deadline)
		if err != nil {
			return err
		}
		if m.namespace != nsReceiver {
			continue
		}
		var st receiverStatus
		if err := json.Unmarshal([]byte(m.payload), &st); err != nil {
			continue
		}
		if st.Type != "RECEIVER_STATUS" || st.Status == nil {
			continue
		}
		for _, app := range st.Status.Applications {
			for _, ns := range app.Namespaces {
				if ns.Name == nsMedia && app.TransportID != "" {
					c.mu.Lock()
					c.sessionID, c.transportID = app.SessionID, app.TransportID
					c.mu.Unlock()
					dbg.Log("cast: receiver app=%s session=%s transport=%s", app.AppID, app.SessionID, app.TransportID)
					return c.send(payloadType{Type: "CONNECT"}, nsConnection, app.TransportID)
				}
			}
		}
	}
}

// Load plays one stream URL on the launched receiver. title shows on the TV;
// mime comes from GuessContentType and streamType from GuessStreamType.
func (c *Caster) Load(ctx context.Context, streamURL, title, mime, streamType string) error {
	c.mu.Lock()
	transport, session := c.transportID, c.sessionID
	c.mu.Unlock()
	if transport == "" {
		return fmt.Errorf("cast: no receiver launched")
	}
	type mediaInfo struct {
		ContentID   string `json:"contentId"`
		ContentType string `json:"contentType"`
		StreamType  string `json:"streamType"`
		Metadata    struct {
			Title string `json:"title"`
		} `json:"metadata"`
	}
	type loadPayload struct {
		payloadType
		SessionID string    `json:"sessionId"`
		Media     mediaInfo `json:"media"`
		Autoplay  bool      `json:"autoplay"`
	}
	var mi mediaInfo
	mi.ContentID, mi.ContentType, mi.StreamType = streamURL, mime, streamType
	mi.Metadata.Title = title
	dbg.Log("cast: LOAD %s mime=%s stream=%s title=%q", dbg.ShortURL(streamURL), mime, streamType, title)
	if err := c.send(loadPayload{
		payloadType: payloadType{Type: "LOAD", RequestID: c.nextID()},
		SessionID:   session, Media: mi, Autoplay: true,
	}, nsMedia, transport); err != nil {
		return err
	}
	deadline := deadlineOf(ctx, 15*time.Second)
	for {
		m, err := c.recv(deadline)
		if err != nil {
			return err
		}
		if m.namespace != nsMedia {
			continue
		}
		var st struct {
			Type   string `json:"type"`
			Reason string `json:"reason,omitempty"`
		}
		if err := json.Unmarshal([]byte(m.payload), &st); err != nil {
			continue
		}
		switch st.Type {
		case "MEDIA_STATUS":
			dbg.Log("cast: LOAD accepted")
			return nil
		case "LOAD_FAILED", "LOAD_CANCELLED":
			if st.Reason == "" {
				st.Reason = st.Type
			}
			dbg.Log("cast: LOAD refused (%s)", st.Reason)
			return fmt.Errorf("cast: tv refused the stream (%s)", st.Reason)
		}
	}
}

// receiverApps asks the TV who is running right now.
func (c *Caster) receiverApps(ctx context.Context) ([]receiverApp, error) {
	if err := c.send(payloadType{Type: "GET_STATUS", RequestID: c.nextID()}, nsReceiver, receiverID); err != nil {
		return nil, err
	}
	deadline := deadlineOf(ctx, 8*time.Second)
	for {
		m, err := c.recv(deadline)
		if err != nil {
			return nil, err
		}
		if m.namespace != nsReceiver {
			continue
		}
		var st receiverStatus
		if err := json.Unmarshal([]byte(m.payload), &st); err != nil {
			continue
		}
		if st.Type != "RECEIVER_STATUS" || st.Status == nil {
			continue
		}
		return st.Status.Applications, nil
	}
}

// mediaSessionID asks the media channel for the live media session, 0 when
// nothing is playing. Best-effort: callers ignore the error.
func (c *Caster) mediaSessionID(ctx context.Context) int64 {
	c.mu.Lock()
	transport := c.transportID
	c.mu.Unlock()
	if transport == "" {
		return 0
	}
	if err := c.send(payloadType{Type: "GET_STATUS", RequestID: c.nextID()}, nsMedia, transport); err != nil {
		return 0
	}
	deadline := deadlineOf(ctx, 3*time.Second)
	for {
		m, err := c.recv(deadline)
		if err != nil {
			return 0
		}
		if m.namespace != nsMedia {
			continue
		}
		var st struct {
			Type   string `json:"type"`
			Status []struct {
				MediaSessionID int64 `json:"mediaSessionId"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(m.payload), &st); err != nil {
			continue
		}
		if st.Type != "MEDIA_STATUS" || len(st.Status) == 0 {
			continue
		}
		return st.Status[0].MediaSessionID
	}
}

func appRunning(apps []receiverApp) bool {
	for _, app := range apps {
		if app.AppID == DefaultReceiver {
			return true
		}
		for _, ns := range app.Namespaces {
			if ns.Name == nsMedia {
				return true
			}
		}
	}
	return false
}

// StopApp ends playback on the TV: a media-level STOP when a session is
// live, then a bare receiver STOP (no session ID — a scoped STOP is what
// receivers ignore), then it waits for the app to actually disappear so a
// silent ignore surfaces as an error instead of a false "TV stopped".
func (c *Caster) StopApp(ctx context.Context) error {
	c.mu.Lock()
	transport := c.transportID
	c.mu.Unlock()
	if msID := c.mediaSessionID(ctx); msID != 0 && transport != "" {
		type mediaStop struct {
			payloadType
			MediaSessionID int64 `json:"mediaSessionId"`
		}
		dbg.Log("cast: media STOP session=%d", msID)
		_ = c.send(mediaStop{
			payloadType:    payloadType{Type: "STOP", RequestID: c.nextID()},
			MediaSessionID: msID,
		}, nsMedia, transport)
	}
	if err := c.send(payloadType{Type: "CONNECT"}, nsConnection, receiverID); err != nil {
		return err
	}
	dbg.Log("cast: receiver STOP sent")
	if err := c.send(payloadType{Type: "STOP", RequestID: c.nextID()}, nsReceiver, receiverID); err != nil {
		return err
	}
	deadline := deadlineOf(ctx, 8*time.Second)
	for {
		apps, err := c.receiverApps(ctx)
		if err != nil {
			if time.Now().After(deadline) {
				dbg.Log("cast: stop confirm failed: %s", dbg.Scrub(err.Error()))
				return fmt.Errorf("cast: tv did not stop (%v)", err)
			}
			continue
		}
		if !appRunning(apps) {
			dbg.Log("cast: stop confirmed, %d app(s) left", len(apps))
			return nil
		}
		if time.Now().After(deadline) {
			dbg.Log("cast: stop ignored, still running: %+v", apps)
			return fmt.Errorf("cast: tv did not stop")
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Close drops the TLS session. The TV keeps playing: like keep-mpv-on-quit,
// a cast outlives the terminal by design.
func (c *Caster) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}
