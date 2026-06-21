package wireguard

import (
	"fmt"
	"net"
	"sort"
)

// ValidNodeIP reports whether ip falls inside the allocatable node range
// (10.10.0.2 through 10.10.0.254).
func ValidNodeIP(ip string) bool {
	parsed := net.ParseIP(ip).To4()
	if parsed == nil {
		return false
	}
	if parsed[0] != 10 || parsed[1] != 10 || parsed[2] != 0 {
		return false
	}
	return parsed[3] >= 2 && parsed[3] <= 254
}

// AllocateIP returns the smallest unused IP in the node range given the set
// of currently assigned IPs. Recycled IPs from deleted nodes are reused.
func AllocateIP(assigned []string) (string, error) {
	taken := map[byte]bool{}
	for _, ip := range assigned {
		parsed := net.ParseIP(ip).To4()
		if parsed == nil {
			continue
		}
		if parsed[0] == 10 && parsed[1] == 10 && parsed[2] == 0 {
			taken[parsed[3]] = true
		}
	}
	for octet := byte(2); octet <= 254; octet++ {
		if !taken[octet] {
			return fmt.Sprintf("10.10.0.%d", octet), nil
		}
	}
	return "", fmt.Errorf("internal subnet %s is exhausted (253 nodes maximum)", SubnetCIDR)
}

// SortPeersByIP returns peers ordered by their assigned IP, ascending. Order
// is stable across regenerations so config diffs stay minimal.
func SortPeersByIP(peers []Peer) []Peer {
	out := make([]Peer, len(peers))
	copy(out, peers)
	sort.Slice(out, func(i, j int) bool {
		return ipLess(out[i].IP, out[j].IP)
	})
	return out
}

func ipLess(a, b string) bool {
	ai := net.ParseIP(a).To4()
	bi := net.ParseIP(b).To4()
	if ai == nil || bi == nil {
		return a < b
	}
	for k := 0; k < 4; k++ {
		if ai[k] != bi[k] {
			return ai[k] < bi[k]
		}
	}
	return false
}
