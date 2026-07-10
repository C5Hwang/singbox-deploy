package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/certrenew"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func runCert(args []string) error {
	if len(args) == 0 || args[0] != "renew" {
		return flag.ErrHelp
	}

	fs := flag.NewFlagSet("cert renew", flag.ContinueOnError)
	thresholdDays := fs.Int("threshold-days", int(certrenew.DefaultRenewBefore/(24*time.Hour)), "renew when certificate expires within this many days")
	force := fs.Bool("force", false, "force immediate DNS-01 reissuance, ignoring the expiry threshold")
	domain := fs.String("domain", "", "limit --force to one managed domain")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *thresholdDays <= 0 {
		return fmt.Errorf("threshold-days must be > 0")
	}

	layout := paths.DefaultLayout()
	manager := &certmgr.Manager{Layout: layout, Output: os.Stdout}
	ctrl := &hubctl.Controller{Layout: layout, Runner: system.NewExecRunner(os.Stdout), ExpectedVersion: version}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if *force {
		return forceRenew(ctx, layout, manager, ctrl, *domain)
	}
	if *domain != "" {
		return fmt.Errorf("--domain requires --force")
	}
	r := certrenew.Renewer{
		Layout:      layout,
		Manager:     manager,
		Runner:      system.NewExecRunner(os.Stdout),
		RenewBefore: time.Duration(*thresholdDays) * 24 * time.Hour,
		Output:      os.Stdout,
		AfterRenew: func(domain string) error {
			return ctrl.DistributeCertificate(ctx, domain, os.Stdout)
		},
	}
	pendingErr := ctrl.RetryPendingCertificates(ctx, os.Stdout)
	renewErr := r.Run(ctx)
	return errors.Join(pendingErr, renewErr)
}

func forceRenew(ctx context.Context, layout paths.Layout, manager *certmgr.Manager, ctrl *hubctl.Controller, onlyDomain string) error {
	if err := certmgr.SeedLegacyCredentials(layout); err != nil {
		return err
	}
	type target struct{ domain, email string }
	var targets []target
	if onlyDomain != "" {
		targets = append(targets, target{domain: onlyDomain})
	} else {
		inventory, err := certmgr.Inventory(layout)
		if err != nil {
			return err
		}
		for _, info := range inventory {
			targets = append(targets, target{domain: info.Domain, email: info.Email})
		}
	}
	if len(targets) == 0 {
		return fmt.Errorf("no managed certificates to renew")
	}
	var errs []error
	for _, target := range targets {
		fmt.Fprintf(os.Stdout, "force renewing certificate for %s via DNS-01\n", target.domain)
		if _, err := manager.Issue(ctx, target.domain, target.email); err != nil {
			errs = append(errs, fmt.Errorf("renew %s: %w", target.domain, err))
			continue
		}
		if err := ctrl.DistributeCertificate(ctx, target.domain, os.Stdout); err != nil {
			errs = append(errs, fmt.Errorf("activate %s: %w", target.domain, err))
		}
	}
	return errors.Join(errs...)
}
