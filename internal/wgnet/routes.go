package wgnet

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// CheckSubnetRouteConflict rejects an overlay subnet that overlaps an existing
// non-default IPv4 route. Routes owned by ignoreInterface are excluded so an
// already-running overlay remains idempotently configurable.
func CheckSubnetRouteConflict(subnet, ignoreInterface string) error {
	routes, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return fmt.Errorf("inspect routes before initializing WireGuard overlay %s: %w", subnet, err)
	}
	return checkProcRouteTable(subnet, ignoreInterface, routes)
}

func checkProcRouteTable(subnet, ignoreInterface string, routes []byte) error {
	overlay, err := netip.ParsePrefix(strings.TrimSpace(subnet))
	if err != nil {
		return fmt.Errorf("parse WireGuard overlay subnet %q: %w", subnet, err)
	}
	overlay = overlay.Masked()
	if !overlay.Addr().Is4() {
		return fmt.Errorf("WireGuard overlay subnet %q must be IPv4", subnet)
	}

	scanner := bufio.NewScanner(bytes.NewReader(routes))
	line := 0
	for scanner.Scan() {
		line++
		fields := strings.Fields(scanner.Text())
		if line == 1 || len(fields) < 8 {
			continue
		}
		iface := fields[0]
		if iface == ignoreInterface {
			continue
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&0x1 == 0 { // RTF_UP
			continue
		}
		destination, err := procRouteIPv4(fields[1])
		if err != nil {
			return fmt.Errorf("parse route table destination on line %d: %w", line, err)
		}
		maskAddr, err := procRouteIPv4(fields[7])
		if err != nil {
			return fmt.Errorf("parse route table mask on line %d: %w", line, err)
		}
		maskBytes := maskAddr.As4()
		ones, bits := net.IPMask(maskBytes[:]).Size()
		if bits != 32 {
			return fmt.Errorf("inspect routes before initializing WireGuard overlay %s: non-contiguous mask %s on interface %s", overlay, maskAddr, iface)
		}
		if ones == 0 {
			continue // the default route necessarily overlaps every subnet
		}
		route := netip.PrefixFrom(destination, ones).Masked()
		if prefixesOverlap(overlay, route) {
			return fmt.Errorf("WireGuard overlay subnet %s conflicts with existing route %s on interface %s; remove the route or configure a non-overlapping overlay subnet", overlay, route, iface)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("inspect routes before initializing WireGuard overlay %s: %w", overlay, err)
	}
	return nil
}

// Linux exposes /proc/net/route IPv4 words in little-endian byte order.
func procRouteIPv4(value string) (netip.Addr, error) {
	n, err := strconv.ParseUint(value, 16, 32)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid IPv4 word %q", value)
	}
	return netip.AddrFrom4([4]byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)}), nil
}

func prefixesOverlap(a, b netip.Prefix) bool {
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}
