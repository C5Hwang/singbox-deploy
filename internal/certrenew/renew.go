// Package certrenew sweeps the managed certificate inventory and renews, via
// ACME DNS-01, every certificate that is near expiry. The hub owns all
// certificates (its own and each spoke's), so renewal is a single hub-side
// sweep; spoke delivery of refreshed pairs is wired by the caller through the
// AfterRenew hook.
package certrenew

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// DefaultRenewBefore is how long before expiry a certificate is renewed.
const DefaultRenewBefore = certmgr.DefaultRenewBefore

// Renewer performs one renewal sweep over every managed certificate.
type Renewer struct {
	Layout      paths.Layout
	Manager     *certmgr.Manager
	Runner      system.Runner
	Now         func() time.Time
	RenewBefore time.Duration
	Output      io.Writer
	// AfterRenew, when set, overrides the default post-renew action for each
	// renewed domain. The hub wires this to push refreshed spoke certificates
	// over the overlay; unset, only local services are restarted.
	AfterRenew func(domain string) error
}

// Run renews every certificate that is missing, invalid, or expiring within
// RenewBefore. Local services are restarted once if any certificate changed.
func (r Renewer) Run(ctx context.Context) error {
	r.defaults()
	// Ensure a freshly upgraded single-domain install can still renew.
	if err := certmgr.SeedLegacyCredentials(r.Layout); err != nil {
		return err
	}
	restartLocal := false
	r.Manager.AfterRenew = func(domain string) error {
		if r.AfterRenew != nil {
			return r.AfterRenew(domain)
		}
		// Default: defer a single local restart until the sweep completes.
		restartLocal = true
		r.logf("renewed certificate for %s\n", domain)
		return nil
	}
	renewed, err := r.Manager.RenewDue(ctx, r.RenewBefore)
	if err != nil {
		return err
	}
	if len(renewed) == 0 {
		r.logf("no certificates are due for renewal\n")
		return nil
	}
	if restartLocal {
		if rerr := deploy.RunCommands(r.Runner,
			system.Systemctl("restart", system.SingBoxService),
			system.Systemctl("restart", "nginx"),
		); rerr != nil {
			return rerr
		}
	}
	return nil
}

func (r *Renewer) defaults() {
	if r.Layout.Root == "" {
		r.Layout = paths.DefaultLayout()
	}
	if r.RenewBefore == 0 {
		r.RenewBefore = DefaultRenewBefore
	}
	if r.Now == nil {
		r.Now = time.Now
	}
	if r.Runner == nil {
		r.Runner = system.NewExecRunner(r.Output)
	}
	if r.Manager == nil {
		r.Manager = &certmgr.Manager{Layout: r.Layout, Now: r.Now, Output: r.Output}
	}
}

func (r Renewer) logf(format string, args ...any) {
	if r.Output != nil {
		fmt.Fprintf(r.Output, format, args...)
	}
}
