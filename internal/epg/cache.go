package epg

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/mertturunc/iptv-tui/internal/store"
)

var (
	ErrStale   = errors.New("epg cache stale")
	ErrNoCache = errors.New("epg cache missing")
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// testCacheDir overrides the cache location; used by tests to avoid the
// real user config directory.
var testCacheDir string

func ensureCacheDir() (string, error) {
	d := testCacheDir
	if d == "" {
		var err error
		d, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
		d = filepath.Join(d, "iptv-tui", "epg")
	}
	return d, os.MkdirAll(d, 0o755)
}

func writeGzippedAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write(data); err != nil {
		gz.Close()
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := gz.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func readGzippedFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	return io.ReadAll(gz)
}

// loadCacheEntry reads a gzipped JSON cache entry and reports freshness.
// It returns (entry, stale, err) where err is nil (fresh), ErrStale, or ErrNoCache.
func loadCacheEntry[T any](path string, maxAge time.Duration) (T, bool, error) {
	var zero T
	raw, err := readGzippedFile(path)
	if err != nil {
		return zero, false, ErrNoCache
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return zero, false, ErrNoCache
	}
	var stamp struct {
		FetchedAt time.Time `json:"fetchedAt"`
	}
	if err := json.Unmarshal(raw, &stamp); err != nil {
		return zero, false, ErrNoCache
	}
	if maxAge > 0 && time.Since(stamp.FetchedAt) > maxAge {
		return v, true, ErrStale
	}
	return v, false, nil
}

// ---------------------------------------------------------------------------
// Per-channel short EPG (Xtream)
// ---------------------------------------------------------------------------

type shortEntry struct {
	FetchedAt time.Time       `json:"fetchedAt"`
	Progs     []store.Program `json:"progs"`
}

func shortPath(srcID string, streamID int) (string, error) {
	dir, err := ensureCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%s-%d.json.gz", srcID, streamID)), nil
}

// SaveShortEpg persists the per-channel listing. Best-effort; callers may
// ignore the error without consequence beyond a missed cache hit next time.
func SaveShortEpg(srcID string, streamID int, progs []store.Program, fetchedAt time.Time) error {
	p, err := shortPath(srcID, streamID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(shortEntry{FetchedAt: fetchedAt, Progs: progs})
	if err != nil {
		return err
	}
	return writeGzippedAtomic(p, raw)
}

// LoadShortEpg returns cached progs (even when stale) together with an error
// indicating freshness: nil = fresh, ErrStale = stale but returned,
// ErrNoCache = nothing on disk.
func LoadShortEpg(srcID string, streamID int, maxAge time.Duration) ([]store.Program, error) {
	p, err := shortPath(srcID, streamID)
	if err != nil {
		return nil, ErrNoCache
	}
	e, _, err := loadCacheEntry[shortEntry](p, maxAge)
	if err != nil {
		if errors.Is(err, ErrStale) {
			return e.Progs, ErrStale
		}
		return nil, ErrNoCache
	}
	return e.Progs, nil
}

// ---------------------------------------------------------------------------
// Source-level XMLTV index cache (M3U)
// ---------------------------------------------------------------------------

type indexEntry struct {
	FetchedAt time.Time                  `json:"fetchedAt"`
	ByID      map[string][]store.Program `json:"byID"`
	Sources   []string                   `json:"sources,omitempty"`
}

func indexPath(srcID string) (string, error) {
	dir, err := ensureCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, srcID+".xmltv.json.gz"), nil
}

// SaveIndexCache writes the parsed XMLTV index as gzipped JSON. Best-effort.
func SaveIndexCache(srcID string, idx *store.ProgramIndex, fetchedAt time.Time) error {
	p, err := indexPath(srcID)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(indexEntry{
		FetchedAt: fetchedAt,
		ByID:      idx.Snapshot(),
		Sources:   idx.Sources,
	})
	if err != nil {
		return err
	}
	return writeGzippedAtomic(p, raw)
}

// LoadIndexCache reads a cached XMLTV index. Returns the index (possibly
// stale) together with freshness status: nil = fresh, ErrStale, ErrNoCache.
func LoadIndexCache(srcID string, maxAge time.Duration) (*store.ProgramIndex, error) {
	p, err := indexPath(srcID)
	if err != nil {
		return nil, ErrNoCache
	}
	e, stale, err := loadCacheEntry[indexEntry](p, maxAge)
	if err != nil {
		if errors.Is(err, ErrStale) {
			return &store.ProgramIndex{ByID: e.ByID, Sources: e.Sources}, ErrStale
		}
		return nil, ErrNoCache
	}
	_ = stale
	idx := &store.ProgramIndex{ByID: e.ByID, Sources: e.Sources}
	return idx, nil
}
