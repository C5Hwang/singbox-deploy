//go:build !linux

package monitor

// ResourceReading holds computed resource metrics from a single collection.
type ResourceReading struct {
	CPUPct        float64
	MemPct        float64
	DiskUsedPct   float64
	DIOReadDelta  uint64
	DIOWriteDelta uint64
	Valid         bool
}

// ResourceCollector is a no-op on non-Linux platforms.
type ResourceCollector struct{}

// NewResourceCollector returns a stub collector.
func NewResourceCollector(_ string) *ResourceCollector { return &ResourceCollector{} }

// Collect returns zero metrics on non-Linux. Valid is always false.
func (rc *ResourceCollector) Collect() (ResourceReading, error) {
	return ResourceReading{}, nil
}
