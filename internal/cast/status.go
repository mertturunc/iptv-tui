package cast

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mertturunc/iptv-tui/internal/dbg"
)

// MediaStatus is the receiver's answer to GET_STATUS on the media channel:
// what approach-B monitoring is built from.
type MediaStatus struct {
	PlayerState string  // PLAYING, PAUSED, BUFFERING, IDLE, LOADING
	CurrentTime float64 // seconds into the media
	Duration    float64 // seconds; 0 when unreported (typical for live)
	Rate        float64 // playback rate, usually 1
}

// Status asks the receiver how playback is going. The round-trip is returned
// alongside: it measures the control link only, never stream bitrate —
// receivers do not report download speed, which is what the local proxy
// measures instead (approach A).
func (c *Caster) Status(ctx context.Context) (*MediaStatus, time.Duration, error) {
	c.mu.Lock()
	transport := c.transportID
	c.mu.Unlock()
	if transport == "" {
		return nil, 0, fmt.Errorf("cast: no receiver launched")
	}
	start := time.Now()
	if err := c.send(payloadType{Type: "GET_STATUS", RequestID: c.nextID()}, nsMedia, transport); err != nil {
		return nil, 0, err
	}
	rtt := time.Since(start)
	deadline := deadlineOf(ctx, 8*time.Second)
	for {
		m, err := c.recv(deadline)
		if err != nil {
			return nil, 0, err
		}
		if m.namespace != nsMedia {
			continue
		}
		var st struct {
			Type   string `json:"type"`
			Reason string `json:"reason,omitempty"`
			Status []struct {
				PlayerState  string  `json:"playerState"`
				CurrentTime  float64 `json:"currentTime"`
				PlaybackRate float64 `json:"playbackRate"`
				Media        *struct {
					Duration float64 `json:"duration"`
				} `json:"media"`
			} `json:"status"`
		}
		if err := json.Unmarshal([]byte(m.payload), &st); err != nil {
			continue
		}
		switch st.Type {
		case "LOAD_FAILED", "LOAD_CANCELLED":
			if st.Reason == "" {
				st.Reason = st.Type
			}
			return nil, 0, fmt.Errorf("cast: tv refused the stream (%s)", st.Reason)
		case "MEDIA_STATUS":
			if len(st.Status) == 0 {
				continue
			}
			s := st.Status[0]
			out := &MediaStatus{PlayerState: s.PlayerState, CurrentTime: s.CurrentTime, Rate: s.PlaybackRate}
			if s.Media != nil {
				out.Duration = s.Media.Duration
			}
			if out.PlayerState == "" {
				out.PlayerState = "IDLE"
			}
			dbg.Log("cast: status %s pos=%.1f dur=%.1f rate=%.2f rtt=%dms",
				out.PlayerState, out.CurrentTime, out.Duration, out.Rate, int(rtt/time.Millisecond))
			return out, rtt, nil
		}
	}
}

// ShortState trims a player state for the one-row playbar.
func ShortState(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch s {
	case "PLAYING", "BUFFERING", "PAUSED", "IDLE", "LOADING":
		return s
	case "":
		return "IDLE"
	default:
		return s
	}
}
