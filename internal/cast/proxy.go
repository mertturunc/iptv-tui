package cast

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mertturunc/iptv-tui/internal/dbg"
)

// Proxy serves one upstream stream to the TV over the LAN (approach A): all
// bytes flow through here, so throughput is measured rather than guessed,
// and provider headers are injected server-side — where the TV cannot send
// them itself. HLS playlists are rewritten so segments stay behind the
// proxy too; DASH manifests are not rewritten (XML) and cast direct.
type Proxy struct {
	upstream    string
	ua          string
	referer     string
	client      *http.Client
	srv         *http.Server
	urlBase     string
	started     time.Time
	bytes       atomic.Uint64
	reqs        atomic.Uint64
	mu          sync.Mutex
	lastRange   string
	lastSegment string
}

// ShouldProxy reports whether a URL gains from proxying. DASH (.mpd) stays
// direct: only its manifest would flow through here while segments go
// straight to the CDN, which would make the byte counter a liar.
func ShouldProxy(raw string) bool {
	u := strings.ToLower(raw)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return !strings.HasSuffix(u, ".mpd")
}

// StartProxy binds a LAN port and serves upstream from /s. The TV must be
// able to reach this machine: same network, and on first use the OS asks to
// allow the listener through the firewall. tvAddr (the receiver's host)
// picks the advertised address by route, so multi-homed machines (VPNs,
// virtual adapters) don't hand the TV an unreachable IP.
func StartProxy(upstream, ua, referer, tvAddr string) (*Proxy, error) {
	ip, err := advertiseIP(tvAddr)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		return nil, fmt.Errorf("cast: proxy listen: %w", err)
	}
	p := &Proxy{
		upstream: upstream,
		ua:       ua,
		referer:  referer,
		client: &http.Client{
			Transport: &http.Transport{
				ResponseHeaderTimeout: 15 * time.Second,
			},
			// Providers routinely gate segments behind the session cookie
			// set on the manifest fetch. Separate client.Do calls share
			// nothing by default, so without a jar every segment looks
			// unauthenticated. Same-host jar below; no new dependencies.
			Jar: newSimpleJar(),
		},
		started: time.Now(),
	}
	if ua == "" {
		dbg.Log("proxy: no UA configured, sending iptv-tui/1.0")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/s", func(w http.ResponseWriter, r *http.Request) {
		p.serveUpstream(w, r, upstream, upstream)
	})
	mux.HandleFunc("/p", func(w http.ResponseWriter, r *http.Request) {
		target := r.URL.Query().Get("u")
		if target == "" {
			http.Error(w, "missing u", http.StatusBadRequest)
			return
		}
		p.serveUpstream(w, r, target, target)
	})
	// The receiver is a browser context on another origin, so every fetch
	// it makes — manifest, nested playlists, segments, keys — is a CORS
	// request. Without these headers the bytes arrive but the player must
	// reject them unseen: manifests retried, segments never pulled,
	// LOAD_FAILED. This was the Toot outage.
	p.srv = &http.Server{Handler: cors(mux), ReadHeaderTimeout: 10 * time.Second}
	p.urlBase = "http://" + net.JoinHostPort(ip, portOf(ln))
	dbg.Log("proxy: serving %s via %s (ua=%v referer=%v cors=*)",
		dbg.ShortURL(upstream), p.urlBase, ua != "", referer != "")
	go p.srv.Serve(ln) //nolint:errcheck // closed via Close
	return p, nil
}

func portOf(ln net.Listener) string {
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return "0"
	}
	return port
}

// cors wraps every proxy response in the headers a Cast receiver demands.
// Range (used on segment pulls) is not a safelisted header, so players
// preflight with OPTIONS: answer those outright.
func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Range, Content-Type, Accept")
		h.Set("Access-Control-Expose-Headers", "Content-Length, Content-Range, Accept-Ranges")
		h.Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// StreamURL is the URL the TV loads: our /s endpoint for the stream.
func (p *Proxy) StreamURL() string { return p.urlBase + "/s" }

// BaseURL is the LAN address handed to the TV, for display and debugging.
func (p *Proxy) BaseURL() string { return p.urlBase }

// Bytes is the cumulative media payload served so far; samplers derive
// bitrate from consecutive reads.
func (p *Proxy) Bytes() uint64 { return p.bytes.Load() }

// Requests counts TV fetches (manifest + segments).
func (p *Proxy) Requests() uint64 { return p.reqs.Load() }

// LastRange is the most recent Range header the TV sent — which part of a
// progressive stream it is pulling (empty for live/sequential fetches).
func (p *Proxy) LastRange() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastRange
}

// Elapsed since the proxy started serving.
func (p *Proxy) Elapsed() time.Duration { return time.Since(p.started) }

// Close stops serving. A TV mid-stream stalls: like quitting while a local
// player runs, ending the process ends a proxied cast by design.
func (p *Proxy) Close() error { return p.srv.Close() }

type countingWriter struct {
	w io.Writer
	n *atomic.Uint64
}

func (c countingWriter) Write(b []byte) (int, error) {
	n, err := c.w.Write(b)
	c.n.Add(uint64(n))
	return n, err
}

func isPlaylistURL(raw string) bool {
	u := strings.ToLower(raw)
	if i := strings.IndexAny(u, "?#"); i >= 0 {
		u = u[:i]
	}
	return strings.HasSuffix(u, ".m3u8")
}

// serveUpstream fetches target with the provider headers and streams it to
// the TV, counting payload bytes. Playlists are rewritten onto /p so every
// segment and key stays measurable.
func (p *Proxy) serveUpstream(w http.ResponseWriter, r *http.Request, target, base string) {
	p.reqs.Add(1)
	up, err := http.NewRequestWithContext(r.Context(), http.MethodGet, target, nil)
	if err != nil {
		http.Error(w, "bad upstream", http.StatusBadGateway)
		return
	}
	if p.ua != "" {
		up.Header.Set("User-Agent", p.ua)
	} else {
		// An empty UA is a WAF magnet; mpv and browsers always send one.
		// Override per-source or via DefaultUserAgent when a panel insists.
		up.Header.Set("User-Agent", "iptv-tui/1.0")
	}
	if p.referer != "" {
		up.Header.Set("Referer", p.referer)
	}
	if rng := r.Header.Get("Range"); rng != "" {
		up.Header.Set("Range", rng)
		p.mu.Lock()
		p.lastRange = rng
		p.mu.Unlock()
	}
	resp, err := p.client.Do(up)
	if err != nil {
		dbg.Log("proxy: upstream %s: %s", dbg.ShortURL(target), dbg.Scrub(err.Error()))
		http.Error(w, "upstream unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if strings.Contains(resp.Header.Get("Content-Type"), "mpegurl") || isPlaylistURL(target) {
		// A non-200 manifest is forwarded honestly: serving an error page
		// as 200 application/x-mpegURL sends the receiver into a retry
		// loop over a "playlist" with no playable segments.
		if resp.StatusCode != http.StatusOK {
			dbg.Log("proxy: playlist %s upstream status=%d, forwarding error",
				dbg.ShortURL(target), resp.StatusCode)
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(io.Discard, resp.Body)
			return
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		if err != nil {
			dbg.Log("proxy: playlist %s read: %s", dbg.ShortURL(target), dbg.Scrub(err.Error()))
			http.Error(w, "playlist read", http.StatusBadGateway)
			return
		}
		rewritten, segs := rewritePlaylistCount(string(body), base, p.urlBase)
		dbg.Log("proxy: playlist %s status=%d %s -> %d segment(s) via %s",
			dbg.ShortURL(target), resp.StatusCode, playlistTags(string(body)), segs, p.urlBase)
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.WriteHeader(http.StatusOK)
		n, _ := io.WriteString(countingWriter{w: w, n: &p.bytes}, rewritten)
		dbg.Log("proxy: playlist served %d bytes to %s", n, r.RemoteAddr)
		return
	}
	for _, h := range []string{"Content-Type", "Content-Length", "Accept-Ranges", "Content-Range"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	b0 := p.bytes.Load()
	_, _ = io.Copy(countingWriter{w: w, n: &p.bytes}, resp.Body)
	dbg.Log("proxy: %s -> %s status=%d range=%q served=%d bytes",
		dbg.ShortURL(target), r.RemoteAddr, resp.StatusCode, r.Header.Get("Range"), p.bytes.Load()-b0)
}

// proxyURL points a segment/key/playlist at our /p endpoint.
func (p *Proxy) proxyURL(u string) string {
	return p.urlBase + "/p?u=" + url.QueryEscape(u)
}

// rewritePlaylist points every fetchable URI in an HLS playlist at the
// proxy: bare segment lines, nested playlists, and URI="..." attributes
// (keys, maps, media). Relative references resolve against the playlist URL.
func rewritePlaylist(body, base, urlBase string) string {
	out, _ := rewritePlaylistCount(body, base, urlBase)
	return out
}

// playlistTags summarizes an HLS playlist for logs: variant count, codecs,
// key method, and live/event shape. Codec strings and tag values carry no
// credentials, so they log raw — this is the diagnostic for receivers that
// fetch a manifest but refuse every rendition.
func playlistTags(body string) string {
	var parts []string
	variants, keys := 0, ""
	codecs := map[string]bool{}
	var codecList []string
	version, targetDur := "", ""
	live := true
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(s, "#EXT-X-VERSION:"):
			version = strings.TrimPrefix(s, "#EXT-X-VERSION:")
		case strings.HasPrefix(s, "#EXT-X-TARGETDURATION:"):
			targetDur = strings.TrimPrefix(s, "#EXT-X-TARGETDURATION:")
		case strings.HasPrefix(s, "#EXT-X-STREAM-INF"):
			variants++
			if i := strings.Index(s, `CODECS="`); i >= 0 {
				rest := s[i+8:]
				if j := strings.Index(rest, `"`); j >= 0 {
					for _, c := range strings.Split(rest[:j], ",") {
						c = strings.TrimSpace(c)
						if c != "" && !codecs[c] {
							codecs[c] = true
							codecList = append(codecList, c)
						}
					}
				}
			}
		case strings.HasPrefix(s, "#EXT-X-KEY:"):
			keys = keyMethod(s)
		case strings.HasPrefix(s, "#EXT-X-ENDLIST"):
			live = false
		}
	}
	if version != "" {
		parts = append(parts, "v"+version)
	}
	if variants > 0 {
		parts = append(parts, fmt.Sprintf("%d variant(s)", variants))
	}
	if len(codecList) > 0 {
		parts = append(parts, "codecs="+strings.Join(codecList, "+"))
	}
	if keys != "" {
		parts = append(parts, "key="+keys)
	}
	if targetDur != "" {
		parts = append(parts, "dur="+targetDur)
	}
	if live {
		parts = append(parts, "live")
	} else {
		parts = append(parts, "event")
	}
	if len(parts) == 0 {
		return "untagged"
	}
	return strings.Join(parts, " ")
}

// keyMethod pulls METHOD= out of an #EXT-X-KEY line.
func keyMethod(line string) string {
	if i := strings.Index(line, "METHOD="); i >= 0 {
		rest := line[i+7:]
		if j := strings.Index(rest, ","); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	return ""
}
func rewritePlaylistCount(body, base, urlBase string) (string, int) {
	segs := 0
	resolve := func(ref string) string {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return ref
		}
		if b, err := url.Parse(base); err == nil {
			if u, err := b.Parse(ref); err == nil {
				ref = u.String()
			}
		}
		segs++
		return urlBase + "/p?u=" + url.QueryEscape(ref)
	}
	var out strings.Builder
	for _, line := range strings.Split(body, "\n") {
		t := strings.TrimRight(line, "\r")
		s := strings.TrimSpace(t)
		if s == "" || strings.HasPrefix(s, "#EXTM3U") {
			out.WriteString(t + "\n")
			continue
		}
		if strings.HasPrefix(s, "#") {
			out.WriteString(rewriteQuotedURIs(t, resolve) + "\n")
			continue
		}
		out.WriteString(resolve(s) + "\n")
	}
	return out.String(), segs
}

// rewriteQuotedURIs rewrites URI="..." attributes inside tag lines.
func rewriteQuotedURIs(line string, resolve func(string) string) string {
	var out strings.Builder
	for {
		i := strings.Index(line, `URI="`)
		if i < 0 {
			out.WriteString(line)
			return out.String()
		}
		j := strings.Index(line[i+5:], `"`)
		if j < 0 {
			out.WriteString(line)
			return out.String()
		}
		out.WriteString(line[:i+5] + resolve(line[i+5:i+5+j]) + `"`)
		line = line[i+5+j+1:]
	}
}

// advertiseIP picks the address the TV should fetch from: the local address
// of the route toward the TV (a UDP dial moves no packets, it only asks the
// kernel), falling back to LanIP when the TV address is empty or unroutable.
func advertiseIP(tvAddr string) (string, error) {
	if host := stripPort(tvAddr); host != "" {
		if conn, err := net.DialTimeout("udp", net.JoinHostPort(host, "8009"), 2*time.Second); err == nil {
			if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
				conn.Close()
				if ip := addr.IP.To4(); ip != nil && !ip.IsLoopback() {
					return ip.String(), nil
				}
			}
			conn.Close()
		}
	}
	return LanIP()
}

// simpleJar is a minimal cookie jar: exact-host plus domain-suffix match,
// path prefix, expiry and secure enforcement. Enough for provider session
// cookies without pulling in a public-suffix dependency.
type simpleJar struct {
	mu      sync.Mutex
	cookies map[string]map[string]*http.Cookie // host-or-domain -> name -> cookie
}

func newSimpleJar() *simpleJar { return &simpleJar{cookies: map[string]map[string]*http.Cookie{}} }

// SetCookies implements http.CookieJar.
func (j *simpleJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, c := range cs {
		key := strings.ToLower(u.Hostname())
		if c.Domain != "" {
			key = strings.ToLower(strings.TrimPrefix(c.Domain, "."))
		}
		if j.cookies[key] == nil {
			j.cookies[key] = map[string]*http.Cookie{}
		}
		if c.Path == "" {
			c.Path = "/"
		}
		j.cookies[key][c.Name] = c
	}
}

// Cookies implements http.CookieJar.
func (j *simpleJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := strings.ToLower(u.Hostname())
	now := time.Now()
	var out []*http.Cookie
	for domain, byName := range j.cookies {
		if host != domain && !strings.HasSuffix(host, "."+domain) {
			continue
		}
		for _, c := range byName {
			if !c.Expires.IsZero() && now.After(c.Expires) {
				continue
			}
			if c.MaxAge < 0 {
				continue
			}
			if !strings.HasPrefix(u.EscapedPath(), c.Path) && !strings.HasPrefix(u.Path, c.Path) {
				continue
			}
			if c.Secure && u.Scheme != "https" {
				continue
			}
			out = append(out, c)
		}
	}
	return out
}

// LanIP returns this machine's LAN address: the first private IPv4 on an
// up interface, else any non-loopback IPv4.
func LanIP() (string, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var ips []net.IP
	for _, iface := range ifs {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil {
				continue
			}
			if ip = ip.To4(); ip == nil || ip.IsLoopback() {
				continue
			}
			ips = append(ips, ip)
		}
	}
	if pick := selectIP(ips); pick != "" {
		return pick, nil
	}
	return "", fmt.Errorf("cast: no LAN address")
}

func isPrivate(ip net.IP) bool {
	return ip.IsPrivate()
}

// selectIP prefers RFC1918 addresses, else the first candidate.
func selectIP(ips []net.IP) string {
	for _, ip := range ips {
		if isPrivate(ip) {
			return ip.String()
		}
	}
	if len(ips) > 0 {
		return ips[0].String()
	}
	return ""
}
