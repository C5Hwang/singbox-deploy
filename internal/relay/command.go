package relay

import (
	"context"
	"fmt"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// Command dispatches the "relay" subcommand both the hub and the agent binary
// expose. "apply" is what the boot-time unit runs; "clear" is the manual escape
// hatch for withdrawing the data plane on a node the hub can no longer reach.
//
// bin names the binary the unit should reapply with, which differs between the
// hub and a spoke.
func Command(ctx context.Context, args []string, bin string) error {
	applier := &Applier{Bin: bin, Firewall: system.DetectFirewall()}
	if len(args) != 1 {
		return usage()
	}
	switch args[0] {
	case "apply":
		return applier.Reapply(ctx)
	case "clear":
		return applier.Clear(ctx)
	default:
		return usage()
	}
}

func usage() error { return fmt.Errorf("usage: relay apply|clear") }
