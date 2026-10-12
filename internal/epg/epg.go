// Package epg loads and queries XMLTV EPG data. It supports both uncompressed
// XML and gzip-encoded feeds (common with iptv-org). Parsed programmes are
// indexed by channel ID for fast now/next lookups.
package epg

import (
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mertturunc/iptv-tui/internal/store"
)

type xmltvDoc struct {
	Channels []xmltvChannel `xml:"channel"`
	Programs []xmltvProgram `xml:"programme"`
}

type xmltvChannel struct {
	ID   string `xml:"id,attr"`
	Name []struct {
		Lang string `xml:"lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"display-name"`
}

type xmltvProgram struct {
	Start   string `xml:"start,attr"`
	Stop    string `xml:"stop,attr"`
	Channel string `xml:"channel,attr"`
	Title   []struct {
		Lang string `xml:"lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"title"`
	Desc []struct {
		Lang string `xml:"lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"desc"`
}

// FromXMLTV parses raw XMLTV bytes and returns an index.
func FromXMLTV(data []byte) (*store.ProgramIndex, error) {
	doc := xmltvDoc{}
	if err := xml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	idx := &store.ProgramIndex{ByID: map[string][]store.Program{}}
	for _, p := range doc.Programs {
		start := parseXMLTVTime(p.Start)
		stop := parseXMLTVTime(p.Stop)
		prog := store.Program{
			Start:   start,
			Stop:    stop,
			Channel: p.Channel,
		}
		if len(p.Title) > 0 {
			prog.Title = p.Title[0].Text
		}
		if len(p.Desc) > 0 {
			prog.Desc = p.Desc[0].Text
		}
		idx.ByID[p.Channel] = append(idx.ByID[p.Channel], prog)
	}
	for id := range idx.ByID {
		sort.SliceStable(idx.ByID[id], func(i, j int) bool {
			return idx.ByID[id][i].Start.Before(idx.ByID[id][j].Start)
		})
	}
	return idx, nil
}

func parseXMLTVTime(s string) time.Time {
	s = strings.TrimSpace(s)
	formats := []string{
		"20060102150405 -0700",
		"20060102150405 +0000",
		"20060102150405",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

// FetchAndParse downloads one or more XMLTV URLs, merges them, and returns a
// merged ProgramIndex.
func FetchAndParse(urls []string, userAgent string) (*store.ProgramIndex, error) {
	hc := &http.Client{Timeout: 30 * time.Second}
	if userAgent == "" {
		userAgent = "iptv-tui/1.0"
	}
	merged := &store.ProgramIndex{ByID: map[string][]store.Program{}}
	for _, u := range urls {
		idx, err := fetchOne(hc, u, userAgent)
		if err != nil {
			merged.Err = err
			continue
		}
		for id, progs := range idx.ByID {
			merged.ByID[id] = append(merged.ByID[id], progs...)
		}
		merged.Sources = append(merged.Sources, u)
	}
	// Sort each channel.
	for id := range merged.ByID {
		sort.SliceStable(merged.ByID[id], func(i, j int) bool {
			return merged.ByID[id][i].Start.Before(merged.ByID[id][j].Start)
		})
	}
	return merged, nil
}

func fetchOne(hc *http.Client, url, ua string) (*store.ProgramIndex, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ua)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var r io.Reader = resp.Body
	if strings.HasSuffix(url, ".gz") || strings.HasSuffix(url, ".gzip") || resp.Header.Get("Content-Type") == "application/gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip open: %w", err)
		}
		defer gz.Close()
		r = gz
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return FromXMLTV(data)
}

// NowNext returns the current and next programmes for a channel.
func NowNext(idx *store.ProgramIndex, channelID string) (now, next *store.Program) {
	if idx == nil {
		return nil, nil
	}
	progs := idx.Get(channelID)
	ts := time.Now()
	var cur, nxt *store.Program
	for i := range progs {
		if progs[i].Start.Before(ts) && progs[i].Stop.After(ts) {
			cur = &progs[i]
			if i+1 < len(progs) {
				nxt = &progs[i+1]
			}
			break
		}
	}
	// If no current, find first upcoming.
	if cur == nil {
		for i := range progs {
			if progs[i].Start.After(ts) {
				nxt = &progs[i]
				break
			}
		}
	}
	return cur, nxt
}

// GuideFor returns upcoming programs starting from a reference time.
func GuideFor(idx *store.ProgramIndex, channelID string, from time.Time, limit int) []store.Program {
	if idx == nil || limit <= 0 {
		return nil
	}
	progs := idx.Get(channelID)
	var out []store.Program
	for _, p := range progs {
		if p.Stop.Before(from) {
			continue
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out
}
