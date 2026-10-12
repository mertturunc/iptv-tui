// Package load fetches and normalizes M3U and Xtream sources into the unified
// channel tree the UI consumes (mirroring iptvnator's import/normalization).
package load

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mertturunc/iptv-tui/internal/epg"
	"github.com/mertturunc/iptv-tui/internal/m3u"
	"github.com/mertturunc/iptv-tui/internal/store"
	"github.com/mertturunc/iptv-tui/internal/xtream"
)

func fetchText(url, userAgent string) (string, error) {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}
		if userAgent == "" {
			userAgent = "iptv-tui/1.0"
		}
		req.Header.Set("User-Agent", userAgent)
		hc := &http.Client{Timeout: 60 * time.Second}
		resp, err := hc.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		b, err := io.ReadAll(resp.Body)
		return string(b), err
	}
	// Local file path.
	b, err := os.ReadFile(url)
	return string(b), err
}

func baseChannel(src *store.Source, t store.MediaType, number int, name, url string) store.Channel {
	return store.Channel{
		SourceID:   src.ID,
		SourceName: src.Name,
		Type:       t,
		Number:     number,
		Name:       name,
		URL:        url,
		Key:        store.ChannelKey(src.ID, t, url),
	}
}

func uniqueGroups(channels []store.Channel) []string {
	var groups []string
	seen := map[string]bool{}
	for _, c := range channels {
		g := c.Group
		if g == "" {
			g = "Ungrouped"
		}
		if !seen[g] {
			seen[g] = true
			groups = append(groups, g)
		}
	}
	return groups
}

// LoadM3U builds a Loaded from an M3U source. The channel list is returned
// immediately; XMLTV EPG is served from the disk cache when fresh and never
// blocks source load — unresolved XMLTV URLs are returned to the caller so it
// can refresh the EPG index in the background.
func LoadM3U(src *store.Source) (*store.Loaded, []string) {
	// Serve the whole-source snapshot instantly when present; the caller
	// re-fetches in the background when it's aged (Stale). Snapshots only hold
	// the lists, so EPG still comes from its own disk cache below.
	if ld, err := loadSnapshot(src, SourceSnapshotTTL); err == nil || errors.Is(err, ErrCacheStale) {
		ld.Stale = errors.Is(err, ErrCacheStale)
		if idx, ierr := epg.LoadIndexCache(src.ID, 12*time.Hour); ierr == nil && len(idx.ByID) > 0 {
			ld.EPG = idx
		}
		return ld, nil
	}
	return loadM3UNet(src)
}

// RefreshM3U re-fetches a source over the network, always bypassing the whole
// snapshot, and persists the new snapshot. Used for the background update after
// a stale snapshot serve.
func RefreshM3U(src *store.Source) (*store.Loaded, []string) {
	return loadM3UNet(src)
}

func loadM3UNet(src *store.Source) (*store.Loaded, []string) {
	ld := &store.Loaded{
		Source:   src,
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	content, err := fetchText(src.URL, src.UserAgent)
	if err != nil {
		ld.LoadErr = err
		return ld, nil
	}
	pl, err := m3u.Parse(content)
	if err != nil {
		ld.LoadErr = err
		return ld, nil
	}

	for i := range pl.Items {
		it := &pl.Items[i]
		if it.URL == "" {
			continue
		}
		group := it.GroupTitle
		if group == "" {
			group = "Ungrouped"
		}
		ch := baseChannel(src, store.Live, i+1, it.Name, it.URL)
		ch.Logo = it.Tvg.Logo
		ch.Group = group
		ch.TvgID = it.Tvg.ID
		ch.TvgName = it.Tvg.Name
		ch.UserAgent = it.HTTP.UserAgent
		ch.Referer = it.HTTP.Referrer
		ch.Radio = it.Radio
		ch.CatchupType = it.Catchup.Type
		ch.CatchupDays = it.Catchup.Days
		ch.Timeshift = it.Timeshift
		ch.Lang = it.Lang
		if ch.CatchupType == "" && it.Catchup.Source != "" && it.Catchup.Days != "" {
			ch.CatchupType = "m3u"
		}
		ld.Channels[store.Live] = append(ld.Channels[store.Live], ch)
	}
	ld.Groups[store.Live] = uniqueGroups(ld.Channels[store.Live])

	// EPG urls: from header extraction, then source-level overrides.
	urls := m3u.ExtractEpgUrls(pl)
	if len(src.EPGUrls) > 0 {
		urls = append(urls, src.EPGUrls...)
	}
	// Serve cached XMLTV instantly; refresh happens in the background by the
	// caller, so a cold start still shows channels right away.
	if len(urls) > 0 {
		if idx, err := epg.LoadIndexCache(src.ID, 12*time.Hour); err == nil {
			ld.EPG = idx
		}
	}
	ld.LoadedAt = time.Now()
	if ld.LoadErr == nil {
		cacheSnapshot(src, ld)
	}
	return ld, urls
}

// LoadXtream builds a Loaded for a source by authorizing and pulling all
// categories/streams. Series episodes are expanded lazily into Episodes.
// RefreshXtream re-fetches a source over the network, always bypassing the
// whole list snapshot, and persists the new snapshot. Used for the
// background update after a stale snapshot serve.
func RefreshXtream(src *store.Source) *store.Loaded {
	return loadXtreamNet(src)
}

func LoadXtream(src *store.Source) *store.Loaded {
	if ld, err := loadSnapshot(src, SourceSnapshotTTL); err == nil || errors.Is(err, ErrCacheStale) {
		ld.Stale = errors.Is(err, ErrCacheStale)
		return ld
	}
	return loadXtreamNet(src)
}

func loadXtreamNet(src *store.Source) *store.Loaded {
	ld := &store.Loaded{
		Source:   src,
		Channels: map[store.MediaType][]store.Channel{},
		Groups:   map[store.MediaType][]string{},
		Episodes: map[string][]store.Channel{},
		EPG:      &store.ProgramIndex{ByID: map[string][]store.Program{}},
	}
	cl := xtream.New(src.Server, src.Username, src.Password)

	auth, err := cl.Authenticate()
	if err != nil {
		ld.LoadErr = fmt.Errorf("xtream auth failed: %w", err)
		return ld
	}
	if auth.UserInfo.Auth != 1 {
		ld.LoadErr = fmt.Errorf("xtream auth rejected: %s (%s)", auth.UserInfo.Status, auth.UserInfo.Username)
		return ld
	}
	ld.Author = auth.UserInfo.Username

	cats, err := cl.LiveCategories()
	if err != nil {
		ld.LoadErr = fmt.Errorf("live categories: %w", err)
		return ld
	}
	catName := map[string]string{}
	for _, c := range cats {
		catName[c.CategoryID] = c.CategoryName
	}
	streams, err := cl.LiveStreams()
	if err != nil {
		ld.LoadErr = fmt.Errorf("live streams: %w", err)
		return ld
	}
	for _, s := range streams {
		ch := baseChannel(src, store.Live, int(s.Num), s.Name, cl.LiveURL(s))
		ch.Logo = s.StreamIcon
		ch.Group = catName[s.CategoryID]
		ch.TvgID = s.EpgChannelID
		ch.StreamID = int(s.StreamID)
		if ch.Group == "" {
			ch.Group = "All"
		}
		if s.TVArchive == 1 {
			ch.CatchupType = "archive"
			ch.CatchupDays = fmt.Sprintf("%d", s.TVArchiveDuration)
		}
		ld.Channels[store.Live] = append(ld.Channels[store.Live], ch)
	}
	ld.Groups[store.Live] = uniqueGroups(ld.Channels[store.Live])

	// VOD
	vcats, err := cl.VodCategories()
	if err == nil {
		catName = map[string]string{}
		for _, c := range vcats {
			catName[c.CategoryID] = c.CategoryName
		}
		vod, err := cl.VodStreams()
		if err == nil {
			for _, s := range vod {
				ch := baseChannel(src, store.Movie, int(s.Num), s.Name, cl.VODURL(s))
				ch.Logo = s.StreamIcon
				ch.Group = catName[s.CategoryID]
				ch.StreamID = int(s.StreamID)
				if ch.Group == "" {
					ch.Group = "All"
				}
				ld.Channels[store.Movie] = append(ld.Channels[store.Movie], ch)
			}
			ld.Groups[store.Movie] = uniqueGroups(ld.Channels[store.Movie])
		}
	}

	// Series
	scats, err := cl.SeriesCategories()
	if err == nil {
		catName = map[string]string{}
		for _, c := range scats {
			catName[c.CategoryID] = c.CategoryName
		}
		series, err := cl.SeriesList()
		if err == nil {
			for _, s := range series {
				ch := baseChannel(src, store.Series, int(s.Num), s.Name, "")
				ch.Logo = s.Cover
				ch.Group = catName[s.CategoryID]
				ch.SeriesID = int(s.SeriesID)
				if ch.Group == "" {
					ch.Group = "All"
				}
				// Series containers have no direct URL; key on the series ID
				// so favorites/history stay stable per series.
				ch.URL = ""
				ch.Key = store.ChannelKey(src.ID, store.Series, fmt.Sprintf("series:%d", s.SeriesID))
				ld.Channels[store.Series] = append(ld.Channels[store.Series], ch)
			}
			ld.Groups[store.Series] = uniqueGroups(ld.Channels[store.Series])
		}
	}

	ld.LoadedAt = time.Now()
	if ld.LoadErr == nil {
		cacheSnapshot(src, ld)
	}
	return ld
}

// EpgForStream fetches the short EPG for a single Xtream live stream and maps
// it into store.Program entries.
func EpgForStream(src *store.Source, streamID int) ([]store.Program, error) {
	cl := xtream.New(src.Server, src.Username, src.Password)
	items, err := cl.EPG(streamID)
	if err != nil {
		return nil, err
	}
	out := make([]store.Program, 0, len(items))
	for _, it := range items {
		out = append(out, store.Program{
			Start:   it.StartTime,
			Stop:    it.EndTime,
			Channel: src.Name,
			Title:   it.Title,
			Desc:    it.Description,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// ExpandSeries lazily loads and caches episodes for a series channel. Returns
// the mapped episode channels. Safe for concurrent use; guard by caller.
func ExpandSeries(ld *store.Loaded, src *store.Source, seriesID int, cache *sync.Map) []store.Channel {
	key := fmt.Sprintf("%s|%d", src.ID, seriesID)
	if cached, ok := cache.Load(key); ok {
		return cached.([]store.Channel)
	}
	defaultVal := []store.Channel{}
	cache.Store(key, defaultVal) // avoid duplicate fetches

	cl := xtream.New(src.Server, src.Username, src.Password)
	info, err := cl.SeriesInfo(seriesID)
	if err != nil {
		return nil
	}
	out := mapEpisodes(src, seriesID, info)
	cache.Store(key, out)
	return out
}

// mapEpisodes turns a series_info payload into episode channels. Pure (no
// network) so provider shape quirks stay unit-testable.
func mapEpisodes(src *store.Source, seriesID int, info xtream.SeriesInfo) []store.Channel {
	cl := xtream.New(src.Server, src.Username, src.Password)
	var out []store.Channel
	for seasonKey := range info.Episodes {
		seasonNum, _ := strconv.Atoi(seasonKey)
		for _, ep := range info.Episodes[seasonKey] {
			season := fmt.Sprintf("Season %d", seasonNum)
			// Providers disagree on where the episode name lives: most put
			// it in info.name, some only in the top-level title.
			title := ep.Title
			if title == "" {
				title = ep.Info.Name
			}
			if title == "" {
				title = fmt.Sprintf("Episode %d", ep.EpisodeNum)
			}
			ch := baseChannel(src, store.Series, 0,
				fmt.Sprintf("S%02d E%02d · %s", seasonNum, ep.EpisodeNum, title),
				cl.EpisodeURL(ep))
			ch.Group = season
			ch.SeriesID = seriesID
			ch.Season = seasonNum
			ch.Episode = int(ep.EpisodeNum)
			out = append(out, ch)
		}
	}
	// Map iteration is random; keep seasons and episodes in watch order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
	return out
}
