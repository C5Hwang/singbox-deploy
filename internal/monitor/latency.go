package monitor

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// PingInterval is how often every latency target is probed.
	PingInterval = 5 * time.Minute
	// pingCount is the number of echo requests averaged into one sample.
	pingCount = 10
	// pingRetention bounds the latency history. Older samples are dropped
	// wholesale rather than folded: a week of five-minute samples is already
	// small, and hourly averaging happens at read time.
	pingRetention = 7 * 24 * time.Hour
	// pingRunSeconds is ping's own deadline. Ten requests spaced by
	// pingIntervalArg need about three seconds; the rest is headroom for a slow
	// path. pingDeadline is the backstop for a ping that ignores its deadline,
	// and stays comfortably above it: killing ping before it prints its summary
	// would throw away a perfectly good 100%-loss reading.
	pingRunSeconds  = 15
	pingDeadline    = 25 * time.Second
	pingIntervalArg = "0.3"
	pingWaitArg     = "2"
)

// PingTarget is one fixed latency probe destination. The nine targets are the
// provincial carrier resolvers Chinese network tests conventionally use: they
// answer ICMP reliably and sit on each carrier's own backbone, so the numbers
// describe the route rather than the responder.
type PingTarget struct {
	ID      string `json:"id"`
	Carrier string `json:"carrier"`
	City    string `json:"city"`
	Address string `json:"address"`
}

// DefaultPingTargets is the probe list: three carriers by three cities.
var DefaultPingTargets = []PingTarget{
	{ID: "telecom-beijing", Carrier: "China Telecom", City: "Beijing", Address: "219.141.136.10"},
	{ID: "telecom-shanghai", Carrier: "China Telecom", City: "Shanghai", Address: "202.96.209.133"},
	{ID: "telecom-guangzhou", Carrier: "China Telecom", City: "Guangzhou", Address: "202.96.128.86"},
	{ID: "unicom-beijing", Carrier: "China Unicom", City: "Beijing", Address: "202.106.195.68"},
	{ID: "unicom-shanghai", Carrier: "China Unicom", City: "Shanghai", Address: "210.22.70.3"},
	{ID: "unicom-guangzhou", Carrier: "China Unicom", City: "Guangzhou", Address: "210.21.196.6"},
	{ID: "mobile-beijing", Carrier: "China Mobile", City: "Beijing", Address: "221.130.33.60"},
	{ID: "mobile-shanghai", Carrier: "China Mobile", City: "Shanghai", Address: "211.136.112.50"},
	{ID: "mobile-guangzhou", Carrier: "China Mobile", City: "Guangzhou", Address: "211.136.192.6"},
}

// PingSample is one probe outcome. AvgMS is nil when every request was lost,
// which the dashboard draws as a gap rather than as zero latency.
type PingSample struct {
	AvgMS   *float64
	LossPct float64
}

// PingCollector probes the latency targets with the system ping utility.
type PingCollector struct {
	targets []PingTarget
	// probe is the test seam. Production shells out to ping(8), which is the
	// only ICMP path that works without the binary holding raw-socket rights.
	probe func(context.Context, string) (PingSample, error)
}

// NewPingCollector returns a collector for the default targets, or nil when the
// host has no ping utility. A nil collector disables latency sampling and
// leaves every other metric untouched.
func NewPingCollector() *PingCollector {
	binary, err := exec.LookPath("ping")
	if err != nil {
		return nil
	}
	return &PingCollector{
		targets: DefaultPingTargets,
		probe:   func(ctx context.Context, address string) (PingSample, error) { return systemPing(ctx, binary, address) },
	}
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

// systemPing runs one probe and parses the summary ping(8) prints. A run where
// every request was lost exits non-zero, which is a valid sample rather than an
// error; only a failure to run or an unparsable summary is an error.
func systemPing(ctx context.Context, binary, address string) (PingSample, error) {
	ctx, cancel := context.WithTimeout(ctx, pingDeadline)
	defer cancel()
	out, err := exec.CommandContext(ctx, binary,
		"-n", "-q",
		"-c", strconv.Itoa(pingCount),
		"-i", pingIntervalArg,
		"-W", pingWaitArg,
		"-w", strconv.Itoa(pingRunSeconds),
		address,
	).Output()
	sample, parseErr := parsePingOutput(string(out))
	if parseErr != nil {
		if err != nil {
			return PingSample{}, fmt.Errorf("ping %s: %w", address, err)
		}
		return PingSample{}, fmt.Errorf("ping %s: %w", address, parseErr)
	}
	return sample, nil
}

// parsePingOutput reads the transmitted/received counts and the rtt summary.
// Loss is recomputed from the counts rather than read from the percentage,
// which some implementations round.
func parsePingOutput(out string) (PingSample, error) {
	var (
		sample      PingSample
		haveCounts  bool
		transmitted int
		received    int
	)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "packets transmitted"):
			transmitted, received, haveCounts = parsePingCounts(line)
		case strings.HasPrefix(line, "rtt "), strings.HasPrefix(line, "round-trip "):
			if avg, ok := parsePingAverage(line); ok {
				sample.AvgMS = &avg
			}
		}
	}
	if !haveCounts || transmitted <= 0 {
		return PingSample{}, fmt.Errorf("no packet statistics in ping output")
	}
	if received <= 0 {
		// Every request was lost: there is no rtt line to read, and reporting a
		// latency of zero here would read as a perfect link.
		sample.AvgMS = nil
	}
	sample.LossPct = float64(transmitted-received) / float64(transmitted) * 100
	return sample, nil
}

// parsePingCounts reads "10 packets transmitted, 8 received, ...". BusyBox
// spells the second count "8 packets received", so each keyword takes the
// nearest integer before it rather than a fixed field position.
func parsePingCounts(line string) (transmitted, received int, ok bool) {
	preceding := -1
	for _, field := range strings.Fields(strings.ReplaceAll(line, ",", " ")) {
		if value, err := strconv.Atoi(field); err == nil {
			preceding = value
			continue
		}
		if preceding < 0 {
			continue
		}
		switch field {
		case "transmitted":
			transmitted, ok = preceding, true
		case "received":
			received = preceding
		}
	}
	return transmitted, received, ok
}

// parsePingAverage reads the second field of "min/avg/max/mdev = a/b/c/d ms".
func parsePingAverage(line string) (float64, bool) {
	_, values, found := strings.Cut(line, "=")
	if !found {
		return 0, false
	}
	fields := strings.Fields(values)
	if len(fields) == 0 {
		return 0, false
	}
	parts := strings.Split(fields[0], "/")
	if len(parts) < 2 {
		return 0, false
	}
	avg, err := strconv.ParseFloat(parts[1], 64)
	if err != nil {
		return 0, false
	}
	return avg, true
}
