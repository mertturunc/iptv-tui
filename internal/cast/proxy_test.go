package cast

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRewritePlaylist(t *testing.T) {
	body := "#EXTM3U\n" +
		"#EXT-X-VERSION:3\n" +
		"#EXT-X-TARGETDURATION:10\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n" +
		"#EXTINF:10.0,\n" +
		"seg1.ts\n" +
		"#EXTINF:10.0,\n" +
		"https://cdn.example.com/abs/seg2.ts\n" +
		"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",URI=\"audio.m3u8\"\n" +
		"#EXT-X-ENDLIST\n"
	out := rewritePlaylist(body, "https://x.example.com/live/list.m3u8", "http://127.0.0.1:9")
	for _, want := range []string{
		"/p?u=https%3A%2F%2Fx.example.com%2Flive%2Fseg1.ts",
		"/p?u=https%3A%2F%2Fcdn.example.com%2Fabs%2Fseg2.ts",
		`URI="http://127.0.0.1:9/p?u=https%3A%2F%2Fx.example.com%2Flive%2Fkey.bin"`,
		"/p?u=https%3A%2F%2Fx.example.com%2Flive%2Faudio.m3u8",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rewrite missing %q in:\n%s", want, out)
		}
	}
	if !strings.HasPrefix(out, "#EXTM3U\n") || !strings.Contains(out, "#EXT-X-ENDLIST") {
		t.Errorf("tags must survive rewriting:\n%s", out)
	}
}

func TestPlaylistTags(t *testing.T) {
	master := "#EXTM3U\n" +
		"#EXT-X-VERSION:6\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=800000,CODECS=\"avc1.64001f,mp4a.40.2\"\n" +
		"low.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=4000000,CODECS=\"hvc1.1.6.L123,b6,ec-3\"\n" +
		"hi.m3u8\n"
	got := playlistTags(master)
	for _, want := range []string{"v6", "2 variant(s)", "avc1.64001f", "hvc1.1.6.L123", "live"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	media := "#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:10,\ns.ts\n#EXT-X-ENDLIST\n"
	got = playlistTags(media)
	for _, want := range []string{"dur=10", "key=AES-128", "event"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestShouldProxy(t *testing.T) {
	if !ShouldProxy("http://x/y.m3u8") || !ShouldProxy("http://x/live/u/p/1.ts") {
		t.Fatal("hls and progressive streams should proxy")
	}
	if ShouldProxy("http://x/y.mpd?token=z") {
		t.Fatal("dash must stay direct (no manifest rewrite)")
	}
}

func TestSelectIP(t *testing.T) {
	ips := []net.IP{net.ParseIP("203.0.113.9"), net.ParseIP("192.168.1.7")}
	if got := selectIP(ips); got != "192.168.1.7" {
		t.Fatalf("prefer private, got %q", got)
	}
	if got := selectIP(nil); got != "" {
		t.Fatalf("empty in, empty out; got %q", got)
	}
}

func TestAdvertiseIPFollowsRoute(t *testing.T) {
	// A UDP dial moves no packets; the kernel only reveals the source
	// address of the route. TEST-NET-1 is unroutable in the streaming
	// sense but still resolves a local source address.
	got, err := advertiseIP("192.0.2.1")
	if err != nil {
		t.Fatalf("advertise: %v", err)
	}
	ip := net.ParseIP(got)
	if ip == nil || ip.To4() == nil || ip.IsLoopback() {
		t.Fatalf("advertise = %q, want a usable IPv4", got)
	}
}

func TestProxyServesAndCounts(t *testing.T) {
	var gotUA, gotRef, gotRange string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotRef, gotRange = r.UserAgent(), r.Referer(), r.Header.Get("Range")
		if gotRange != "" {
			w.Header().Set("Content-Range", "bytes 4-9/10")
			w.WriteHeader(http.StatusPartialContent)
			w.Write([]byte("012345"))
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write([]byte("0123456789"))
	}))
	defer up.Close()

	p, err := StartProxy(up.URL+"/s.ts", "test-ua/1", "https://ref.example/", "127.0.0.1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Close()
	if !strings.HasPrefix(p.StreamURL(), "http://") || !strings.HasSuffix(p.StreamURL(), "/s") {
		t.Fatalf("stream url %q", p.StreamURL())
	}

	// Plain fetch: headers injected, bytes counted.
	resp, err := http.Get(p.StreamURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "0123456789" {
		t.Fatalf("body %q", body)
	}
	if gotUA != "test-ua/1" || gotRef != "https://ref.example/" {
		t.Fatalf("upstream headers ua=%q ref=%q", gotUA, gotRef)
	}
	if p.Bytes() != 10 || p.Requests() != 1 {
		t.Fatalf("bytes=%d reqs=%d", p.Bytes(), p.Requests())
	}

	// Range fetch passes through with 206 and records the range.
	req, _ := http.NewRequest(http.MethodGet, p.StreamURL(), nil)
	req.Header.Set("Range", "bytes=4-9")
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("range get: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d", resp2.StatusCode)
	}
	if p.LastRange() != "bytes=4-9" || gotRange != "bytes=4-9" {
		t.Fatalf("range not tracked: proxy=%q upstream=%q", p.LastRange(), gotRange)
	}
}

func TestProxyRewritesPlaylistEndToEnd(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list.m3u8":
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n#EXTINF:10,\nseg1.ts\n")
		case "/seg1.ts":
			w.Write([]byte("SEGMENT"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	p, err := StartProxy(up.URL+"/list.m3u8", "", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Close()

	resp, err := http.Get(p.StreamURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	pl, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-mpegURL" {
		t.Fatalf("playlist content-type %q", ct)
	}
	// Follow the rewritten segment URL back through the proxy.
	var segURL string
	for _, line := range strings.Split(string(pl), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "http") {
			segURL = strings.TrimSpace(line)
		}
	}
	if segURL == "" {
		t.Fatalf("no rewritten segment in:\n%s", pl)
	}
	resp2, err := http.Get(segURL)
	if err != nil {
		t.Fatalf("segment get: %v", err)
	}
	seg, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if string(seg) != "SEGMENT" {
		t.Fatalf("segment %q", seg)
	}
	if p.Bytes() == 0 || p.Requests() != 2 {
		t.Fatalf("bytes=%d reqs=%d", p.Bytes(), p.Requests())
	}
}

func TestProxySendsCookiesAndDefaultUA(t *testing.T) {
	var gotUA, gotCookie string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA, gotCookie = r.UserAgent(), r.Header.Get("Cookie")
		if r.URL.Path == "/list.m3u8" {
			http.SetCookie(w, &http.Cookie{Name: "sess", Value: "abc", Path: "/"})
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n#EXTINF:10,\nseg.ts\n")
			return
		}
		w.Write([]byte("SEGMENT"))
	}))
	defer up.Close()

	// Empty UA must fall back to a real identifier, not a blank header.
	p, err := StartProxy(up.URL+"/list.m3u8", "", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Close()
	resp, err := http.Get(p.StreamURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if gotUA != "iptv-tui/1.0" {
		t.Fatalf("upstream UA = %q, want default identifier", gotUA)
	}
	// The session cookie from the manifest must ride along on segments.
	resp2, err := http.Get(p.BaseURL() + "/p?u=" + url.QueryEscape(up.URL+"/seg.ts"))
	if err != nil {
		t.Fatalf("segment get: %v", err)
	}
	io.Copy(io.Discard, resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(gotCookie, "sess=abc") {
		t.Fatalf("segment cookie = %q, want sess=abc", gotCookie)
	}
	// Every response a Cast receiver touches must carry CORS.
	if got := resp2.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("segment CORS = %q, want *", got)
	}
	// Range pulls preflight: OPTIONS must answer outright.
	pre, err := http.NewRequest(http.MethodOptions, p.StreamURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	pre.Header.Set("Origin", "https://www.gstatic.com")
	pre.Header.Set("Access-Control-Request-Method", "GET")
	resp3, err := http.DefaultClient.Do(pre)
	if err != nil {
		t.Fatalf("options: %v", err)
	}
	resp3.Body.Close()
	if resp3.StatusCode != http.StatusOK {
		t.Fatalf("preflight status = %d, want 200", resp3.StatusCode)
	}
	if got := resp3.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Range") {
		t.Fatalf("preflight must allow Range, got %q", got)
	}
}

func TestProxyForwardsUpstreamErrors(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		io.WriteString(w, "<html>denied</html>")
	}))
	defer up.Close()

	p, err := StartProxy(up.URL+"/list.m3u8", "", "", "127.0.0.1")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Close()
	resp, err := http.Get(p.StreamURL())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// An error page must not masquerade as a playlist: forward the status
	// so the receiver fails fast instead of retrying a fake manifest.
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.StatusCode)
	}
	if strings.Contains(string(body), "/p?u=") {
		t.Fatal("error body must not be rewritten as a playlist")
	}
}
