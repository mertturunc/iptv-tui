package store

import (
	"encoding/json"
	"testing"
)

func TestAirPlayTVRoundTrip(t *testing.T) {
	s := &Store{Cfg: DefaultConfig()}
	if s.AirPlayTV() != "" {
		t.Fatalf("AirPlayTV = %q, want empty default", s.AirPlayTV())
	}
	s.SetAirPlayTV("192.168.1.10")
	if s.AirPlayTV() != "192.168.1.10" {
		t.Fatalf("AirPlayTV = %q, want 192.168.1.10", s.AirPlayTV())
	}
	// The address must survive a save/load cycle: it is the sheet fallback.
	raw, err := json.Marshal(s.Cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Settings.AirPlayTV != "192.168.1.10" {
		t.Fatalf("persisted airPlayTV = %q, want 192.168.1.10", back.Settings.AirPlayTV)
	}
}
