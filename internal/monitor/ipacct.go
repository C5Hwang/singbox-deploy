package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ipAcctTable is the nftables table the per-IP counters live in. It is a table
// of this deployment's own, so it never touches the rules ufw or firewalld
// manage: nftables evaluates every base chain registered on a hook, and these
// chains only count.
const ipAcctTable = "singbox_deploy_monitor"

// ipAcctCommandTimeout bounds one nft invocation. Listing a full set is a
// kernel dump measured in milliseconds; anything near this is a stuck host.
const ipAcctCommandTimeout = 10 * time.Second

// ipAcctRuleset counts bytes per remote address, split by direction.
//
// The direction test is what makes the counters describe clients rather than
// the sites the proxy fetches on their behalf: a connection someone opened to
// this host is "original" inbound and "reply" outbound, and a connection this
// host opened is the mirror image. Private, loopback, link-local, multicast and
// carrier-NAT ranges are excluded.
//
// Excluding those ranges does not keep the WireGuard overlay out. Only the
// tunnelled 10.90.0.0/24 addresses are private; the packets carrying them
// travel between the fleet's public addresses, so without overlayReturn a hub
// is ranked as a client of every spoke it manages, usually the busiest one on a
// quiet node. overlayPorts holds the local WireGuard listen ports, which are
// the destination port inbound and the source port outbound, so one port per
// interface covers both directions on both ends of a tunnel.
//
// The empty table declaration before the delete is the standard atomic-replace
// idiom: it makes the delete succeed whether or not the table already exists.
func ipAcctRuleset(overlayPorts []int) string {
	return `
table inet ` + ipAcctTable + ` {}
delete table inet ` + ipAcctTable + `

table inet ` + ipAcctTable + ` {
	set peer_in4 {
		type ipv4_addr
		size 65535
		flags dynamic
		timeout 2h
		counter
	}
	set peer_out4 {
		type ipv4_addr
		size 65535
		flags dynamic
		timeout 2h
		counter
	}
	set peer_in6 {
		type ipv6_addr
		size 65535
		flags dynamic
		timeout 2h
		counter
	}
	set peer_out6 {
		type ipv6_addr
		size 65535
		flags dynamic
		timeout 2h
		counter
	}

	chain peers_in {
		type filter hook input priority 0; policy accept;
		ct direction reply return
` + overlayReturn("dport", overlayPorts) + `		ip saddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/4, 240.0.0.0/4 } return
		ip6 saddr { ::1/128, fc00::/7, fe80::/10, ff00::/8 } return
		update @peer_in4 { ip saddr }
		update @peer_in6 { ip6 saddr }
	}

	chain peers_out {
		type filter hook output priority 0; policy accept;
		ct direction original return
` + overlayReturn("sport", overlayPorts) + `		ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/4, 240.0.0.0/4 } return
		ip6 daddr { ::1/128, fc00::/7, fe80::/10, ff00::/8 } return
		update @peer_out4 { ip daddr }
		update @peer_out6 { ip6 daddr }
	}
}
`
}

// overlayReturn renders the rule that skips the overlay's own UDP transport,
// or nothing at all on a host running no WireGuard interface.
func overlayReturn(direction string, ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, strconv.Itoa(port))
	}
	return "\t\tudp " + direction + " { " + strings.Join(values, ", ") + " } return\n"
}

// ipAcctSets pairs each counter set with the direction it accumulates.
var ipAcctSets = []struct {
	name    string
	inbound bool
}{
	{name: "peer_in4", inbound: true},
	{name: "peer_in6", inbound: true},
	{name: "peer_out4"},
	{name: "peer_out6"},
}

// IPTrafficDelta is one remote address's traffic since the previous read.
type IPTrafficDelta struct {
	InBytes  uint64
	OutBytes uint64
}

// IPAccounting samples nftables counters into per-address deltas. It is nil on
// a host with no nft utility, which disables per-IP accounting and leaves every
// other metric untouched.
type IPAccounting struct {
	// run is the test seam. stdin is passed to nft for ruleset loads.
	run func(ctx context.Context, stdin string, args ...string) ([]byte, error)
	// overlayPorts reports the WireGuard listen ports to keep out of the
	// counters. It is a seam for the same reason run is.
	overlayPorts func(ctx context.Context) []int
	// previous holds the last counter value read per set and address, so a
	// sample can be turned into a delta.
	previous map[string]uint64
	// ports is the overlay port list the loaded ruleset was built from. A
	// wg-quick restart hands the interface a new ephemeral port, so a changed
	// list has to rebuild the table rather than keep counting the tunnel.
	ports  []int
	loaded bool
}

// NewIPAccounting returns a sampler, or nil when the host has no nft utility.
func NewIPAccounting() *IPAccounting {
	binary, err := exec.LookPath("nft")
	if err != nil {
		return nil
	}
	return &IPAccounting{
		run: func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
			return runNFT(ctx, binary, stdin, args...)
		},
		overlayPorts: wireGuardListenPorts,
		previous:     map[string]uint64{},
	}
}

// wireGuardListenPorts asks wg(8) which UDP ports the overlay listens on. A
// host with no wg utility, no interface, or an unreadable answer yields none,
// which only means the overlay is not excluded from the counters.
func wireGuardListenPorts(ctx context.Context) []int {
	binary, err := exec.LookPath("wg")
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, ipAcctCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary, "show", "all", "listen-port").Output()
	if err != nil {
		return nil
	}
	return parseWireGuardListenPorts(string(out))
}

// parseWireGuardListenPorts reads the "<interface>\t<port>" lines wg prints.
func parseWireGuardListenPorts(out string) []int {
	var ports []int
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		port, err := strconv.Atoi(fields[len(fields)-1])
		if err != nil || port < 1 || port > 65535 || slices.Contains(ports, port) {
			continue
		}
		ports = append(ports, port)
	}
	slices.Sort(ports)
	return ports
}

func runNFT(ctx context.Context, binary, stdin string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, ipAcctCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.Output()
	if err != nil {
		// nft explains itself on stderr, which Output() discards into the error.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

// Enabled reports whether per-IP accounting can run on this host.
func (a *IPAccounting) Enabled() bool { return a != nil }

// Collect returns the traffic each remote address moved since the previous
// call. The ruleset is installed on first use and reinstalled if it disappears,
// so a firewall reload that flushes it self-heals on the next sample.
func (a *IPAccounting) Collect(ctx context.Context) (map[string]IPTrafficDelta, error) {
	if a == nil {
		return nil, nil
	}
	if ports := a.currentOverlayPorts(ctx); !a.loaded || !slices.Equal(ports, a.ports) {
		if err := a.apply(ctx, ports); err != nil {
			return nil, err
		}
	}
	deltas := map[string]IPTrafficDelta{}
	for _, set := range ipAcctSets {
		counters, err := a.readSet(ctx, set.name)
		if err != nil {
			// The table is gone (a firewall reload, or a manual flush). Rebuild
			// it and let the next round produce numbers rather than reporting
			// the interrupted one.
			a.loaded = false
			a.previous = map[string]uint64{}
			return nil, err
		}
		for address, current := range counters {
			key := set.name + "|" + address
			delta := counterDelta(a.previous[key], current)
			a.previous[key] = current
			if delta == 0 {
				continue
			}
			entry := deltas[address]
			if set.inbound {
				entry.InBytes += delta
			} else {
				entry.OutBytes += delta
			}
			deltas[address] = entry
		}
	}
	return deltas, nil
}

// currentOverlayPorts reads the overlay's listen ports for this round.
func (a *IPAccounting) currentOverlayPorts(ctx context.Context) []int {
	if a.overlayPorts == nil {
		return nil
	}
	return a.overlayPorts(ctx)
}

// counterDelta treats a counter that moved backwards as one that restarted:
// nftables evicts an element after its idle timeout, and the address's next
// packet re-adds it from zero.
func counterDelta(previous, current uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func (a *IPAccounting) apply(ctx context.Context, overlayPorts []int) error {
	if _, err := a.run(ctx, ipAcctRuleset(overlayPorts), "-f", "-"); err != nil {
		return fmt.Errorf("install per-IP accounting ruleset: %w", err)
	}
	a.ports = overlayPorts
	a.loaded = true
	// A fresh table starts at zero, so nothing carried over from the previous
	// one may be subtracted from it.
	a.previous = map[string]uint64{}
	return nil
}

// Remove drops the accounting table. It is called when the monitor shuts down
// so a disabled monitor leaves no chains registered on the host's hooks.
func (a *IPAccounting) Remove(ctx context.Context) error {
	if a == nil || !a.loaded {
		return nil
	}
	a.loaded = false
	_, err := a.run(ctx, "", "delete", "table", "inet", ipAcctTable)
	return err
}

func (a *IPAccounting) readSet(ctx context.Context, name string) (map[string]uint64, error) {
	out, err := a.run(ctx, "", "-j", "list", "set", "inet", ipAcctTable, name)
	if err != nil {
		return nil, fmt.Errorf("read per-IP counter set %s: %w", name, err)
	}
	return parseNFTCounterSet(out)
}

// nftSetListing is the subset of `nft -j list set` this needs. Elements are
// left raw: a set carrying counters lists objects, but an empty or
// counter-less set lists bare address strings, and both shapes appear in the
// same field.
type nftSetListing struct {
	Nftables []struct {
		Set *struct {
			Elem []json.RawMessage `json:"elem"`
		} `json:"set"`
	} `json:"nftables"`
}

type nftCounterElement struct {
	Elem *struct {
		Val     string `json:"val"`
		Counter *struct {
			Bytes uint64 `json:"bytes"`
		} `json:"counter"`
	} `json:"elem"`
}

func parseNFTCounterSet(raw []byte) (map[string]uint64, error) {
	var listing nftSetListing
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("decode nft output: %w", err)
	}
	counters := map[string]uint64{}
	for _, entry := range listing.Nftables {
		if entry.Set == nil {
			continue
		}
		for _, raw := range entry.Set.Elem {
			var element nftCounterElement
			if err := json.Unmarshal(raw, &element); err != nil {
				continue // a bare address string: no counter to read
			}
			if element.Elem == nil || element.Elem.Counter == nil || element.Elem.Val == "" {
				continue
			}
			counters[element.Elem.Val] = element.Elem.Counter.Bytes
		}
	}
	return counters, nil
}
