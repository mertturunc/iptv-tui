// Package m3u ports the parsing semantics of iptvnator's
// iptv-playlist-parser fork (v0.15.2 deltas: radio attr, pipe stripping,
// |User-Agent=/|Referer= params) as faithfully as practicable.
package m3u

import (
	"errors"
	"regexp"
	"strings"
)

var (
	attrScan        = regexp.MustCompile(`([A-Za-z0-9-]+)="([^"]*)"`)
	nameClean       = regexp.MustCompile(`="(.*?)"`)
	vlcReferrerOpt  = regexp.MustCompile(`:http-referrer=(.*)`)
	vlcUserAgentOpt = regexp.MustCompile(`:http-user-agent=(.*)`)
	extgrpValue     = regexp.MustCompile(`:(.*)`)
	paramUA         = regexp.MustCompile(`(?i)user-agent=(\w[^&]*)`)
	paramRef        = regexp.MustCompile(`(?i)referer=(\w[^&]*)`)
)

// wanted attribute names (lowercase), fixed distribution order. Mirrors the
// JS fork's WANTED slice incluing index layout.
var wanted = []string{
	"tvg-id", "tvg-name", "tvg-logo", "tvg-url", "tvg-rec", "tvg-shift",
	"group-title", "referrer", "user-agent", "catchup", "catchup-days",
	"catchup-source", "timeshift", "lang", "radio",
}

// extinfAttrs is the named form of the fixed distribution order above.
// Field order matches wanted so call sites read names, not f[7].
type extinfAttrs struct {
	TvgID, TvgName, Logo, TvgURL, Rec, Shift string
	Group, Referrer, UserAgent               string
	Catchup, CatchupDays, CatchupSource      string
	Timeshift, Lang                          string
	Radio                                    bool
}

type Item struct {
	Name       string
	Tvg        struct{ ID, Name, Logo, URL, Rec, Shift string }
	GroupTitle string
	HTTP       struct{ Referrer, UserAgent string }
	URL        string
	Raw        string
	Line       int
	Catchup    struct{ Type, Days, Source string }
	Timeshift  string
	Lang       string
	Radio      bool
}

type Playlist struct {
	Header struct {
		Raw   string
		Attrs map[string]string
	}
	Items []Item
}

func scanAttrs(line string) [][2]string {
	out := make([][2]string, 0, 8)
	for _, m := range attrScan.FindAllStringSubmatch(line, -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

// distribute assigns scanned attrs to wanted fields: first attribute (in
// position order) whose lowercased name ends with the field name wins.
func distribute(attrs [][2]string) extinfAttrs {
	var fields [15]string
	taken := [15]bool{}
	for _, a := range attrs {
		lower := strings.ToLower(a[0])
		for idx := 0; idx < len(wanted); idx++ {
			if taken[idx] {
				continue
			}
			if strings.HasSuffix(lower, wanted[idx]) {
				taken[idx] = true
				fields[idx] = a[1]
			}
		}
	}
	return extinfAttrs{
		TvgID: fields[0], TvgName: fields[1], Logo: fields[2], TvgURL: fields[3],
		Rec: fields[4], Shift: fields[5], Group: fields[6],
		Referrer: fields[7], UserAgent: fields[8],
		Catchup: fields[9], CatchupDays: fields[10], CatchupSource: fields[11],
		Timeshift: fields[12], Lang: fields[13], Radio: fields[14] != "",
	}
}

// displayName strips "key=\"value\"" tokens then returns the remainder after
// the first remaining comma (the display name).
func displayName(line string) string {
	cleaned := nameClean.ReplaceAllString(line, "")
	i := strings.IndexByte(cleaned, ',')
	if i == -1 {
		return ""
	}
	return cleaned[i+1:]
}

func vlcOption(line string, re *regexp.Regexp) string {
	m := re.FindStringSubmatch(line)
	if m == nil || m[1] == "" {
		return ""
	}
	return strings.ReplaceAll(m[1], `"`, "")
}

func parseHeader(firstLine string) (raw string, attrs map[string]string) {
	attrs = map[string]string{}
	for _, a := range scanAttrs(firstLine) {
		lower := strings.ToLower(a[0])
		for _, name := range []string{"x-tvg-url", "url-tvg"} {
			if strings.HasSuffix(lower, name) {
				if _, exists := attrs[name]; !exists && a[1] != "" {
					attrs[name] = a[1]
				}
			}
		}
	}
	return firstLine, attrs
}

// Parse parses M3U/M3U8 playlist content.
func Parse(content string) (Playlist, error) {
	if strings.HasPrefix(content, "\ufeff") {
		content = content[1:]
	}
	lines := strings.Split(content, "\r\n")
	if len(lines) == 1 {
		lines = strings.Split(content, "\n")
	}

	hIdx := 0
	for hIdx < len(lines) && strings.TrimSpace(lines[hIdx]) == "" {
		hIdx++
	}
	if hIdx >= len(lines) || !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(lines[hIdx])), "#EXTM3U") {
		return Playlist{}, errors.New("Playlist is not valid")
	}

	var pl Playlist
	pl.Header.Raw, pl.Header.Attrs = parseHeader(strings.TrimSpace(lines[hIdx]))

	var cur *Item
	var rawLines []string
	var pendingKodiprops []string

	flushRaw := func() {
		if rawLines != nil && cur != nil {
			cur.Raw = strings.Join(rawLines, "\r\n")
		}
	}

	for idx := hIdx + 1; idx < len(lines); idx++ {
		raw := lines[idx]
		line := strings.TrimSpace(raw)
		isComment := line != "" && line[0] == '#'

		if isComment {
			if strings.HasPrefix(line, "#EXTINF:") {
				f := distribute(scanAttrs(line))
				cur = &Item{}
				cur.Name = displayName(line)
				cur.Tvg.ID = f.TvgID
				cur.Tvg.Name = f.TvgName
				cur.Tvg.Logo = f.Logo
				cur.Tvg.URL = f.TvgURL
				cur.Tvg.Rec = f.Rec
				cur.Tvg.Shift = f.Shift
				cur.GroupTitle = f.Group
				cur.HTTP.Referrer = f.Referrer
				cur.HTTP.UserAgent = f.UserAgent
				cur.Catchup.Type = f.Catchup
				cur.Catchup.Days = f.CatchupDays
				cur.Catchup.Source = f.CatchupSource
				cur.Timeshift = f.Timeshift
				cur.Lang = f.Lang
				cur.Radio = f.Radio
				cur.Line = idx + 1
				rawLines = nil
				if len(pendingKodiprops) > 0 {
					rawLines = append([]string{}, pendingKodiprops...)
					rawLines = append(rawLines, raw)
					cur.Raw = strings.Join(rawLines, "\r\n")
					pendingKodiprops = nil
				}
				continue
			}

			if cur == nil {
				if len(line) > 10 && strings.HasPrefix(strings.ToUpper(line), "#KODIPROP:") {
					pendingKodiprops = append(pendingKodiprops, raw)
				}
				continue
			}

			if strings.HasPrefix(line, "#EXTVLCOPT:") {
				if v := vlcOption(line, vlcReferrerOpt); v != "" {
					cur.HTTP.Referrer = v
				}
				if v := vlcOption(line, vlcUserAgentOpt); v != "" {
					cur.HTTP.UserAgent = v
				}
			} else if strings.HasPrefix(line, "#EXTGRP:") {
				m := extgrpValue.FindStringSubmatch(line)
				v := ""
				if m != nil && m[1] != "" {
					v = strings.ReplaceAll(m[1], `"`, "")
				}
				if v != "" {
					cur.GroupTitle = v
				}
			}

			if rawLines == nil {
				rawLines = []string{cur.Raw}
			}
			rawLines = append(rawLines, raw)
			continue
		}

		if cur == nil {
			continue
		}

		pipeIdx := strings.IndexByte(line, '|')
		url := line
		if pipeIdx != -1 {
			url = line[:pipeIdx]
		}

		if url != "" {
			cur.URL = url
			lastPipe := strings.LastIndexByte(line, '|')
			params := line
			if lastPipe != -1 {
				params = line[lastPipe+1:]
			}
			if m := paramUA.FindStringSubmatch(params); m != nil && m[1] != "" {
				cur.HTTP.UserAgent = m[1]
			}
			if m := paramRef.FindStringSubmatch(params); m != nil && m[1] != "" {
				cur.HTTP.Referrer = m[1]
			}
			if rawLines == nil {
				if cur.Raw != "" {
					cur.Raw += "\r\n" + raw
				} else {
					cur.Raw = raw
				}
			} else {
				rawLines = append(rawLines, raw)
				cur.Raw = strings.Join(rawLines, "\r\n")
			}
			pl.Items = append(pl.Items, *cur)
			cur = nil
			rawLines = nil
		} else {
			if rawLines == nil {
				rawLines = []string{cur.Raw}
			}
			rawLines = append(rawLines, raw)
		}
	}

	if cur != nil {
		flushRaw()
		pl.Items = append(pl.Items, *cur)
	}

	return pl, nil
}

// EPG header attribute names tracked like iptvnator.
var epgHeaderAttrs = []string{"x-tvg-url", "url-tvg", "tvg-url"}

var httpURL = regexp.MustCompile(`\bhttps?://[^\s,"']+`)

// ExtractEpgUrls gathers http(s) EPG urls from the playlist header, mirroring
// iptvnator's extractM3uEpgUrls (attrs + raw header scan).
func ExtractEpgUrls(pl Playlist) []string {
	seen := map[string]bool{}
	var urls []string
	scan := func(value string) {
		if value == "" {
			return
		}
		for _, u := range httpURL.FindAllString(value, -1) {
			u = strings.TrimSpace(u)
			if u != "" && !seen[u] {
				seen[u] = true
				urls = append(urls, u)
			}
		}
	}
	for _, name := range epgHeaderAttrs {
		scan(pl.Header.Attrs[name])
	}
	for _, a := range scanAttrs(pl.Header.Raw) {
		lower := strings.ToLower(a[0])
		for _, name := range epgHeaderAttrs {
			if strings.HasSuffix(lower, name) {
				scan(a[1])
			}
		}
	}
	return urls
}
