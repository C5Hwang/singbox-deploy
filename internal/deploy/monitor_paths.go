package deploy

import (
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// RemoteMonitorPath returns the on-disk JSON snapshot file the master's
// monitor uses to surface aggregated samples from cluster nodes. Written by
// the periodic RefreshRemoteSources callback and read by the monitor server
// when assembling /api/summary.
func RemoteMonitorPath(layout paths.Layout) string {
	return filepath.Join(layout.StateDir, "remote_monitor.json")
}
