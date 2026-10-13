package store

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// MediaType discriminates live/VOD/series content within one source.
type MediaType string

const (
	Live   MediaType = "live"
	Movie  MediaType = "movie"
	Series MediaType = "series"
)

// SourceKind names the playlist backend. It stays a string so persisted
// config.json keeps working unchanged.
type SourceKind string

const (
	SourceM3U    SourceKind = "m3u"
	SourceXtream SourceKind = "xtream"
)

// IsXtream reports whether the kind is an Xtream Codes portal.
func (k SourceKind) IsXtream() bool { return k == SourceXtream }

// IsM3U reports whether the kind is an M3U playlist (including empty for
// configs predating the field).
func (k SourceKind) IsM3U() bool { return k == SourceM3U || k == "" }

// Source is the persisted definition of an M3U or Xtream playlist.
type Source struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      SourceKind `json:"kind"` // "m3u" | "xtream"
	URL       string     `json:"url,omitempty"`
	Server    string     `json:"server,omitempty"`
	Username  string     `json:"username,omitempty"`
	Password  string     `json:"password,omitempty"`
	UserAgent string     `json:"userAgent,omitempty"`
	Referer   string     `json:"referer,omitempty"`
	EPGUrls   []string   `json:"epgUrls,omitempty"`
}

// IsXtream is a convenience for callers holding a Source.
func (s *Source) IsXtream() bool {
	if s == nil {
		return false
	}
	return s.Kind.IsXtream()
}

// M3UFields carries playlist-only metadata (tvg/catchup/radio).
type M3UFields struct {
	TvgID       string
	TvgName     string
	Radio       bool
	CatchupType string
	CatchupDays string
	Timeshift   string
	Lang        string
}

// XtreamFields carries Xtream-only detail (series identity, stream IDs).
type XtreamFields struct {
	// SeriesID/Season/Episode identify series detail.
	SeriesID int
	Season   int
	Episode  int

	// StreamID is the Xtream stream ID for EPG lookups.
	StreamID int
}

// Channel is a unified playable entry used by the UI regardless of backend.
// Backend-specific columns live in the embedded M3UFields/XtreamFields so
// M3U and Xtream changes land in different structs instead of colliding.
type Channel struct {
	Key        string // sourceID + "|" + mediatype + "|" + URL
	SourceID   string
	SourceName string
	Type       MediaType
	Number     int    // m3u listing order or xtream "num"
	Name       string // display name / movie title
	URL        string
	Logo       string
	Group      string
	UserAgent  string
	Referer    string

	M3UFields
	XtreamFields
}

func (c Channel) IsPositionTrackable() bool {
	// Live channels have no meaningful resume position.
	return c.Type != Live
}

type HistoryEntry struct {
	Position time.Duration `json:"posNs"`
	Duration time.Duration `json:"durNs,omitempty"`
	Updated  time.Time     `json:"updated"`
}

type Settings struct {
	MpvPath          string `json:"mpvPath,omitempty"`
	MpvArgs          string `json:"mpvArgs,omitempty"` // one per line custom mpv args
	DefaultUserAgent string `json:"defaultUserAgent,omitempty"`
	KeepMpvOnQuit    bool   `json:"keepMpvOnQuit,omitempty"`
	Theme            string `json:"theme,omitempty"`     // palette name, e.g. "Dracula"
	CastTV           string `json:"castTV,omitempty"`    // last Chromecast target host
	AirPlayTV        string `json:"airPlayTV,omitempty"` // last AirPlay target host
}

type Config struct {
	Version   int                     `json:"version"`
	Sources   []Source                `json:"sources"`
	Favorites map[string]bool         `json:"favorites"`
	History   map[string]HistoryEntry `json:"history"`
	Settings  Settings                `json:"settings"`
}

// Loaded keeps in-memory parsed content for one source.
type Loaded struct {
	Source  *Source
	LoadErr error

	// Stale is set by loaders when the content was served from an aged disk
	// snapshot; the UI uses it to schedule a background re-fetch. Never
	// persisted (snapshots are serialized separately).
	Stale bool `json:"-"`

	Episodes map[string][]Channel // seriesID -> episode channels (per Series)

	// Per media type: flattened channels and their groups, kept in order.
	Channels map[MediaType][]Channel
	Groups   map[MediaType][]string

	EPG *ProgramIndex // tvgID -> programs (nil if not loaded)

	Author   string // xtream account name if authenticated
	LoadedAt time.Time
}

type Program struct {
	Start   time.Time
	Stop    time.Time
	Channel string
	Title   string
	Desc    string
}

type ProgramIndex struct {
	mu      sync.Mutex
	ByID    map[string][]Program
	Err     error
	Sources []string
}

func (pi *ProgramIndex) Set(id string, progs []Program) {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	if pi.ByID == nil {
		pi.ByID = map[string][]Program{}
	}
	sort.SliceStable(progs, func(i, j int) bool { return progs[i].Start.Before(progs[j].Start) })
	pi.ByID[id] = progs
}

func (pi *ProgramIndex) Get(id string) []Program {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	return pi.ByID[id]
}

// Snapshot returns a shallow copy of the channel→programs map under lock.
func (pi *ProgramIndex) Snapshot() map[string][]Program {
	pi.mu.Lock()
	defer pi.mu.Unlock()
	out := make(map[string][]Program, len(pi.ByID))
	for k, v := range pi.ByID {
		out[k] = v
	}
	return out
}

func (l *Loaded) GroupTitles(t MediaType) []string {
	return l.Groups[t]
}

// ChannelKey returns the storage key for favorites and history.
func ChannelKey(srcID string, t MediaType, url string) string {
	return srcID + "|" + string(t) + "|" + url
}

// configPath returns <userConfigDir>/iptv-tui/config.json
func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "iptv-tui", "config.json"), nil
}

type Store struct {
	path string
	mu   sync.Mutex
	Cfg  Config
}

func DefaultConfig() Config {
	return Config{
		Version:   1,
		Favorites: map[string]bool{},
		History:   map[string]HistoryEntry{},
	}
}

func LoadConfig() (*Store, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	s := &Store{path: p, Cfg: DefaultConfig()}
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, &s.Cfg); err != nil {
		return nil, fmt.Errorf("corrupt config %s: %w", p, err)
	}
	if s.Cfg.Favorites == nil {
		s.Cfg.Favorites = map[string]bool{}
	}
	if s.Cfg.History == nil {
		s.Cfg.History = map[string]HistoryEntry{}
	}
	return s, nil
}

func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(&s.Cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *Store) ToggleFavorite(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := s.Cfg.Favorites[key]
	s.Cfg.Favorites[key] = !cur
	return !cur
}

func (s *Store) IsFavorite(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Favorites[key]
}

func (s *Store) RecordPosition(key string, pos, dur time.Duration) {
	s.mu.Lock()
	v := s.Cfg.History[key]
	v.Position = pos
	if dur > 0 {
		v.Duration = dur
	}
	v.Updated = time.Now()
	s.Cfg.History[key] = v
	s.mu.Unlock()
}

func (s *Store) GetHistory(key string) (HistoryEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Cfg.History[key]
	return v, ok
}

func (s *Store) AddSource(src Source) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cfg.Sources = append(s.Cfg.Sources, src)
}

func (s *Store) RemoveSource(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Fresh slice, not an in-place [:0] filter: callers may still hold the
	// old backing array (e.g. a ranged copy), which in-place filtering would
	// silently rewrite under them.
	out := make([]Source, 0, len(s.Cfg.Sources))
	for _, src := range s.Cfg.Sources {
		if src.ID != id {
			out = append(out, src)
		}
	}
	s.Cfg.Sources = out
	prefix := id + "|"
	for k := range s.Cfg.Favorites {
		if strings.HasPrefix(k, prefix) {
			delete(s.Cfg.Favorites, k)
		}
	}
	for k := range s.Cfg.History {
		if strings.HasPrefix(k, prefix) {
			delete(s.Cfg.History, k)
		}
	}
}

func (s *Store) FindSource(id string) *Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	// UI goroutine only: the returned pointer aliases the live slice, so it
	// must not escape to background goroutines (use FindSourceCopy there).
	for i := range s.Cfg.Sources {
		if s.Cfg.Sources[i].ID == id {
			return &s.Cfg.Sources[i]
		}
	}
	return nil
}

// FindSourceCopy returns a source by value for use off the UI goroutine
// (background loaders). The interior pointer from FindSource must not escape
// the UI goroutine: a later AddSource/RemoveSource may reallocate the slice.
func (s *Store) FindSourceCopy(id string) (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, src := range s.Cfg.Sources {
		if src.ID == id {
			return src, true
		}
	}
	return Source{}, false
}

// Sources returns a snapshot copy of the configured sources for the UI
// goroutine to range over without holding the lock or aliasing live state.
func (s *Store) Sources() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Source, len(s.Cfg.Sources))
	copy(out, s.Cfg.Sources)
	return out
}

// SourceAt returns the source at idx, or false when out of range.
func (s *Store) SourceAt(idx int) (Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < 0 || idx >= len(s.Cfg.Sources) {
		return Source{}, false
	}
	return s.Cfg.Sources[idx], true
}

// SourceName returns one source's display name by index.
func (s *Store) SourceName(idx int) (string, bool) {
	src, ok := s.SourceAt(idx)
	if !ok {
		return "", false
	}
	return src.Name, true
}

// Settings accessors. Callers go through these instead of reaching
// m.cfg.Cfg.Settings two levels deep (Message Chains): one place defines
// the fallback policy, and future persistence changes land here.
func (s *Store) MpvPath() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Settings.MpvPath
}

func (s *Store) SetMpvPath(p string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cfg.Settings.MpvPath = p
}

func (s *Store) MpvArgs() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Settings.MpvArgs
}

func (s *Store) DefaultUserAgent() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Settings.DefaultUserAgent
}

func (s *Store) KeepMpvOnQuit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Settings.KeepMpvOnQuit
}

func (s *Store) ToggleKeepMpvOnQuit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cfg.Settings.KeepMpvOnQuit = !s.Cfg.Settings.KeepMpvOnQuit
	return s.Cfg.Settings.KeepMpvOnQuit
}

func (s *Store) Theme() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Settings.Theme
}

func (s *Store) SetTheme(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cfg.Settings.Theme = name
}

func (s *Store) CastTV() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Settings.CastTV
}

func (s *Store) SetCastTV(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cfg.Settings.CastTV = addr
}

func (s *Store) AirPlayTV() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Cfg.Settings.AirPlayTV
}

func (s *Store) SetAirPlayTV(addr string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Cfg.Settings.AirPlayTV = addr
}

// RandomID returns a short random identifier for a new source.
func RandomID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("src-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%s-%d", hex.EncodeToString(b)[:8], time.Now().UnixNano()%100000)
}
