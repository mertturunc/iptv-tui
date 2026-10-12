// Package xtream implements a minimal Xtream Codes IPTV API client.
// It covers authentication, live categories/streams, VOD categories/streams,
// series/categories/series_info, and an EPG helper.
package xtream

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Client struct {
	Server   string
	Username string
	Password string
	HC       *http.Client
}

func New(server, user, pass string) *Client {
	s := &Client{Server: trimSlash(server), Username: user, Password: pass, HC: &http.Client{Timeout: 15 * time.Second}}
	return s
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// flexParse decodes a JSON number that may arrive as a number, a numeric
// string, an empty string, or null — the shapes Xtream panels emit.
func flexParse(b []byte, parse func(string) error) error {
	if len(b) == 0 || string(b) == "null" {
		return parse("")
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		return parse(s)
	}
	var raw json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	return parse(string(raw))
}

// flexInt accepts JSON integers as well as numeric strings ("1" or 1), which
// some Xtream panels emit for fields that others send as numbers.
type flexInt int

func (f *flexInt) UnmarshalJSON(b []byte) error {
	return flexParse(b, func(s string) error {
		if s == "" || s == "null" {
			*f = 0
			return nil
		}
		// Trim JSON number encoding artefacts (e.g. 1.0 for an int field).
		if n, err := strconv.Atoi(s); err == nil {
			*f = flexInt(n)
			return nil
		}
		if fl, err := strconv.ParseFloat(s, 64); err == nil {
			*f = flexInt(int(fl))
			return nil
		}
		return strconv.ErrSyntax
	})
}

// flexFloat accepts JSON floats as well as numeric strings.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	return flexParse(b, func(s string) error {
		if s == "" || s == "null" {
			*f = 0
			return nil
		}
		n, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return err
		}
		*f = flexFloat(n)
		return nil
	})
}

func (c *Client) api(extra string) string {
	return fmt.Sprintf("%s/player_api.php?username=%s&password=%s%s",
		c.Server,
		url.QueryEscape(c.Username),
		url.QueryEscape(c.Password),
		extra,
	)
}

func (c *Client) get(path string) (json.RawMessage, error) {
	resp, err := c.HC.Get(path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var raw json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, string(b))
	}
	return raw, nil
}

type AuthInfo struct {
	Message    string  `json:"message"`
	Auth       int     `json:"auth"`
	Status     string  `json:"status"`
	ExpDate    flexInt `json:"exp_date"`
	IsTrial    string  `json:"is_trial"`
	Username   string  `json:"username"`
	Password   string  `json:"password"`
	MaxConn    string  `json:"max_connections"`
	ActiveCons string  `json:"active_cons"`
	ServerURL  string  `json:"server_url"`
	EMAC       string  `json:"mac"`
}

type UserInfo struct {
	Username   string  `json:"username"`
	Password   string  `json:"password"`
	Auth       int     `json:"auth"`
	Status     string  `json:"status"`
	ExpDate    flexInt `json:"exp_date"`
	ActiveCons string  `json:"active_cons"`
	MaxConn    string  `json:"max_connections"`
}

type AuthResult struct {
	UserInfo   UserInfo   `json:"user_info"`
	ServerInfo ServerInfo `json:"server_info"`
}
type ServerInfo struct {
	URL            string `json:"url"`
	Port           string `json:"port"`
	ServerProtocol string `json:"server_protocol"`
	RTMPUrl        string `json:"rtmp_url"`
	Timezone       string `json:"timezone"`
	TimestampNow   int64  `json:"timestamp_now"`
	TimeNow        string `json:"time_now"`
}

type Category struct {
	CategoryID   string  `json:"category_id"`
	CategoryName string  `json:"category_name"`
	ParentID     flexInt `json:"parent_id"`
}

type LiveStream struct {
	Num               flexInt `json:"num"`
	Name              string  `json:"name"`
	StreamType        string  `json:"stream_type"`
	StreamID          flexInt `json:"stream_id"`
	StreamIcon        string  `json:"stream_icon"`
	EpgChannelID      string  `json:"epg_channel_id"`
	Added             string  `json:"added"`
	IsAdult           string  `json:"is_adult"`
	CategoryID        string  `json:"category_id"`
	CustomSID         string  `json:"custom_sid"`
	TVArchive         flexInt `json:"tv_archive"`
	DirectSource      string  `json:"direct_source"`
	TVArchiveDuration flexInt `json:"tv_archive_duration"`
}

type VODStream struct {
	Num                flexInt   `json:"num"`
	Name               string    `json:"name"`
	StreamType         string    `json:"stream_type"`
	StreamID           flexInt   `json:"stream_id"`
	StreamIcon         string    `json:"stream_icon"`
	Rating             flexFloat `json:"rating"`
	RatingFiveBase     flexFloat `json:"rating_5based"`
	Genre              string    `json:"genre"`
	CategoryID         string    `json:"category_id"`
	ContainerExtension string    `json:"container_extension"`
	CustomSID          string    `json:"custom_sid"`
	DirectSource       string    `json:"direct_source"`
}

type Series struct {
	Num          flexInt   `json:"num"`
	Name         string    `json:"name"`
	SeriesID     flexInt   `json:"series_id"`
	Cover        string    `json:"cover"`
	Plot         string    `json:"plot"`
	Cast         string    `json:"cast"`
	Director     string    `json:"director"`
	Genre        string    `json:"genre"`
	ReleaseDate  string    `json:"releaseDate"`
	Rating       flexFloat `json:"rating"`
	BackdropPath []string  `json:"backdrop_path"`
	CategoryID   string    `json:"category_id"`
}

type SeriesInfo struct {
	Seasons  []Season             `json:"seasons"`
	Episodes map[string][]Episode `json:"episodes"`
}

type Season struct {
	SeasonNumber flexInt `json:"season_number"`
	AirDate      string  `json:"air_date"`
	EpisodeCount flexInt `json:"episode_count"`
	Overview     string  `json:"overview"`
	Cover        string  `json:"cover"`
	CoverBig     string  `json:"cover_big"`
}

type Episode struct {
	ID                 flexInt     `json:"id"`
	Title              string      `json:"title"`
	EpisodeNum         flexInt     `json:"episode_num"`
	ContainerExtension string      `json:"container_extension"`
	Info               EpisodeInfo `json:"info"`
}

type EpisodeInfo struct {
	Name     string    `json:"name"`
	Plot     string    `json:"plot"`
	Duration string    `json:"duration"`
	Rating   flexFloat `json:"rating"`
	AirDate  string    `json:"air_date"`
}

type EPGItem struct {
	ID             flexInt `json:"id"`
	EpgID          string  `json:"epg_id"`
	Title          string  `json:"title"`
	Language       string  `json:"lang"`
	Start          string  `json:"start"`
	End            string  `json:"end"`
	Description    string  `json:"description"`
	ChannelID      string  `json:"channel_id"`
	ChannelName    string  `json:"channel_name"`
	StartTimestamp string  `json:"start_timestamp"`
	EndTimestamp   string  `json:"end_timestamp"`
}

type SimpleEPG struct {
	Title       string
	StartTime   time.Time
	EndTime     time.Time
	Description string
}

// ParseXTIMEStamp tries the formats used by Xtream APIs.
func ParseXTIMEStamp(raw string) (time.Time, error) {
	formats := []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		"20060102150405 -0700",
	}
	for _, f := range formats {
		t, err := time.Parse(f, raw)
		if err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("bad timestamp: %q", raw)
}

// LiveURL constructs the direct play URL for a live stream.
func (c *Client) LiveURL(s LiveStream) string {
	return fmt.Sprintf("%s/live/%s/%s/%d.m3u8", c.Server, c.Username, c.Password, s.StreamID)
}

// VODURL returns the direct URL for a VOD/movie stream.
func (c *Client) VODURL(s VODStream) string {
	ext := s.ContainerExtension
	if ext == "" {
		ext = "mp4"
	}
	return fmt.Sprintf("%s/movie/%s/%s/%d.%s", c.Server, c.Username, c.Password, s.StreamID, ext)
}

// EpisodeURL constructs the direct URL for a series episode.
func (c *Client) EpisodeURL(ep Episode) string {
	ext := ep.ContainerExtension
	if ext == "" {
		ext = "mp4"
	}
	return fmt.Sprintf("%s/series/%s/%s/%d.%s", c.Server, c.Username, c.Password, ep.ID, ext)
}

func (c *Client) Authenticate() (AuthResult, error) {
	var r AuthResult
	raw, err := c.get(c.api(""))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return r, fmt.Errorf("auth parse: %w", err)
	}
	return r, nil
}

func (c *Client) getArray(path string) ([]json.RawMessage, error) {
	raw, err := c.get(path)
	if err != nil {
		return nil, err
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		// Some servers wrap in {"data":[...]} or return {} on empty.
		var wrap struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &wrap); err != nil || len(wrap.Data) == 0 {
			return nil, nil
		}
		return wrap.Data, nil
	}
	return arr, nil
}

func (c *Client) fetchList[T any](action string) ([]T, error) {
	raw, err := c.getArray(c.api(action))
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(raw))
	for _, r := range raw {
		var v T
		if err := json.Unmarshal(r, &v); err == nil {
			out = append(out, v)
		}
	}
	return out, nil
}

func (c *Client) LiveCategories() ([]Category, error) {
	return c.fetchList[Category]("&action=get_live_categories")
}

func (c *Client) LiveStreams() ([]LiveStream, error) {
	return c.fetchList[LiveStream]("&action=get_live_streams")
}

func (c *Client) VodCategories() ([]Category, error) {
	return c.fetchList[Category]("&action=get_vod_categories")
}

func (c *Client) VodStreams() ([]VODStream, error) {
	return c.fetchList[VODStream]("&action=get_vod_streams")
}

func (c *Client) SeriesCategories() ([]Category, error) {
	return c.fetchList[Category]("&action=get_series_categories")
}

func (c *Client) SeriesList() ([]Series, error) {
	return c.fetchList[Series]("&action=get_series")
}

func (c *Client) SeriesInfo(seriesID int) (SeriesInfo, error) {
	var info SeriesInfo
	raw, err := c.get(c.api("&action=get_series_info&series_id=" + strconv.Itoa(seriesID)))
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(raw, &info); err != nil {
		return info, fmt.Errorf("series_info parse: %w", err)
	}
	return info, nil
}

// EPG returns short EPG items for a given stream ID.
func (c *Client) EPG(streamID int) ([]SimpleEPG, error) {
	raw, err := c.get(c.api("&action=get_short_epg&stream_id=" + strconv.Itoa(streamID)))
	if err != nil {
		return nil, err
	}
	var wrap struct {
		EPGList []EPGItem `json:"epg_listings"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		return nil, err
	}
	var out []SimpleEPG
	for _, item := range wrap.EPGList {
		var e SimpleEPG
		e.Title = item.Title
		e.Description = item.Description
		if t, err := ParseXTIMEStamp(item.Start); err == nil {
			e.StartTime = t
		}
		if t, err := ParseXTIMEStamp(item.End); err == nil {
			e.EndTime = t
		}
		out = append(out, e)
	}
	return out, nil
}
