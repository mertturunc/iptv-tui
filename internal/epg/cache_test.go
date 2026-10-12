package epg

import (
	"errors"
	"testing"
	"time"

	"github.com/mertturunc/iptv-tui/internal/store"
)

func TestShortEpgCache(t *testing.T) {
	testCacheDir = t.TempDir()
	defer func() { testCacheDir = "" }()

	progs := []store.Program{
		{Start: time.Now().Add(-time.Hour), Stop: time.Now().Add(time.Hour), Title: "Now Showing"},
		{Start: time.Now().Add(time.Hour), Stop: time.Now().Add(2 * time.Hour), Title: "Next Up"},
	}

	// fresh write → fresh read
	if err := SaveShortEpg("src1", 42, progs, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadShortEpg("src1", 42, time.Hour)
	if err != nil {
		t.Fatalf("fresh load: %v", err)
	}
	if len(got) != 2 || got[0].Title != "Now Showing" {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	// stale (written an hour ago, maxAge 1m)
	SaveShortEpg("src1", 43, progs, time.Now().Add(-time.Hour))
	stale, err := LoadShortEpg("src1", 43, time.Minute)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("expected ErrStale, got %v", err)
	}
	if len(stale) != 2 {
		t.Fatalf("stale read should still return data, got %d", len(stale))
	}

	// no cache
	if _, err := LoadShortEpg("nope", 1, time.Hour); !errors.Is(err, ErrNoCache) {
		t.Fatalf("expected ErrNoCache, got %v", err)
	}
}

func TestIndexCache(t *testing.T) {
	testCacheDir = t.TempDir()
	defer func() { testCacheDir = "" }()

	idx := &store.ProgramIndex{
		ByID: map[string][]store.Program{
			"bbc1": {{Title: "A", Start: time.Now()}},
		},
		Sources: []string{"https://epg.example/x.xml.gz"},
	}
	if err := SaveIndexCache("m3u1", idx, time.Now()); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := LoadIndexCache("m3u1", 12*time.Hour)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Get("bbc1")[0].Title != "A" {
		t.Fatalf("index roundtrip: %+v", got.ByID)
	}

	// stale index still returns data
	SaveIndexCache("m3u2", idx, time.Now().Add(-24*time.Hour))
	_, err = LoadIndexCache("m3u2", time.Hour)
	if !errors.Is(err, ErrStale) {
		t.Fatalf("expected ErrStale, got %v", err)
	}
}
