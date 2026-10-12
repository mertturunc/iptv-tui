package xtream

import (
	"encoding/json"
	"testing"
)

// Provider-shaped fixture: string ids, empty-string ratings, season numbers
// as strings — all seen in the wild, all must parse.
const seriesInfoFixture = `{
	"seasons": [{"season_number": "1", "episode_count": "2"}],
	"episodes": {
		"1": [
			{"id": "901", "title": "Pilot", "episode_num": "1",
			 "container_extension": "mp4",
			 "info": {"name": "", "plot": "", "rating": ""}},
			{"id": 902, "episode_num": 2,
			 "container_extension": "mp4",
			 "info": {"name": "Second", "rating": "7.5"}}
		]
	}
}`

func TestSeriesInfoLenientShapes(t *testing.T) {
	var info SeriesInfo
	if err := json.Unmarshal([]byte(seriesInfoFixture), &info); err != nil {
		t.Fatalf("series_info parse: %v", err)
	}
	eps := info.Episodes["1"]
	if len(eps) != 2 {
		t.Fatalf("want 2 episodes, got %d", len(eps))
	}
	if int(eps[0].ID) != 901 || int(eps[1].ID) != 902 {
		t.Fatalf("string/number ids not parsed: %+v", eps)
	}
	if info.Seasons[0].SeasonNumber != 1 || info.Seasons[0].EpisodeCount != 2 {
		t.Fatalf("string season numbers not parsed: %+v", info.Seasons[0])
	}
	if eps[1].Info.Rating != 7.5 {
		t.Fatalf("string rating not parsed: %v", eps[1].Info.Rating)
	}
}

const streamFixture = `[
	{"num": 1, "name": "A", "stream_id": "1001", "category_id": "5"},
	{"num": "2", "name": "B", "stream_id": 1002, "category_id": "5"}
]`

func TestStreamStringIDs(t *testing.T) {
	var live []LiveStream
	if err := json.Unmarshal([]byte(streamFixture), &live); err != nil {
		t.Fatalf("live parse: %v", err)
	}
	if int(live[0].StreamID) != 1001 || int(live[1].StreamID) != 1002 {
		t.Fatalf("stream ids not parsed: %+v", live)
	}
	var vod []VODStream
	if err := json.Unmarshal([]byte(streamFixture), &vod); err != nil {
		t.Fatalf("vod parse: %v", err)
	}
	if int(vod[0].StreamID) != 1001 {
		t.Fatalf("vod stream id not parsed: %+v", vod[0])
	}
}

func TestFlexEmptyString(t *testing.T) {
	var i flexInt
	if err := json.Unmarshal([]byte(`""`), &i); err != nil || i != 0 {
		t.Fatalf("empty string int: %v %v", i, err)
	}
	var f flexFloat
	if err := json.Unmarshal([]byte(`""`), &f); err != nil || f != 0 {
		t.Fatalf("empty string float: %v %v", f, err)
	}
}
