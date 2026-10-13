package airplay

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/mertturunc/iptv-tui/internal/dbg"
)

const (
	// serviceType is the mDNS service every AirPlay video receiver
	// (Apple TV, and AirPlay-compatible TVs/speakers that take video)
	// announces itself under.
	serviceType   = "_airplay._tcp"
	serviceDomain = "local."
)

// Scan browses the LAN for AirPlay receivers until ctx ends and returns them
// sorted by name. An empty list with no error means none answered; callers
// keep manual address entry as the fallback.
func Scan(ctx context.Context) ([]Device, error) {
	var out []Device
	err := browse(ctx, 0, nil, &out)
	return out, err
}

// ScanProgress browses like Scan but also calls emit with the current list
// every interval, so a sheet can list TVs as they answer instead of waiting
// out the whole timeout. A final emit always runs at the end.
func ScanProgress(ctx context.Context, interval time.Duration, emit func([]Device)) error {
	var out []Device
	return browse(ctx, interval, emit, &out)
}

func browse(ctx context.Context, interval time.Duration, emit func([]Device), out *[]Device) error {
	resolver, err := zeroconf.NewResolver(nil)
	if err != nil {
		return err
	}
	entries := make(chan *zeroconf.ServiceEntry)
	if err := resolver.Browse(ctx, serviceType, serviceDomain, entries); err != nil {
		return err
	}
	var raw []*zeroconf.ServiceEntry
	flush := func() {
		*out = collect(raw)
		if emit != nil {
			emit(*out)
		}
	}
	start := time.Now()
	dbg.Log("airplay: scan start")
	defer func() {
		dbg.Log("airplay: scan done in %s: %d TV(s)", time.Since(start).Round(100*time.Millisecond), len(*out))
		for _, d := range *out {
			dbg.Log("airplay: scan found %q at %s", d.Name, d.Addr)
		}
	}()
	var ticker <-chan time.Time
	if emit != nil && interval > 0 {
		t := time.NewTicker(interval)
		defer t.Stop()
		ticker = t.C
	}
	for {
		select {
		case e, ok := <-entries:
			if !ok {
				flush()
				return nil
			}
			raw = append(raw, e)
		case <-ticker:
			if len(raw) > 0 {
				flush()
			}
		case <-ctx.Done():
			flush()
			return nil
		}
	}
}

// deviceName prefers the mDNS instance string, which for AirPlay is the
// device name the owner set ("Living Room Apple TV"). The hostname is only
// a fallback.
func deviceName(e *zeroconf.ServiceEntry) string {
	if n := strings.TrimSpace(e.Instance); n != "" {
		return n
	}
	return strings.TrimSuffix(e.HostName, ".local.")
}

// deviceAddr prefers IPv4, which is what Dial wants.
func deviceAddr(e *zeroconf.ServiceEntry) string {
	if len(e.AddrIPv4) > 0 {
		return e.AddrIPv4[0].String()
	}
	if len(e.AddrIPv6) > 0 {
		return e.AddrIPv6[0].String()
	}
	return strings.TrimSuffix(e.HostName, ".")
}

// collect dedupes responders by address (a TV answers on several interfaces)
// and sorts by name for a stable sheet.
func collect(entries []*zeroconf.ServiceEntry) []Device {
	seen := map[string]bool{}
	var out []Device
	for _, e := range entries {
		addr := deviceAddr(e)
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		out = append(out, Device{Name: deviceName(e), Addr: addr})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
