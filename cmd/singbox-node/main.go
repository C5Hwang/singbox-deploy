// singbox-node is the persistent agent that runs on every remote node. It
// serves the master-facing HTTP API over the WireGuard interface so the
// master can update configs, push renewed certificates, upgrade binaries,
// and orchestrate teardown without re-using the initial SSH credentials.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/C5Hwang/singbox-deploy/internal/node"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "singbox-node:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return flag.ErrHelp
	}
	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "setup":
		return runSetup(args[1:])
	case "version":
		fmt.Println(version)
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: singbox-node <command>

Commands:
  serve     Start the persistent agent HTTP service on the WireGuard interface.
  setup     One-shot bootstrap (initial provisioning).
  version   Print the agent version.`)
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	layout := paths.DefaultLayout()
	state, err := node.LoadAgentState(layout)
	if err != nil {
		return err
	}
	srv := node.NewServer(layout, state, system.NewExecRunner(os.Stdout), version)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(os.Stderr, "singbox-node listening on %s:%d\n", state.WGIP, node.APIPort)
	return srv.ListenAndServe(ctx)
}

// runSetup is the on-node initial bootstrap. The master invokes this over SSH
// during initial provisioning to write agent state and start the agent unit.
// Detailed implementation lives in the cluster package; this entry point just
// reads flags and forwards.
func runSetup(args []string) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	apiToken := fs.String("api-token", "", "shared bearer token used by the master")
	wgIP := fs.String("wg-ip", "", "WireGuard IP this node was assigned")
	masterPub := fs.String("master-public-key", "", "master's WireGuard public key")
	masterEndpoint := fs.String("master-endpoint", "", "master public host:port for WireGuard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *apiToken == "" || *wgIP == "" {
		return fmt.Errorf("--api-token and --wg-ip are required")
	}
	layout := paths.DefaultLayout()
	state := node.AgentState{
		APIToken:        *apiToken,
		WGIP:            *wgIP,
		MasterPublicKey: *masterPub,
		MasterEndpoint:  *masterEndpoint,
	}
	if err := node.SaveAgentState(layout, state); err != nil {
		return fmt.Errorf("save agent state: %w", err)
	}
	fmt.Fprintln(os.Stdout, "singbox-node: agent state written")
	return nil
}
