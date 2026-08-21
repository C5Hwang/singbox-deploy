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
// The peers chains describe only traffic this host terminates. A flow the
// relay data plane DNATs (internal/relay) is routed, not delivered, so it
// crosses the forward hook instead — which is where the relay_* sets count it,
// still attributed to the client's address. Those two are keyed by the address
// and the port it arrived on, because one relay commonly fronts several landing
// nodes and the port is what says which: the DNAT rewrites the destination, but
// conntrack still remembers the listen port the client reached, and every
// listen port belongs to exactly one landing node. The relay sets are declared
// even on a node that relays for nobody, so the collector can read all of them
// without caring whether the forward chain was rendered. They have no v6
// counterparts because the relay's own ruleset is a `table ip`.
//
// The empty table declaration before the delete is the standard atomic-replace
// idiom: it makes the delete succeed whether or not the table already exists.
func ipAcctRuleset(overlayPorts, relayPorts []int) string {
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
	set relay_in4 {
		type ipv4_addr . inet_service
		size 65535
		flags dynamic
		timeout 2h
		counter
	}
	set relay_out4 {
		type ipv4_addr . inet_service
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
` + relayForwardChain(relayPorts) + `}
`
}

// relayForwardChain renders the chain that counts the relay's forwarded flows,
// or nothing on a node that relays for nobody. The conntrack entry is created
// before the DNAT rewrites anything, so the original tuple still carries the
// relay's own listen port; requiring it alongside the DNAT status pins these
// rules to the relay's flows and to nothing else this host might forward.
//
// Direction reads inverted on this path: an original packet travels
// client-to-landing, so the client is its source, and a reply travels
// landing-to-client, so the client is its destination. The private-range
// excludes of the peers chains are not repeated here, because the port test
// already restricts the chain to flows the relay accepted on its listeners.
func relayForwardChain(ports []int) string {
	if len(ports) == 0 {
		return ""
	}
	values := make([]string, 0, len(ports))
	for _, port := range ports {
		values = append(values, strconv.Itoa(port))
	}
	match := "ct status dnat ct original proto-dst { " + strings.Join(values, ", ") + " }"
	return `
	chain peers_fwd {
		type filter hook forward priority 0; policy accept;
		` + match + ` ct direction original update @relay_in4 { ip saddr . ct original proto-dst }
		` + match + ` ct direction reply update @relay_out4 { ip daddr . ct original proto-dst }
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

// RelayForward is one port the relay data plane answers on and the landing node
// it fronts. The monitor needs both: the ports pin the forward chain to the
// relay's own flows, and the landing behind each one is what lets a client's
// forwarded bytes be reported per destination instead of as one relayed lump.
type RelayForward struct {
	ListenPort int
	// LandingID is the landing node's stable registry identity. It is the half
	// of the accounting key that survives a rename, so a landing node relabelled
	// mid-cycle keeps its history.
	LandingID string
	// LandingName is the landing node's display alias, attached to the entries
	// the API returns rather than stored, so the dashboard never has to resolve
	// an ID it has no registry for.
	LandingName string
}

// relayForwardPorts reduces the relay's job to the ports the forward chain
// matches on, deduplicated and ordered so the rendered ruleset is stable.
func relayForwardPorts(forwards []RelayForward) []int {
	seen := make(map[int]struct{}, len(forwards))
	ports := make([]int, 0, len(forwards))
	for _, forward := range forwards {
		if forward.ListenPort < 1 || forward.ListenPort > 65535 {
			continue
		}
		if _, duplicate := seen[forward.ListenPort]; duplicate {
			continue
		}
		seen[forward.ListenPort] = struct{}{}
		ports = append(ports, forward.ListenPort)
	}
	slices.Sort(ports)
	return ports
}

// relayLandingsByPort maps each relay listen port onto the landing node behind
// it. One port belongs to one landing node, and internal/relaylinks is what
// makes that true: it claims a generated port across the whole relay by number
// alone, so no two links it writes can share one. Config.Validate is not that
// guarantee and must not be read as one — it claims transport and port
// together, because what it guards is a socket conflict, and tcp/N and udp/N
// are two sockets.
//
// A job that names two landing nodes on one port anyway is one nothing in this
// fleet wrote, and its bytes cannot honestly be split between them. The port is
// left naming neither, so its traffic records as relayed to a destination this
// node cannot name — which is the truth — rather than being handed whole to
// whichever landing happened to be read first. That also makes the mapping a
// function of the job's content and not of the order it is read in.
func relayLandingsByPort(forwards []RelayForward) map[int]string {
	// An empty landing reads to the caller exactly like a port with no mapping
	// at all, which is what a contested port has to amount to.
	landings := make(map[int]string, len(forwards))
	for _, forward := range forwards {
		if claimed, seen := landings[forward.ListenPort]; seen && claimed != forward.LandingID {
			landings[forward.ListenPort] = ""
			continue
		}
		landings[forward.ListenPort] = forward.LandingID
	}
	return landings
}

// ipAcctSets pairs each counter set with the direction it accumulates and
// whether it meters the relay's forwarded flows rather than direct peers.
var ipAcctSets = []struct {
	name    string
	inbound bool
	relayed bool
}{
	{name: "peer_in4", inbound: true},
	{name: "peer_in6", inbound: true},
	{name: "peer_out4"},
	{name: "peer_out6"},
	{name: "relay_in4", inbound: true, relayed: true},
	{name: "relay_out4", relayed: true},
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
	// relayForwards reports the relay's listen ports and the landing node each
	// one fronts, which is what the forward chain meters. It is a seam like the
	// two above.
	relayForwards func() []RelayForward
	// previous holds the last counter value read per set element, so a sample
	// can be turned into a delta.
	previous map[string]uint64
	// applied is the ruleset text the loaded table was built from. A wg-quick
	// restart hands the interface a new ephemeral port and the hub pushes or
	// withdraws relay jobs between samples; comparing the rendered ruleset
	// catches every such drift with one test, and rebuilds the table rather
	// than keep counting flows the configuration no longer describes.
	applied string
	loaded  bool
}

// NewIPAccounting returns a sampler, or nil when the host has no nft utility.
// relayForwards reports the relay's port mappings to meter on the forward path;
// nil stands for a node that never relays.
func NewIPAccounting(relayForwards func() []RelayForward) *IPAccounting {
	binary, err := exec.LookPath("nft")
	if err != nil {
		return nil
	}
	return &IPAccounting{
		run: func(ctx context.Context, stdin string, args ...string) ([]byte, error) {
			return runNFT(ctx, binary, stdin, args...)
		},
		overlayPorts:  wireGuardListenPorts,
		relayForwards: relayForwards,
		previous:      map[string]uint64{},
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

// Collect returns the traffic each accounting key moved since the previous
// call: the bare address for a direct peer, the relay-marked form naming the
// landing node for a client whose traffic this node forwarded. The ruleset is
// installed on first use and reinstalled if it disappears, so a firewall reload
// that flushes it self-heals on the next sample.
func (a *IPAccounting) Collect(ctx context.Context) (map[string]IPTrafficDelta, error) {
	if a == nil {
		return nil, nil
	}
	forwards := a.currentRelayForwards()
	if ruleset := ipAcctRuleset(a.currentOverlayPorts(ctx), relayForwardPorts(forwards)); !a.loaded || ruleset != a.applied {
		if err := a.apply(ctx, ruleset); err != nil {
			return nil, err
		}
	}
	// Read once per round rather than per element: the mapping is the same for
	// every counter in the round, and it is what turns a listen port back into
	// the landing node the bytes went to.
	landings := relayLandingsByPort(forwards)
	deltas := map[string]IPTrafficDelta{}
	// Rebuilt rather than updated in place: an element nftables evicted after
	// its idle timeout has to leave with it, or a node facing a long tail of
	// one-off client addresses grows this map for as long as it runs — past the
	// monitor unit's MemoryMax, given enough weeks. An address that comes back
	// is a new element counting from zero, which is exactly what a missing
	// previous value already reports.
	previous := make(map[string]uint64, len(a.previous))
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
		for _, counter := range counters {
			key := set.name + "|" + counter.element()
			delta := counterDelta(a.previous[key], counter.Bytes)
			previous[key] = counter.Bytes
			if delta == 0 {
				continue
			}
			// A port with no landing behind it is one the hub withdrew between
			// the ruleset load and this read. Its bytes are still relayed
			// traffic and are recorded as such, under the same key an older
			// release wrote, rather than dropped.
			accountKey := EncodeIPKey(counter.Address, landings[counter.Port], set.relayed)
			entry := deltas[accountKey]
			if set.inbound {
				entry.InBytes += delta
			} else {
				entry.OutBytes += delta
			}
			deltas[accountKey] = entry
		}
	}
	a.previous = previous
	return deltas, nil
}

// currentOverlayPorts reads the overlay's listen ports for this round.
func (a *IPAccounting) currentOverlayPorts(ctx context.Context) []int {
	if a.overlayPorts == nil {
		return nil
	}
	return a.overlayPorts(ctx)
}

// currentRelayForwards reads the relay's port mappings for this round.
func (a *IPAccounting) currentRelayForwards() []RelayForward {
	if a.relayForwards == nil {
		return nil
	}
	return a.relayForwards()
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

func (a *IPAccounting) apply(ctx context.Context, ruleset string) error {
	if _, err := a.run(ctx, ruleset, "-f", "-"); err != nil {
		return fmt.Errorf("install per-IP accounting ruleset: %w", err)
	}
	a.applied = ruleset
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

func (a *IPAccounting) readSet(ctx context.Context, name string) ([]nftCounter, error) {
	out, err := a.run(ctx, "", "-j", "list", "set", "inet", ipAcctTable, name)
	if err != nil {
		return nil, fmt.Errorf("read per-IP counter set %s: %w", name, err)
	}
	return parseNFTCounterSet(out)
}

// nftCounter is one element of a counter set. Port is zero for the peer sets,
// which are keyed by address alone; the relay sets concatenate the relay listen
// port onto the address, so their elements carry both.
type nftCounter struct {
	Address string
	Port    int
	Bytes   uint64
}

// element renders the set key this counter was read under, which is what the
// previous-value map has to be keyed by: two ports of the same address are two
// independent counters, and folding them onto one address would make each
// read subtract the other's total.
func (c nftCounter) element() string {
	if c.Port == 0 {
		return c.Address
	}
	return c.Address + "/" + strconv.Itoa(c.Port)
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

// nftCounterElement decodes one element. Val is left raw because a set keyed by
// one type prints it as a bare string while a concatenated set prints an object
// holding the parts, and both shapes appear in the same field.
type nftCounterElement struct {
	Elem *struct {
		Val     json.RawMessage `json:"val"`
		Counter *struct {
			Bytes uint64 `json:"bytes"`
		} `json:"counter"`
	} `json:"elem"`
}

// nftConcatValue is the object form of a concatenated key. The parts keep their
// own JSON types — an address stays a string, a port stays a number — so they
// are decoded as raw messages and read positionally.
type nftConcatValue struct {
	Concat []json.RawMessage `json:"concat"`
}

func parseNFTCounterSet(raw []byte) ([]nftCounter, error) {
	var listing nftSetListing
	if err := json.Unmarshal(raw, &listing); err != nil {
		return nil, fmt.Errorf("decode nft output: %w", err)
	}
	var counters []nftCounter
	for _, entry := range listing.Nftables {
		if entry.Set == nil {
			continue
		}
		for _, raw := range entry.Set.Elem {
			var element nftCounterElement
			if err := json.Unmarshal(raw, &element); err != nil {
				continue // a bare address string: no counter to read
			}
			if element.Elem == nil || element.Elem.Counter == nil {
				continue
			}
			counter, ok := parseNFTCounterKey(element.Elem.Val)
			if !ok {
				continue
			}
			counter.Bytes = element.Elem.Counter.Bytes
			counters = append(counters, counter)
		}
	}
	return counters, nil
}

// parseNFTCounterKey reads an element's key in either of the two shapes the
// sets produce: a bare address, or an address concatenated with the relay
// listen port the flow arrived on.
func parseNFTCounterKey(raw json.RawMessage) (nftCounter, bool) {
	var address string
	if err := json.Unmarshal(raw, &address); err == nil {
		return nftCounter{Address: address}, address != ""
	}
	var value nftConcatValue
	if err := json.Unmarshal(raw, &value); err != nil || len(value.Concat) != 2 {
		return nftCounter{}, false
	}
	if err := json.Unmarshal(value.Concat[0], &address); err != nil || address == "" {
		return nftCounter{}, false
	}
	var port int
	if err := json.Unmarshal(value.Concat[1], &port); err != nil || port < 1 || port > 65535 {
		return nftCounter{}, false
	}
	return nftCounter{Address: address, Port: port}, true
}
