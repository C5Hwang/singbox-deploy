package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/certrenew"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func runCert(args []string) error {
	if len(args) == 0 || args[0] != "renew" {
		return flag.ErrHelp
	}

	fs := flag.NewFlagSet("cert renew", flag.ContinueOnError)
	thresholdDays := fs.Int("threshold-days", int(certrenew.DefaultRenewBefore/(24*time.Hour)), "renew when certificate expires within this many days")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *thresholdDays <= 0 {
		return fmt.Errorf("threshold-days must be > 0")
	}

	layout := paths.DefaultLayout()
	issuer := acme.NewLegoIssuer()
	issuer.Output = os.Stdout
	issuer.AccountKeyPath = acme.AccountKeyPathFor(layout.StateDir)
	r := certrenew.Renewer{
		Layout:      layout,
		ACME:        acme.NewManager(issuer),
		Runner:      system.NewExecRunner(os.Stdout),
		RenewBefore: time.Duration(*thresholdDays) * 24 * time.Hour,
		Output:      os.Stdout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return r.Run(ctx)
}
