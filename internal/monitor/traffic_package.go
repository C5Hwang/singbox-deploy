package monitor

import (
	"time"
)

// The functions here read and move one quota cycle's figures — its sampled
// usage and its traffic package — for a process that is not the sampler: the
// hub's TUI writing beside the monitor service, or a spoke's agent whose
// monitor is switched off. The running Monitor uses the same functions under
// its own mutex, so every path records a change the same way.

// ReadCurrentTrafficUsage returns the usage and package of the cycle that
// contains now.
func ReadCurrentTrafficUsage(store *Store, now time.Time, resetDay, resetHour int) (TrafficUsage, error) {
	cycleStart := CycleStart(now, resetDay, resetHour)
	totals, err := store.TotalsSince(cycleStart.Unix())
	if err != nil {
		return TrafficUsage{}, err
	}
	pkg, err := store.PackageSince(cycleStart.Unix())
	if err != nil {
		return TrafficUsage{}, err
	}
	return TrafficUsage{Totals: totals, Package: pkg, CycleStart: cycleStart}, nil
}

// ReplaceCurrentTrafficUsage sets the cycle's usage to target and, when pkg is
// given, its package to pkg. It refuses with ErrTrafficCycleChanged when the
// cycle is no longer the one the caller read, so a form filled in before a
// reset can never seed the new month with the old one's figures.
func ReplaceCurrentTrafficUsage(
	store *Store, now time.Time, resetDay, resetHour int,
	expectedCycleStart int64, target TrafficTotals, pkg *TrafficPackage,
) (TrafficUsageUpdate, error) {
	cycleStart := CycleStart(now, resetDay, resetHour)
	if cycleStart.Unix() != expectedCycleStart {
		return TrafficUsageUpdate{}, ErrTrafficCycleChanged
	}
	previous, err := store.ReplaceTotalsSince(cycleStart.Unix(), now.Unix(), target)
	if err != nil {
		return TrafficUsageUpdate{}, err
	}
	previousPkg, err := store.PackageSince(cycleStart.Unix())
	if err != nil {
		return TrafficUsageUpdate{}, err
	}
	appliedPkg := previousPkg
	if pkg != nil {
		if previousPkg, err = store.ReplacePackageSince(cycleStart.Unix(), now.Unix(), *pkg); err != nil {
			return TrafficUsageUpdate{}, err
		}
		appliedPkg = *pkg
	}
	return TrafficUsageUpdate{
		Previous: TrafficUsage{Totals: previous, Package: previousPkg, CycleStart: cycleStart},
		Applied:  TrafficUsage{Totals: target, Package: appliedPkg, CycleStart: cycleStart},
	}, nil
}

// GrantCurrentTrafficPackage adds delta to the cycle's package, leaving the
// sampled usage untouched. The cycle check is the same as for a replacement:
// a package bought for this month must not land in the next one.
func GrantCurrentTrafficPackage(
	store *Store, now time.Time, resetDay, resetHour int,
	expectedCycleStart int64, delta TrafficPackage,
) (TrafficUsageUpdate, error) {
	cycleStart := CycleStart(now, resetDay, resetHour)
	if cycleStart.Unix() != expectedCycleStart {
		return TrafficUsageUpdate{}, ErrTrafficCycleChanged
	}
	totals, err := store.TotalsSince(cycleStart.Unix())
	if err != nil {
		return TrafficUsageUpdate{}, err
	}
	previous, err := store.PackageSince(cycleStart.Unix())
	if err != nil {
		return TrafficUsageUpdate{}, err
	}
	granted, err := store.AddPackageSince(cycleStart.Unix(), now.Unix(), delta)
	if err != nil {
		return TrafficUsageUpdate{}, err
	}
	return TrafficUsageUpdate{
		Previous: TrafficUsage{Totals: totals, Package: previous, CycleStart: cycleStart},
		Applied:  TrafficUsage{Totals: totals, Package: granted, CycleStart: cycleStart},
	}, nil
}
