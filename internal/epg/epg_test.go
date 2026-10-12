package epg

import (
	"testing"
	"time"
)

const xmltvFixture = `<tv>
<channel id="bbc1"><display-name>BBC One</display-name></channel>
<programme start="20260101120000 +0000" stop="20260101130000 +0000" channel="bbc1"><title>Noon News</title><desc>Daily bulletin</desc></programme>
<programme start="2026-01-01T13:00:00Z" stop="2026-01-01T14:00:00Z" channel="bbc1"><title>Doc Hour</title></programme>
</tv>`

func TestFromXMLTVShapes(t *testing.T) {
	idx, err := FromXMLTV([]byte(xmltvFixture))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	progs := idx.Get("bbc1")
	if len(progs) != 2 {
		t.Fatalf("want 2 programmes, got %d", len(progs))
	}
	if progs[0].Title != "Noon News" || progs[0].Desc != "Daily bulletin" {
		t.Fatalf("first prog: %+v", progs[0])
	}
	if !progs[0].Start.Before(progs[1].Start) {
		t.Fatal("programmes should be time-sorted")
	}
	got := GuideFor(idx, "bbc1", time.Date(2026, 1, 1, 12, 30, 0, 0, time.UTC), 1)
	if len(got) != 1 || got[0].Title != "Noon News" {
		t.Fatalf("guide from 12:30: %+v", got)
	}
}

func TestNowNextLive(t *testing.T) {
	now := time.Now()
	idx, err := FromXMLTV([]byte(`<tv>
<programme start="` + now.Add(-time.Hour).Format("20060102150405 -0700") + `" stop="` +
		now.Add(time.Hour).Format("20060102150405 -0700") + `" channel="c"><title>On Now</title></programme>
<programme start="` + now.Add(time.Hour).Format("20060102150405 -0700") + `" stop="` +
		now.Add(2*time.Hour).Format("20060102150405 -0700") + `" channel="c"><title>Up Next</title></programme>
</tv>`))
	if err != nil {
		t.Fatal(err)
	}
	cur, nxt := NowNext(idx, "c")
	if cur == nil || cur.Title != "On Now" {
		t.Fatalf("now: %+v", cur)
	}
	if nxt == nil || nxt.Title != "Up Next" {
		t.Fatalf("next: %+v", nxt)
	}
}
