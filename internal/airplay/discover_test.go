package airplay

import (
	"net"
	"testing"

	"github.com/grandcat/zeroconf"
)

func entry(instance, host string, ipv4 ...string) *zeroconf.ServiceEntry {
	e := &zeroconf.ServiceEntry{Instance: instance, HostName: host + ".local."}
	for _, a := range ipv4 {
		e.AddrIPv4 = append(e.AddrIPv4, net.ParseIP(a))
	}
	return e
}

func TestCollectPrefersInstanceName(t *testing.T) {
	devs := collect([]*zeroconf.ServiceEntry{
		entry("Living Room Apple TV", "Living-Room", "192.168.1.10"),
	})
	if len(devs) != 1 {
		t.Fatalf("collect = %v, want 1 device", devs)
	}
	if devs[0].Name != "Living Room Apple TV" {
		t.Fatalf("Name = %q, want instance name", devs[0].Name)
	}
	if devs[0].Addr != "192.168.1.10" {
		t.Fatalf("Addr = %q, want IPv4", devs[0].Addr)
	}
}

func TestCollectDedupesByAddressAndSorts(t *testing.T) {
	devs := collect([]*zeroconf.ServiceEntry{
		entry("Zebra TV", "zebra", "192.168.1.11"),
		entry("Zebra TV", "zebra", "192.168.1.11"), // same TV, second interface
		entry("Alpha TV", "alpha", "192.168.1.12"),
	})
	if len(devs) != 2 {
		t.Fatalf("collect = %v, want 2 devices", devs)
	}
	if devs[0].Name != "Alpha TV" || devs[1].Name != "Zebra TV" {
		t.Fatalf("collect = %v, want sorted by name", devs)
	}
}

func TestCollectFallsBackToHostname(t *testing.T) {
	devs := collect([]*zeroconf.ServiceEntry{
		{HostName: "Kitchen.local.", AddrIPv4: []net.IP{net.ParseIP("192.168.1.13")}},
	})
	if len(devs) != 1 || devs[0].Name != "Kitchen" {
		t.Fatalf("collect = %v, want hostname fallback", devs)
	}
}

func TestCollectKeepsModelAndFeatures(t *testing.T) {
	e := entry("Living Room Apple TV", "Living-Room", "192.168.1.10")
	e.Text = []string{"model=AppleTV5,3", "features=0x4A7FCA00", "protovers=1.0"}
	devs := collect([]*zeroconf.ServiceEntry{e})
	if len(devs) != 1 {
		t.Fatalf("collect = %v, want 1 device", devs)
	}
	if devs[0].Model != "AppleTV5,3" || devs[0].Features != "0x4A7FCA00" {
		t.Fatalf("collect = %+v, want model+features kept", devs[0])
	}
}

func TestVideoCaps(t *testing.T) {
	cases := []struct {
		features string
		v1, v2   bool
	}{
		{"0x1", true, false},             // only legacy video bit
		{"0x2000000000000", false, true}, // only AirPlay 2 video bit (bit 49)
		{"0x2000000000001", true, true},  // both
		{"1", true, false},               // decimal broadcast
		{"garbage", false, false},
		{"", false, false},
	}
	for _, tc := range cases {
		if v1, v2 := videoCaps(tc.features); v1 != tc.v1 || v2 != tc.v2 {
			t.Errorf("videoCaps(%q) = %v,%v; want %v,%v", tc.features, v1, v2, tc.v1, tc.v2)
		}
	}
}
