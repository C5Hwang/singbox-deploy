package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
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
//
// progress, when set, reports one step per activation target — the hub reload
// plus each spoke — with Total fixed to the number of targets. A caller that
// prefixes its own steps must offset both fields.
func (c *Controller) DistributeCertificate(ctx context.Context, domain string, log io.Writer, progress func(deploy.Event)) error {
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

	store := state.NewStore(c.Layout.StateDir)
	hubConsumes := hubConsumesDomain(store, normalized)

	// Marking the spokes pending above is bookkeeping, not an activation; the
	// reported steps are exactly the targets that receive the new pair.
	total := len(matched)
	if hubConsumes {
		total++
	}
	index := 0

	if hubConsumes {
		index++
		err := reportStep(progress, deploy.Event{
			Index: index, Total: total, Label: "Reload hub services", Detail: normalized,
		}, func() error {
			return errors.Join(c.reloadHubCertificate(store, normalized, log)...)
		})
		if err != nil {
			errs = append(errs, err)
		}
	}

	for _, node := range matched {
		index++
		err := reportStep(progress, deploy.Event{
			Index: index, Total: total, Label: "Deliver to " + node.EffectiveAlias(), Detail: normalized,
		}, func() error {
			fmt.Fprintf(log, "delivering certificate to %s over WireGuard...\n", node.EffectiveAlias())
			_, err := c.syncCertificate(ctx, node, log)
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("deliver certificate to %s: %w", node.EffectiveAlias(), err))
		}
	}
	return errors.Join(errs...)
}

// reloadHubCertificate restarts the hub-local services holding the certificate
// open, bracketed by the pending marker so an interrupted reload is picked up
// by RetryPendingCertificates. A failed marker write does not skip the reload,
// so every failure is collected rather than returned at the first one.
func (c *Controller) reloadHubCertificate(store state.Store, normalized string, log io.Writer) []error {
	var errs []error
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
	return errs
}

// reportStep brackets one activation target with running/ok/fail progress and
// returns its error. Unlike deploy.RunSteps it does not stop the sequence: a
// spoke that cannot be reached stays marked pending for a later retry, so the
// remaining targets must still be attempted.
func reportStep(progress func(deploy.Event), e deploy.Event, run func() error) error {
	e.Status = "running"
	deploy.EmitProgress(progress, e)
	if err := run(); err != nil {
		e.Status, e.Err = "fail", err
		deploy.EmitProgress(progress, e)
		return err
	}
	e.Status = "ok"
	deploy.EmitProgress(progress, e)
	return nil
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
		if _, err := c.syncCertificate(ctx, node, log); err != nil {
			errs = append(errs, fmt.Errorf("retry %s: %w", node.EffectiveAlias(), err))
		}
	}
	return errors.Join(errs...)
}

// hubConsumesDomain reports whether the hub's own Nginx serves normalized. The
// hub holds one certificate per name it answers to, which since the monitor got
// a name of its own is the install domain *and* the monitor domain — checking
// only the install domain would treat the monitor's pair as unused.
func hubConsumesDomain(store state.Store, normalized string) bool {
	for _, name := range hubCertificateDomains(store) {
		if name == normalized {
			return true
		}
	}
	return false
}

// hubCertificateDomains returns every normalized name the hub's Nginx holds a
// certificate for. A monitor that shares the install domain, is disabled, or
// predates the monitor domain adds nothing.
func hubCertificateDomains(store state.Store) []string {
	var domains []string
	installDomain, _ := store.ReadValue("domain", false)
	install, err := certmgr.NormalizeDomain(installDomain)
	if err != nil {
		return nil
	}
	domains = append(domains, install)
	if monitor, _ := store.ReadValue("monitor", false); monitor == "no" {
		return domains
	}
	monitorDomain, _ := store.ReadValue("monitor_domain", false)
	monitor, err := certmgr.NormalizeDomain(monitorDomain)
	if err != nil || monitor == install {
		return domains
	}
	return append(domains, monitor)
}

// CertificateConsumer identifies one installed Hub/Spoke that uses a
// certificate. ID is the stable identity used by deletion safeguards; Label is
// deliberately presentation-only so renaming a spoke cannot change whether the
// certificate is considered in use.
type CertificateConsumer struct {
	ID    string
	Label string
}

// CertificateConsumerList retains stable consumer identities while providing
// the presentation labels needed by interactive callers.
type CertificateConsumerList []CertificateConsumer

// Labels returns a copy of the operator-facing labels.
func (consumers CertificateConsumerList) Labels() []string {
	labels := make([]string, len(consumers))
	for i := range consumers {
		labels[i] = consumers[i].Label
	}
	return labels
}

// CertificateConsumers reports installed Hub/Spoke consumers of domain. The
// returned stable IDs are kept separate from their operator-facing labels.
func (c *Controller) CertificateConsumers(domain string) (CertificateConsumerList, error) {
	normalized, err := certmgr.NormalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return nil, err
	}
	var consumers CertificateConsumerList
	if hubConsumesDomain(state.NewStore(c.Layout.StateDir), normalized) {
		consumers = append(consumers, CertificateConsumer{
			ID:    "hub",
			Label: fmt.Sprintf("Hub (%s)", normalized),
		})
	}
	for _, node := range list {
		nd, err := certmgr.NormalizeDomain(node.Domain)
		if err == nil && nd == normalized && node.Installed {
			consumers = append(consumers, CertificateConsumer{
				ID:    node.ID,
				Label: fmt.Sprintf("%s (%s)", node.EffectiveAlias(), normalized),
			})
		}
	}
	return consumers, nil
}
