package m3u

import (
	"strings"
	"testing"
)

// Mixed fixture covering the attribute shapes seen across free playlists:
// header EPG urls, pipe params, EXTVLCOPT overrides, EXTGRP, KODIPROP,
// catchup/timeshift, and radio items.
const mixedFixture = `#EXTM3U x-tvg-url="https://example.com/guide.xml"
#EXTINF:-1 tvg-id="bbc1" tvg-name="BBC One" tvg-logo="https://example.com/bbc.png" group-title="News",BBC One
https://example.com/bbc.m3u8
#EXTINF:-1 tvg-id="espn" group-title="Sports" user-agent="AttrUA",ESPN
#EXTVLCOPT:http-user-agent=VLCUA
https://example.com/espn.m3u8|User-Agent=PipeUA&Referer=https://ref.example/
#EXTINF:-1 group-title="Old" catchup="shift" catchup-days="7" timeshift="3600" lang="en",Catchup TV
#EXTGRP:Movies
https://example.com/catchup.m3u8
#KODIPROP:inputstream.adaptive.license_type=com.widevine.alpha
#EXTINF:-1 tvg-id="radio1" radio="true" group-title="Radio",Cool FM
https://example.com/cool.aac
`

func TestParseMixed(t *testing.T) {
	pl, err := Parse(mixedFixture)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pl.Items) != 4 {
		t.Fatalf("want 4 items, got %d", len(pl.Items))
	}
	bbc := pl.Items[0]
	if bbc.Tvg.ID != "bbc1" || bbc.GroupTitle != "News" || bbc.Name != "BBC One" {
		t.Fatalf("plain item: %+v", bbc)
	}
	espn := pl.Items[1]
	if espn.URL != "https://example.com/espn.m3u8" {
		t.Fatalf("pipe url not stripped: %q", espn.URL)
	}
	if espn.HTTP.UserAgent != "PipeUA" {
		t.Fatalf("pipe UA should win over attr+vlcopt, got %q", espn.HTTP.UserAgent)
	}
	if espn.HTTP.Referrer != "https://ref.example/" {
		t.Fatalf("pipe referer: %q", espn.HTTP.Referrer)
	}
	cu := pl.Items[2]
	if cu.GroupTitle != "Movies" {
		t.Fatalf("EXTGRP should override group-title, got %q", cu.GroupTitle)
	}
	if cu.Catchup.Type != "shift" || cu.Catchup.Days != "7" || cu.Timeshift != "3600" || cu.Lang != "en" {
		t.Fatalf("catchup fields: %+v", cu.Catchup)
	}
	fm := pl.Items[3]
	if !fm.Radio || fm.Tvg.ID != "radio1" {
		t.Fatalf("radio item: %+v", fm)
	}
	if !strings.Contains(fm.Raw, "KODIPROP") {
		t.Fatalf("kodiprop should attach to raw block: %q", fm.Raw)
	}
}

func TestExtractEpgUrls(t *testing.T) {
	pl, err := Parse(mixedFixture)
	if err != nil {
		t.Fatal(err)
	}
	urls := ExtractEpgUrls(pl)
	if len(urls) != 1 || urls[0] != "https://example.com/guide.xml" {
		t.Fatalf("epg urls: %v", urls)
	}
}

func TestParseRejectsNonPlaylist(t *testing.T) {
	if _, err := Parse("just some text\nhttp://example.com/x.m3u8\n"); err == nil {
		t.Fatal("expected an error for missing #EXTM3U")
	}
}
