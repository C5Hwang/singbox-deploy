package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/ui"
)

var version = "dev"

func main() {
	if printVersion(os.Args, os.Stdout) {
		return
	}
	ui.SetVersion(version)
	if shouldMigrateHubSubscriptions(os.Args) {
		if err := certmgr.SeedLegacyCredentials(paths.DefaultLayout()); err != nil {
			// Keep the control plane available and retry from the next process or
			// certificate operation; the migration marker advances only on success.
			fmt.Fprintln(os.Stderr, "warning: migrate certificate state:", err)
		}
		if err := deploy.RemoveLegacySubscribeToken(paths.DefaultLayout()); err != nil {
			// A leftover token file changes nothing about what is published; the
			// cleanup is idempotent and retried by the next process.
			fmt.Fprintln(os.Stderr, "warning: remove legacy subscription token:", err)
		}
		if err := seedSubscriptionGroups(paths.DefaultLayout(), version); err != nil {
			// Publishing continues from the previously generated files; the next
			// process or subscription operation retries the seed.
			fmt.Fprintln(os.Stderr, "warning: seed subscription groups:", err)
		}
		migrationCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		_, err := migrateHubSubscriptions(migrationCtx, paths.DefaultLayout(), version)
		cancel()
		if err != nil {
			// Leave the marker absent so the next Hub process retries. A stale
			// subscription must not prevent an otherwise healthy control plane or
			// monitor service from starting.
			fmt.Fprintln(os.Stderr, "warning: migrate subscriptions:", err)
		}
	}
	// The monitor subcommand runs the long-lived monitor service and is
	// dispatched before the interactive UI. It is wired in the monitor task.
	if len(os.Args) > 1 && os.Args[1] == "monitor" {
		if err := runMonitor(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "monitor:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "cert" {
		if err := runCert(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "cert:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "agent" {
		if err := runAgentAsset(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "agent:", err)
			os.Exit(1)
		}
		return
	}

	p := tea.NewProgram(ui.NewModel(), tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func printVersion(args []string, out io.Writer) bool {
	if len(args) != 2 || args[1] != "--version" {
		return false
	}
	fmt.Fprintln(out, version)
	return true
}
