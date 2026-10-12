package load

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/mertturunc/iptv-tui/internal/store"
)

// SourceSnapshotTTL is how long a whole-source snapshot is considered fresh.
// Older snapshots are still served instantly from disk (offline-ready) but
// flagged Stale so the UI refreshes the source in the background.
const SourceSnapshotTTL = 6 * time.Hour

var (
	// ErrCacheMissing means no usable snapshot exists (never fetched, or the
	// source definition changed and invalidated it).
	ErrCacheMissing = errors.New("source snapshot missing")
	// ErrCacheStale means a snapshot exists but is older than SourceSnapshotTTL;
	// the data is still returned so the UI can start offline, alongside ErrCacheStale.
	ErrCacheStale = errors.New("source snapshot stale")
)

// testCacheDir redirects snapshots away from the real user config dir in tests.
var testCacheDir string

// snapshot is the disk-serialized form of a fully-loaded source. EPG programs
// are intentionally excluded — they're cached separately by the EPG subsystem
// (index cache + per-channel short cache) because they age much faster.
type snapshot struct {
	FetchedAt time.Time                           `json:"fetchedAt"`
	Source    store.Source                        `json:"source"`
	Author    string                              `json:"author,omitempty"`
	Channels  map[store.MediaType][]store.Channel `json:"channels"`
	Groups    map[store.MediaType][]string        `json:"groups"`
	Episodes  map[string][]store.Channel          `json:"episodes,omitempty"`
}

func cacheDir() (string, error) {
	d := testCacheDir
	if d == "" {
		var err error
		d, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(d, "iptv-tui", "cache")
	}
	return d, os.MkdirAll(d, 0o755)
}

func snapPath(srcID string) (string, error) {
	d, err := cacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, srcID+".json.gz"), nil
}

// cacheSnapshot persists the whole parsed source to disk so a later run starts
// instantly and works offline. Best-effort: failures are swallowed by callers.
func cacheSnapshot(src *store.Source, ld *store.Loaded) error {
	p, err := snapPath(src.ID)
	if err != nil {
		return err
	}
	s := snapshot{
		FetchedAt: time.Now(),
		Source:    *src,
		Author:    ld.Author,
		Channels:  ld.Channels,
		Groups:    ld.Groups,
		Episodes:  ld.Episodes,
	}
	if s.Channels == nil {
		s.Channels = map[store.MediaType][]store.Channel{}
	}
	if s.Groups == nil {
		s.Groups = map[store.MediaType][]string{}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(raw); err != nil {
		gz.Close()
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// loadSnapshot reads the snapshot for src when present and still matching the
// current source definition. It returns the loaded snapshot plus nil when
// fresh, ErrCacheStale when aged but usable, or ErrCacheMissing when absent.
func loadSnapshot(src *store.Source, ttl time.Duration) (*store.Loaded, error) {
	p, err := snapPath(src.ID)
	if err != nil {
		return nil, ErrCacheMissing
	}
	gz, err := os.Open(p)
	if err != nil {
		return nil, ErrCacheMissing
	}
	defer gz.Close()
	zr, err := gzip.NewReader(gz)
	if err != nil {
		return nil, ErrCacheMissing
	}
	defer zr.Close()
	var s snapshot
	if err := json.NewDecoder(zr).Decode(&s); err != nil {
		return nil, ErrCacheMissing
	}
	if !snapshotMatches(src, &s) {
		return nil, ErrCacheMissing
	}
	ld := snapshotToLoaded(src, &s)
	if s.FetchedAt.Before(time.Now().Add(-ttl)) {
		return ld, ErrCacheStale
	}
	return ld, nil
}

// snapshotToLoaded rebuilds a Loaded from a snapshot using the caller's current
// Source for identity. EPG starts empty for the caller (xtream) or the EPG
// loader (m3u) to fill from its own cache.
func snapshotToLoaded(src *store.Source, s *snapshot) *store.Loaded {
	ld := &store.Loaded{
		Source:   src,
		Author:   s.Author,
		Channels: s.Channels,
		Groups:   s.Groups,
		Episodes: s.Episodes,
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
		LoadErr:  nil,
	}
	if ld.Channels == nil {
		ld.Channels = map[store.MediaType][]store.Channel{}
	}
	if ld.Groups == nil {
		ld.Groups = map[store.MediaType][]string{}
	}
	if ld.Episodes == nil {
		ld.Episodes = map[string][]store.Channel{}
	}
	return ld
}

func snapshotMatches(src *store.Source, s *snapshot) bool {
	if src == nil || src.ID == "" || src.Kind == "" {
		return false
	}
	if src.ID != s.Source.ID || src.Kind != s.Source.Kind {
		return false
	}
	if src.Kind.IsXtream() {
		return src.Server == s.Source.Server &&
			src.Username == s.Source.Username &&
			src.Password == s.Source.Password
	}
	// "m3u" and legacy empty kinds.
	return src.URL == s.Source.URL &&
		src.UserAgent == s.Source.UserAgent
}
