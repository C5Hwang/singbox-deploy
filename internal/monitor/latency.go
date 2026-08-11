package monitor

import (
	"context"
	"net"
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

// PingTarget is one latency probe destination: a carrier's node in one city,
// addressed as host:port.
type PingTarget struct {
	ID      string `json:"id"`
	Carrier string `json:"carrier"`
	City    string `json:"city"`
	Address string `json:"address"`
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

// PingSample is one probe outcome. AvgMS is nil when every connect failed,
// which the dashboard draws as a gap rather than as zero latency.
type PingSample struct {
	AvgMS   *float64
	LossPct float64
}

// PingCollector measures TCP connect latency to the probe targets.
type PingCollector struct {
	targets []PingTarget
	// probe is the test seam. Production opens real connections.
	probe func(context.Context, string) (PingSample, error)
}

// NewPingCollector returns a collector for the default targets. Unlike the ICMP
// implementation it replaces, this one needs no external utility and no raw
// socket, so it is never nil.
func NewPingCollector() *PingCollector {
	return &PingCollector{targets: DefaultPingTargets, probe: tcpPing}
}

// Targets returns the probe list this collector samples.
func (c *PingCollector) Targets() []PingTarget {
	if c == nil {
		return nil
	}
	return c.targets
}

// Collect probes every target. The probes run concurrently so one unreachable
// target cannot stretch a round past the sampling interval; results come back
// in target order. A target that could not be probed at all is omitted, which
// is different from one that answered nothing (100% loss).
func (c *PingCollector) Collect(ctx context.Context) map[string]PingSample {
	if c == nil {
		return nil
	}
	samples := make([]*PingSample, len(c.targets))
	var wg sync.WaitGroup
	for i, target := range c.targets {
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
	out := make(map[string]PingSample, len(c.targets))
	for i, target := range c.targets {
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
