// Package monitor implements the built-in monitor: a network-interface
// counter collector, a low-memory SQLite store, quota enforcement, and an HTTP
// API/UI server. Traffic is whole-VPS, derived from interface counters.
package monitor

import "time"

// TrafficLimits holds configured byte limits for a quota cycle.
type TrafficLimits struct {
	InBytes    uint64
	OutBytes   uint64
	TotalBytes uint64
}

// Exceeded reports whether any configured limit has been reached. A zero limit
// means unlimited for that direction.
func (l TrafficLimits) Exceeded(used TrafficTotals) bool {
	return limitExceeded(l.InBytes, used.InBytes) ||
		limitExceeded(l.OutBytes, used.OutBytes) ||
		limitExceeded(l.TotalBytes, used.Total())
}

func limitExceeded(limit, used uint64) bool {
	return limit > 0 && used >= limit
}

// Remaining returns bytes left for one limit, or 0 if over/unlimited-at-zero.
func Remaining(limit, used uint64) uint64 {
	if limit == 0 || used >= limit {
		return 0
	}
	return limit - used
}

// Delta returns current-previous, treating a counter reset (current < previous,
// e.g. after reboot) as zero rather than a huge wrap-around value.
func Delta(previous, current uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

// CycleStart returns the most recent GMT reset boundary at or before now for
// the given reset day-of-month and hour. If the day/hour hasn't occurred yet
// this month, it rolls back to the previous month. Days beyond a month's length
// clamp to its last day.
func CycleStart(now time.Time, resetDay int, resetHour ...int) time.Time {
	if resetDay < 1 {
		resetDay = 1
	}
	hour := resetBoundaryHour(resetHour...)
	now = now.UTC()
	year, month := now.Year(), now.Month()
	day := clampDay(year, month, resetDay)
	candidate := time.Date(year, month, day, hour, 0, 0, 0, time.UTC)
	if !candidate.After(now) {
		return candidate
	}
	// Roll back to previous month.
	prev := now.AddDate(0, -1, 0)
	day = clampDay(prev.Year(), prev.Month(), resetDay)
	return time.Date(prev.Year(), prev.Month(), day, hour, 0, 0, 0, time.UTC)
}

// NextCycleReset returns the next reset boundary strictly after now.
func NextCycleReset(now time.Time, resetDay int, resetHour ...int) time.Time {
	hour := resetBoundaryHour(resetHour...)
	start := CycleStart(now, resetDay, hour)
	next := start.AddDate(0, 1, 0)
	day := clampDay(next.Year(), next.Month(), resetDay)
	return time.Date(next.Year(), next.Month(), day, hour, 0, 0, 0, time.UTC)
}

func resetBoundaryHour(resetHour ...int) int {
	if len(resetHour) == 0 {
		return 0
	}
	hour := resetHour[0]
	if hour < 0 || hour > 23 {
		return 0
	}
	return hour
}

// clampDay limits day to the number of days in the given year/month.
func clampDay(year int, month time.Month, day int) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	return day
}

// ResourceSnapshot is the latest resource reading shown on the monitor card.
type ResourceSnapshot struct {
	CPUPct          float64 `json:"cpuPct"`
	MemPct          float64 `json:"memPct"`
	MemUsedBytes    uint64  `json:"memUsedBytes"`
	MemTotalBytes   uint64  `json:"memTotalBytes"`
	DiskUsagePct    float64 `json:"diskUsagePct"`
	DiskUsedBytes   uint64  `json:"diskUsedBytes"`
	DiskTotalBytes  uint64  `json:"diskTotalBytes"`
	DiskIOReadRate  float64 `json:"diskIOReadRate"`
	DiskIOWriteRate float64 `json:"diskIOWriteRate"`
}

// ResourceRawPoint is one raw resource sample (10-second interval).
type ResourceRawPoint struct {
	TS       int64   `json:"ts"`
	CPUPct   float64 `json:"cpuPct"`
	MemPct   float64 `json:"memPct"`
	DiskPct  float64 `json:"diskPct"`
	DIORead  int64   `json:"dioRead"`
	DIOWrite int64   `json:"dioWrite"`
}

// TrafficRawPoint is one raw traffic sample (per sampling interval).
type TrafficRawPoint struct {
	TS         int64 `json:"ts"`
	InBytes    int64 `json:"inBytes"`
	OutBytes   int64 `json:"outBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

// IPSeriesPoint is one remote address's traffic in one bucket, at whichever
// granularity the series was read at.
type IPSeriesPoint struct {
	TS         int64 `json:"ts"`
	InBytes    int64 `json:"inBytes"`
	OutBytes   int64 `json:"outBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

// IPTrafficWindow is one address's traffic over one of the ranking windows.
type IPTrafficWindow struct {
	InBytes    int64 `json:"inBytes"`
	OutBytes   int64 `json:"outBytes"`
	TotalBytes int64 `json:"totalBytes"`
}

func (w *IPTrafficWindow) total() { w.TotalBytes = w.InBytes + w.OutBytes }

// Add folds another node's figures for the same address into this window, which
// is what merging a fleet's lists into one ranking amounts to.
func (w *IPTrafficWindow) Add(other IPTrafficWindow) {
	w.InBytes += other.InBytes
	w.OutBytes += other.OutBytes
	w.TotalBytes += other.TotalBytes
}

// IPTrafficEntry is one remote address's traffic over each window the table
// sorts by. Every window carries all three directions so the dashboard can sort
// on any column without a second request.
type IPTrafficEntry struct {
	IP string `json:"ip"`
	// Relayed marks a client observed on the relay's forward path rather than
	// as a direct peer: this node carried its traffic to a landing node. The
	// same address can hold one entry of each kind.
	Relayed bool            `json:"relayed,omitempty"`
	Cycle   IPTrafficWindow `json:"cycle"`
	Today   IPTrafficWindow `json:"today"`
	Last7   IPTrafficWindow `json:"last7"`
}

// IPTrafficSnapshot is the payload behind /api/ip-traffic. Enabled is false on
// a host without nftables, which lets the dashboard say why the list is empty
// instead of implying the node saw no traffic.
type IPTrafficSnapshot struct {
	Enabled    bool             `json:"enabled"`
	CycleStart int64            `json:"cycleStart"`
	Entries    []IPTrafficEntry `json:"entries"`
}

// IPTrafficDetail is one address's history behind /api/ip-detail, at the same
// three granularities the node's own traffic modal offers. Relayed carries the
// same distinction the entry list makes: a relay-observed history is a series
// of its own, apart from the address's direct one.
type IPTrafficDetail struct {
	IP      string          `json:"ip"`
	Relayed bool            `json:"relayed,omitempty"`
	Recent  []IPSeriesPoint `json:"recent"`
	Hourly  []IPSeriesPoint `json:"hourly"`
	Daily   []IPSeriesPoint `json:"daily"`
}

// PingLatestPoint is one target's most recent probe.
type PingLatestPoint struct {
	Target  string   `json:"target"`
	TS      int64    `json:"ts"`
	AvgMS   *float64 `json:"avgMs"`
	LossPct float64  `json:"lossPct"`
}

// LatencySnapshot is the payload behind /api/ping-trend: the probe list this
// node samples and its newest reading per target. The target list travels with
// the data so the dashboard never has to hardcode it, and a node running an
// older probe list still describes itself correctly.
//
// This is what the latency page polls every minute, so it holds only what the
// page always shows. The history is a week of one-minute rounds and lives
// behind /api/ping-series, fetched once when someone opens a trend.
type LatencySnapshot struct {
	Targets []PingTarget      `json:"targets"`
	Latest  []PingLatestPoint `json:"latest"`
}

// PingSeries is the full latency history on a fixed grid: slot i of every track
// is the round at Start + i*Step, so a timestamp never has to be transmitted.
type PingSeries struct {
	Start  int64                 `json:"start"`
	Step   int64                 `json:"step"`
	Count  int                   `json:"count"`
	Series map[string]*PingTrack `json:"series"`
}

// PingTrack is one target's week. MS is null for a round that answered nothing
// and Loss is -1 for a slot with no round at all.
type PingTrack struct {
	MS   []*float64 `json:"ms"`
	Loss []float64  `json:"loss"`
}

// ResourceHourlyPoint is one aggregated hourly resource bucket with avg and max.
type ResourceHourlyPoint struct {
	HourTS      int64   `json:"hourTs"`
	CPUAvg      float64 `json:"cpuAvg"`
	CPUMax      float64 `json:"cpuMax"`
	MemAvg      float64 `json:"memAvg"`
	MemMax      float64 `json:"memMax"`
	DiskAvg     float64 `json:"diskAvg"`
	DiskMax     float64 `json:"diskMax"`
	DIOReadAvg  int64   `json:"dioReadAvg"`
	DIOReadMax  int64   `json:"dioReadMax"`
	DIOWriteAvg int64   `json:"dioWriteAvg"`
	DIOWriteMax int64   `json:"dioWriteMax"`
}
