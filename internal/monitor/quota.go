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
