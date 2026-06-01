package main

import "errors"

// runMonitor dispatches the "monitor" subcommand. The real implementation is
// wired in the traffic-monitor task; this stub keeps the build green until then.
func runMonitor(args []string) error {
	return errors.New("monitor service not yet wired")
}
