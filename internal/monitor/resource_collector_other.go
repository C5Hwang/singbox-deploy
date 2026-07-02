//go:build !linux

package monitor

// ResourceReading holds computed resource metrics from a single collection.
// Keep the fields in sync with the linux implementation so the package still
// compiles on development hosts.
type ResourceReading struct {
	CPUPct         float64
	MemPct         float64
	MemUsedBytes   uint64
	MemTotalBytes  uint64
	DiskUsedPct    float64
	DiskUsedBytes  uint64
	DiskTotalBytes uint64
	DIOReadDelta   uint64
	DIOWriteDelta  uint64
	Valid          bool
}

// ResourceCollector is a no-op on non-Linux platforms.
type ResourceCollector struct{}

// NewResourceCollector returns a stub collector.
func NewResourceCollector(_ string) *ResourceCollector { return &ResourceCollector{} }

// Collect returns zero metrics on non-Linux. Valid is always false.
func (rc *ResourceCollector) Collect() (ResourceReading, error) {
	return ResourceReading{}, nil
}
