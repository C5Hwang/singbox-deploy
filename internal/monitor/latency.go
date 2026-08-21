package monitor

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	// PingInterval is how often every latency target is probed.
	PingInterval = 1 * time.Minute
	// pingCount is the number of connects averaged into one sample. Five keeps
	// the loss figure to twenty-point steps within a round while staying cheap
	// enough to repeat every minute: a connect is a SYN, a SYN-ACK and a reset.
	pingCount = 5
	// pingSpacing separates the connects inside one round, so a sample measures
	// the route over a moment rather than one instant of it.
	pingSpacing = 200 * time.Millisecond
	// pingConnectTimeout is how long one connect may take before it counts as
	// lost. Chinese carriers answer a nearby probe in well under 400 ms, so two
	// seconds is a generous ceiling that still fails fast on a black hole.
	pingConnectTimeout = 2 * time.Second
	// pingRoundDeadline bounds a whole round from the outside. Five connects
	// spaced by pingSpacing, each capped at pingConnectTimeout, cannot exceed
	// about eleven seconds; the rest is headroom for a slow resolver.
	pingRoundDeadline = 20 * time.Second
	// pingRetention bounds the latency history at a week of one-minute samples,
	// and that is the only shape it is ever read in. Nine targets at one round a
	// minute is 90,720 rows a week — about 3.4 MB, small enough that folding them
	// into hours would save little and cost the detail the chart is for.
	pingRetention = 7 * 24 * time.Hour
)

// PingTarget is one latency probe destination, addressed as host:port.
//
// The fixed list below is a carrier's node in one city. A target contributed at
// runtime — one landing node this host relays for — carries Kind and Name
// instead, so the dashboard can tell the two apart and give them their own
// panels without either side hardcoding the other's list.
type PingTarget struct {
	ID      string `json:"id"`
	Carrier string `json:"carrier,omitempty"`
	City    string `json:"city,omitempty"`
	Address string `json:"address"`
	// Kind is empty for the carrier probes and names the group a runtime
	// target belongs to.
	Kind string `json:"kind,omitempty"`
	// Name labels a runtime target, which has no carrier or city to name it by.
	Name string `json:"name,omitempty"`
}

// DefaultPingTargets is the probe list: three carriers by three cities.
//
// The hostnames come from the Zstatic CDN node catalogue, which publishes one
// name per carrier per region for exactly this purpose. They are probed by TCP
// connect rather than by ICMP: the catalogue addresses them as host:80, and a
// carrier that rate-limits or deprioritizes ICMP still answers a SYN the same
// way a real client's connection is answered.
//
// The names are resolved on every round rather than pinned to an address, so a
// node the CDN moves is followed without a release.
var DefaultPingTargets = []PingTarget{
	{ID: "telecom-beijing", Carrier: "China Telecom", City: "Beijing", Address: "bj-ct-v4.ip.zstaticcdn.com:80"},
	{ID: "telecom-shanghai", Carrier: "China Telecom", City: "Shanghai", Address: "sh-ct-v4.ip.zstaticcdn.com:80"},
	{ID: "telecom-guangzhou", Carrier: "China Telecom", City: "Guangzhou", Address: "gd-guangzhou-ct-v4.ip.zstaticcdn.com:80"},
	{ID: "unicom-beijing", Carrier: "China Unicom", City: "Beijing", Address: "bj-cu-v4.ip.zstaticcdn.com:80"},
	{ID: "unicom-shanghai", Carrier: "China Unicom", City: "Shanghai", Address: "sh-cu-v4.ip.zstaticcdn.com:80"},
	{ID: "unicom-guangzhou", Carrier: "China Unicom", City: "Guangzhou", Address: "gd-guangzhou-cu-v4.ip.zstaticcdn.com:80"},
	{ID: "mobile-beijing", Carrier: "China Mobile", City: "Beijing", Address: "bj-cm-v4.ip.zstaticcdn.com:80"},
	{ID: "mobile-shanghai", Carrier: "China Mobile", City: "Shanghai", Address: "sh-cm-v4.ip.zstaticcdn.com:80"},
	{ID: "mobile-guangzhou", Carrier: "China Mobile", City: "Guangzhou", Address: "gd-guangzhou-cm-v4.ip.zstaticcdn.com:80"},
}

// RelayPingTargetPrefix namespaces a relay's probe of one landing node, keeping
// it clear of the carrier target IDs above. It is exported because the relay
// package mints those IDs and the store tells the two families apart by it.
const RelayPingTargetPrefix = "relay:"

// relayPingTargetPattern is RelayPingTargetPrefix as a LIKE pattern. The
// prefix's own characters are escaped rather than assumed inert, so a prefix
// that ever grows a wildcard cannot silently widen a delete.
var relayPingTargetPattern = escapeLikePrefix(RelayPingTargetPrefix) + "%"

func escapeLikePrefix(prefix string) string {
	var b strings.Builder
	for _, r := range prefix {
		if r == '%' || r == '_' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// PingSample is one probe outcome. AvgMS is nil when every connect failed,
// which the dashboard draws as a gap rather than as zero latency.
type PingSample struct {
	AvgMS   *float64
	LossPct float64
}

// PingCollector measures TCP connect latency to the probe targets.
type PingCollector struct {
	targets []PingTarget
	// extra contributes destinations that come and go while the monitor runs,
	// so a relay link added or withdrawn takes effect on the next round rather
	// than on the next restart. nil means the fixed list is all there is.
	extra func() []PingTarget
	// probe is the test seam. Production opens real connections.
	probe func(context.Context, string) (PingSample, error)
}

// NewPingCollector returns a collector for the default targets. Probing is a TCP
// connect from inside this process, so it needs no external utility and no raw
// socket and is never nil.
func NewPingCollector() *PingCollector {
	return &PingCollector{targets: DefaultPingTargets, probe: tcpPing}
}

// newPingCollectorWithExtra returns a collector that also samples whatever
// extra contributes on each round.
func newPingCollectorWithExtra(extra func() []PingTarget) *PingCollector {
	c := NewPingCollector()
	c.extra = extra
	return c
}

// Targets returns the probe list for this moment: the fixed carrier list plus
// whatever the runtime contributes. A duplicate ID is dropped, because two
// entries for one target would each overwrite the other's sample.
func (c *PingCollector) Targets() []PingTarget {
	if c == nil {
		return nil
	}
	if c.extra == nil {
		return c.targets
	}
	seen := make(map[string]struct{}, len(c.targets))
	targets := make([]PingTarget, 0, len(c.targets))
	for _, target := range append(append([]PingTarget(nil), c.targets...), c.extra()...) {
		if target.ID == "" || target.Address == "" {
			continue
		}
		if _, duplicate := seen[target.ID]; duplicate {
			continue
		}
		seen[target.ID] = struct{}{}
		targets = append(targets, target)
	}
	return targets
}

// Collect probes every target. The probes run concurrently so one unreachable
// target cannot stretch a round past the sampling interval; results come back
// in target order. A target that could not be probed at all is omitted, which
// is different from one that answered nothing (100% loss).
func (c *PingCollector) Collect(ctx context.Context) map[string]PingSample {
	if c == nil {
		return nil
	}
	// The list is read once per round rather than per probe, so every sample in
	// a round describes the same set of targets.
	targets := c.Targets()
	samples := make([]*PingSample, len(targets))
	var wg sync.WaitGroup
	for i, target := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sample, err := c.probe(ctx, target.Address)
			if err != nil {
				return
			}
			samples[i] = &sample
		}()
	}
	wg.Wait()
	out := make(map[string]PingSample, len(targets))
	for i, target := range targets {
		if samples[i] != nil {
			out[target.ID] = *samples[i]
		}
	}
	return out
}

// tcpPing times pingCount connects to address and averages the ones that
// answered. The name is resolved once per round and the connects go to the
// resolved address, so a slow resolver is not billed to the route; a name that
// does not resolve at all is an error rather than a sample, which leaves the
// target's history untouched instead of recording a route failure.
func tcpPing(ctx context.Context, address string) (PingSample, error) {
	ctx, cancel := context.WithTimeout(ctx, pingRoundDeadline)
	defer cancel()
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return PingSample{}, err
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip4", host)
	if err != nil {
		return PingSample{}, err
	}
	if len(addrs) == 0 {
		return PingSample{}, &net.DNSError{Err: "no address", Name: host}
	}
	target := net.JoinHostPort(addrs[0].String(), port)

	var (
		total     time.Duration
		answered  int
		attempted int
	)
	for i := 0; i < pingCount; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				// The round ran out of time. Report what was measured rather
				// than counting the connects never attempted as losses.
				return pingSampleFrom(total, answered, attempted), nil
			case <-time.After(pingSpacing):
			}
		}
		attempted++
		if elapsed, ok := connectOnce(ctx, target); ok {
			total += elapsed
			answered++
		}
	}
	return pingSampleFrom(total, answered, attempted), nil
}

func connectOnce(ctx context.Context, target string) (time.Duration, bool) {
	dialer := net.Dialer{Timeout: pingConnectTimeout}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return 0, false
	}
	elapsed := time.Since(start)
	// Close with a reset rather than a FIN: the probe has nothing to shut down
	// gracefully, and a round every minute would otherwise leave a steady
	// population of sockets in TIME_WAIT.
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetLinger(0)
	}
	conn.Close()
	return elapsed, true
}

func pingSampleFrom(total time.Duration, answered, attempted int) PingSample {
	if attempted == 0 {
		return PingSample{LossPct: 100}
	}
	sample := PingSample{LossPct: float64(attempted-answered) / float64(attempted) * 100}
	if answered > 0 {
		avg := float64(total.Nanoseconds()) / float64(answered) / 1e6
		sample.AvgMS = &avg
	}
	return sample
}
