package ui

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// refreshHubSubscriptions re-folds every installed spoke's nodes into the hub's
// published subscription over the overlay. It is best-effort: failures (e.g. an
// unreachable spoke) are logged, not surfaced as errors, so a local subscription
// edit still succeeds.
func refreshHubSubscriptions(logs io.Writer) {
	ctrl := &hubctl.Controller{Layout: paths.DefaultLayout(), ExpectedVersion: toolVersion}
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		fmt.Fprintf(logs, "warning: subscription refresh had issues: %v\n", err)
	}
}

// domainCoveredByCredential rejects an install/cert domain that no stored DNS
// credential covers by suffix match. The hub issues every certificate via
// DNS-01, so a covering credential must exist in Certificate management first.
func domainCoveredByCredential(domain string) error {
	return ensureDomainManaged(paths.DefaultLayout(), domain)
}

// ensureDomainManaged enforces the single certificate-management entry point
// used by both hub installation and spoke creation. A domain must be present
// in the central inventory and have a suffix-matching DNS-01 credential.
func ensureDomainManaged(layout paths.Layout, domain string) error {
	if err := certmgr.SeedLegacyCredentials(layout); err != nil {
		return err
	}
	if _, err := certmgr.IsManaged(layout, domain); err != nil {
		return err
	}
	creds, err := certmgr.LoadCredentials(layout)
	if err != nil {
		return err
	}
	if !certmgr.CredentialCovers(creds, domain) {
		return &certmgr.NoCredentialError{Domain: domain}
	}
	return nil
}

func certificateRedirectDomain(err error) string {
	if err == nil {
		return ""
	}
	var unmanaged *certmgr.UnmanagedDomainError
	if errors.As(err, &unmanaged) {
		return unmanaged.Domain
	}
	var uncovered *certmgr.NoCredentialError
	if errors.As(err, &uncovered) {
		return uncovered.Domain
	}
	return ""
}

// finalizeHubInstall brings up the WireGuard overlay before recording hub
// completion. Spoke management must never be unlocked when overlay setup failed.
func finalizeHubInstall(layout paths.Layout, cfg deploy.Config, runner system.Runner, logs io.Writer) error {
	// Clear a stale success marker first (for example when re-running install).
	if err := nodes.SetHubInstalled(layout, false); err != nil {
		return err
	}
	ctrl := &hubctl.Controller{Layout: layout, Runner: runner, ExpectedVersion: toolVersion}
	if _, err := ctrl.EnsureOverlay(cfg.Domain); err != nil {
		return fmt.Errorf("initialize hub WireGuard overlay: %w", err)
	}
	if err := nodes.SetHubInstalled(layout, true); err != nil {
		return err
	}
	// Re-fold any existing spokes into the published subscription (a fresh hub
	// has none, so this is a no-op then).
	if err := ctrl.RefreshSubscriptions(context.Background()); err != nil {
		fmt.Fprintf(logs, "warning: subscription refresh had issues: %v\n", err)
	}
	return nil
}
