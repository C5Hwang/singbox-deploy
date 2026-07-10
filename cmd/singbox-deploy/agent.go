package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/C5Hwang/singbox-deploy/assets/agentbin"
)

// runAgentAsset exports an architecture-matched, TUI-less spoke agent from the
// hub binary. It is used by SSH bootstrap and by the old hub process during a
// transactional self-update; no GitHub download is needed on a spoke.
func runAgentAsset(args []string) error {
	if len(args) == 0 || args[0] != "export" {
		return flag.ErrHelp
	}
	fs := flag.NewFlagSet("agent export", flag.ContinueOnError)
	arch := fs.String("arch", "", "agent architecture (amd64 or arm64)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *arch == "" {
		return fmt.Errorf("--arch is required")
	}
	binary, err := agentbin.Binary(*arch)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(binary)
	return err
}
