package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

const hubCertificatePendingState = "hub_certificate_reload_pending"

// DistributeCertificate activates the hub-owned certificate everywhere that
// consumes domain. The hub services are restarted when it is the local domain;
// every installed spoke using it receives the pair over WireGuard. Failed
// spoke deliveries stay marked pending and CheckHealth retries them later, so
// a successful ACME renewal cannot become a permanently hub-only update.
func (c *Controller) DistributeCertificate(ctx context.Context, domain string, log io.Writer) error {
	c.defaults()
	normalized, err := certmgr.NormalizeDomain(domain)
	if err != nil {
		return err
	}
	if log == nil {
		log = io.Discard
	}

	list, err := nodes.Load(c.Layout)
	if err != nil {
		return err
	}
	var errs []error
	matched := make([]nodes.Node, 0)
	for i := range list {
		nodeDomain, nerr := certmgr.NormalizeDomain(list[i].Domain)
		if nerr != nil || nodeDomain != normalized || !list[i].Installed {
			continue
		}
		var current nodes.Node
		if err := nodes.Mutate(c.Layout, list[i].ID, func(node *nodes.Node) error {
			// Re-check consumption under the transaction lock: a concurrent TUI
			// edit may have changed the node since the initial inventory load.
			domain, err := certmgr.NormalizeDomain(node.Domain)
			if err != nil || domain != normalized || !node.Installed {
				return nil
			}
			node.PendingCertificate = true
			current = *node
			return nil
		}); err != nil {
			errs = append(errs, fmt.Errorf("mark certificate delivery pending for %s: %w", list[i].EffectiveAlias(), err))
			continue
		}
		if current.ID != "" {
			matched = append(matched, current)
		}
	}

	hubDomain, _ := state.NewStore(c.Layout.StateDir).ReadValue("domain", false)
	if local, lerr := certmgr.NormalizeDomain(hubDomain); lerr == nil && local == normalized {
		store := state.NewStore(c.Layout.StateDir)
		if err := store.WriteString(hubCertificatePendingState, normalized+"\n", 0o600); err != nil {
			errs = append(errs, fmt.Errorf("mark hub certificate reload pending: %w", err))
		}
		fmt.Fprintf(log, "reloading hub services for %s...\n", normalized)
		if err := runCommands(c.Runner, []system.Command{
			system.Systemctl("restart", system.SingBoxService),
			system.Systemctl("restart", "nginx"),
		}); err != nil {
			errs = append(errs, fmt.Errorf("reload hub certificate: %w", err))
		} else if err := store.WriteString(hubCertificatePendingState, "", 0o600); err != nil {
			errs = append(errs, fmt.Errorf("clear hub certificate reload state: %w", err))
		}
	}

	for _, node := range matched {
		fmt.Fprintf(log, "delivering certificate to %s over WireGuard...\n", node.EffectiveAlias())
		if _, err := c.CheckHealth(ctx, node, log); err != nil {
			errs = append(errs, fmt.Errorf("deliver certificate to %s: %w", node.EffectiveAlias(), err))
		}
	}
	return errors.Join(errs...)
}

// RetryPendingCertificates retries activation work left by an earlier partial
// delivery without issuing a new certificate. The daily renewal command calls
// this even when no certificate is near expiry.
func (c *Controller) RetryPendingCertificates(ctx context.Context, log io.Writer) error {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	var errs []error
	store := state.NewStore(c.Layout.StateDir)
	if domain, err := store.ReadValue(hubCertificatePendingState, false); err != nil {
		errs = append(errs, err)
	} else if domain != "" {
		fmt.Fprintf(log, "retrying pending hub certificate reload for %s...\n", domain)
		if err := runCommands(c.Runner, []system.Command{
			system.Systemctl("restart", system.SingBoxService),
			system.Systemctl("restart", "nginx"),
		}); err != nil {
			errs = append(errs, fmt.Errorf("retry hub certificate reload: %w", err))
		} else if err := store.WriteString(hubCertificatePendingState, "", 0o600); err != nil {
			errs = append(errs, err)
		}
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	for _, node := range list {
		if !node.PendingCertificate || !node.Installed {
			continue
		}
		fmt.Fprintf(log, "retrying pending certificate delivery to %s...\n", node.EffectiveAlias())
		if _, err := c.CheckHealth(ctx, node, log); err != nil {
			errs = append(errs, fmt.Errorf("retry %s: %w", node.EffectiveAlias(), err))
		}
	}
	return errors.Join(errs...)
}

// CertificateConsumers reports the stable IDs of installed spokes using a
// domain. It is used by certificate deletion safeguards and the TUI.
func (c *Controller) CertificateConsumers(domain string) ([]string, error) {
	normalized, err := certmgr.NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return nil, err
	}
	var ids []string
	hubDomain, _ := state.NewStore(c.Layout.StateDir).ReadValue("domain", false)
	if hd, err := certmgr.NormalizeDomain(hubDomain); err == nil && hd == normalized {
		ids = append(ids, "hub")
	}
	for _, node := range list {
		nd, err := certmgr.NormalizeDomain(node.Domain)
		if err == nil && nd == normalized && node.Installed {
			ids = append(ids, node.ID)
		}
	}
	return ids, nil
}
