// Package monitor implements the built-in traffic monitor: a network-interface
// counter collector, a low-memory SQLite store, quota enforcement, and an HTTP
// API/UI server. Traffic is whole-VPS, derived from interface counters.
package monitor

import "time"

// Quota holds the configured byte limit and current cycle usage.
type Quota struct {
	LimitBytes uint64
	UsedBytes  uint64
	// ResetDay is the day-of-month (1-31) the cycle resets.
	ResetDay int
}

// Exceeded reports whether usage has reached the limit. A zero limit means
// unlimited.
func (q Quota) Exceeded() bool {
	return q.LimitBytes > 0 && q.UsedBytes >= q.LimitBytes
}

// Remaining returns bytes left in the cycle, or 0 if over/unlimited-at-zero.
func (q Quota) Remaining() uint64 {
	if q.LimitBytes == 0 || q.UsedBytes >= q.LimitBytes {
		return 0
	}
	return q.LimitBytes - q.UsedBytes
}

// Delta returns current-previous, treating a counter reset (current < previous,
// e.g. after reboot) as zero rather than a huge wrap-around value.
func Delta(previous, current uint64) uint64 {
	if current < previous {
		return 0
	}
	return current - previous
}

// CycleStart returns the most recent reset boundary at or before now for the
// given reset day-of-month. If the day hasn't occurred yet this month, it rolls
// back to the previous month. Days beyond a month's length clamp to its last
// day.
func CycleStart(now time.Time, resetDay int) time.Time {
	if resetDay < 1 {
		resetDay = 1
	}
	year, month := now.Year(), now.Month()
	day := clampDay(year, month, resetDay)
	candidate := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	if !candidate.After(now) {
		return candidate
	}
	// Roll back to previous month.
	prev := now.AddDate(0, -1, 0)
	day = clampDay(prev.Year(), prev.Month(), resetDay)
	return time.Date(prev.Year(), prev.Month(), day, 0, 0, 0, 0, now.Location())
}

// NextCycleReset returns the next reset boundary strictly after now.
func NextCycleReset(now time.Time, resetDay int) time.Time {
	start := CycleStart(now, resetDay)
	next := start.AddDate(0, 1, 0)
	day := clampDay(next.Year(), next.Month(), resetDay)
	return time.Date(next.Year(), next.Month(), day, 0, 0, 0, 0, now.Location())
}

// clampDay limits day to the number of days in the given year/month.
func clampDay(year int, month time.Month, day int) int {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		return last
	}
	return day
}
