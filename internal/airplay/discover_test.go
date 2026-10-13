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
