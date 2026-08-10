package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
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
// carrier-NAT ranges are excluded, which also keeps the WireGuard overlay out.
//
// The empty table declaration before the delete is the standard atomic-replace
// idiom: it makes the delete succeed whether or not the table already exists.
const ipAcctRuleset = `
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
		ip saddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/4, 240.0.0.0/4 } return
		ip6 saddr { ::1/128, fc00::/7, fe80::/10, ff00::/8 } return
		update @peer_in4 { ip saddr }
		update @peer_in6 { ip6 saddr }
	}

	chain peers_out {
		type filter hook output priority 0; policy accept;
		ct direction original return
		ip daddr { 0.0.0.0/8, 10.0.0.0/8, 100.64.0.0/10, 127.0.0.0/8, 169.254.0.0/16, 172.16.0.0/12, 192.0.0.0/24, 192.168.0.0/16, 198.18.0.0/15, 224.0.0.0/4, 240.0.0.0/4 } return
		ip6 daddr { ::1/128, fc00::/7, fe80::/10, ff00::/8 } return
		update @peer_out4 { ip daddr }
		update @peer_out6 { ip6 daddr }
	}
}
`

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
	// previous holds the last counter value read per set and address, so a
	// sample can be turned into a delta.
	previous map[string]uint64
	loaded   bool
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
		previous: map[string]uint64{},
	}
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
	if !a.loaded {
		if err := a.apply(ctx); err != nil {
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

// counterDelta treats a counter that moved backwards as one that restarted:
// nftables evicts an element after its idle timeout, and the address's next
// packet re-adds it from zero.
func counterDelta(previous, current uint64) uint64 {
	if current >= previous {
		return current - previous
	}
	return current
}

func (a *IPAccounting) apply(ctx context.Context) error {
	if _, err := a.run(ctx, ipAcctRuleset, "-f", "-"); err != nil {
		return fmt.Errorf("install per-IP accounting ruleset: %w", err)
	}
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
