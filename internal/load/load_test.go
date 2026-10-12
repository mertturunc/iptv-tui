package load

import (
	"strings"
	"testing"

	"github.com/mertturunc/iptv-tui/internal/store"
	"github.com/mertturunc/iptv-tui/internal/xtream"
)

func TestMapEpisodesTitlePriority(t *testing.T) {
	src := &store.Source{ID: "s", Name: "demo", Server: "https://demo.example.com", Username: "u", Password: "p"}
	info := xtream.SeriesInfo{
		Episodes: map[string][]xtream.Episode{
			"1": {
				{ID: 1, Title: "Top Title", EpisodeNum: 1, ContainerExtension: "mp4", Info: xtream.EpisodeInfo{Name: "Info Name"}},
				{ID: 2, EpisodeNum: 2, ContainerExtension: "mp4", Info: xtream.EpisodeInfo{Name: "Only Info"}},
				{ID: 3, EpisodeNum: 3, ContainerExtension: "mp4"},
			},
		},
	}
	out := mapEpisodes(src, 9, info)
	if len(out) != 3 {
		t.Fatalf("want 3 episodes, got %d", len(out))
	}
	byEp := map[int]string{}
	for _, ch := range out {
		byEp[ch.Episode] = ch.Name
		if ch.SeriesID != 9 || ch.Group != "Season 1" {
			t.Fatalf("bad mapping: %+v", ch)
		}
	}
	if !strings.Contains(byEp[1], "Top Title") {
		t.Fatalf("top-level title should win: %q", byEp[1])
	}
	if !strings.Contains(byEp[2], "Only Info") {
		t.Fatalf("info.name fallback broken: %q", byEp[2])
	}
	if !strings.Contains(byEp[3], "Episode 3") {
		t.Fatalf("numbered fallback broken: %q", byEp[3])
	}
}
