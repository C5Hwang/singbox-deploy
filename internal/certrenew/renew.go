// Package certrenew checks managed TLS certificates and renews them via ACME
// when they are near expiry. It covers both the master's local certificate
// and every cluster node's certificate, pushing renewed material to nodes
// over the WireGuard agent API.
package certrenew

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

const DefaultRenewBefore = 30 * 24 * time.Hour

// legacyCertState lists state files written by older releases that stored
// challenge selection and plaintext DNS credentials. They are unused after the
// DNS-01-only refactor; we remove them best-effort on the next renewal pass.
var legacyCertState = []string{"acme_challenge", "dns_provider", "dns_credential"}

// Renewer performs one certificate renewal check.
type Renewer struct {
	Layout      paths.Layout
	ACME        *acme.Manager
	Runner      system.Runner
	Now         func() time.Time
	RenewBefore time.Duration
	Output      io.Writer
}

// Run renews the master's certificate if it is missing, invalid, or expiring
// within RenewBefore, then walks every registered cluster node and renews
// each node's certificate the same way (pushing the renewed material via the
// agent API).
func (r Renewer) Run(ctx context.Context) error {
	r.defaults()
	r.cleanupLegacyState()
	if err := r.renewLocal(ctx); err != nil {
		return err
	}
	return r.renewNodes(ctx)
}

func (r Renewer) renewLocal(ctx context.Context) error {
	domain, err := r.localState()
	if err != nil {
		return err
	}

	certPath, keyPath := certPaths(r.Layout, domain)
	due, reason, err := renewalDue(certPath, keyPath, domain, r.now(), r.RenewBefore)
	if err != nil {
		return err
	}
	if !due {
		r.logf("certificate for %s is not due for renewal\n", domain)
		return nil
	}
	r.logf("renewing certificate for %s: %s\n", domain, reason)

	registry := cluster.NewRegistry(r.Layout)
	creds, err := registry.DNS().FindForDomain(domain)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no DNS credentials configured for %s; add them via the Certificate menu", domain)
		}
		return fmt.Errorf("find dns credentials: %w", err)
	}

	cert, err := r.ACME.Obtain(ctx, acme.Request{
		Domain:      domain,
		DNSProvider: creds.Provider,
		Credentials: creds.EnvMap(),
	})
	if err != nil {
		return err
	}
	if err := writeFile(certPath, cert.CertificatePEM, 0o644); err != nil {
		return err
	}
	if err := writeFile(keyPath, cert.PrivateKeyPEM, 0o600); err != nil {
		return err
	}

	return runAll(r.Runner,
		system.Command{Name: "systemctl", Args: []string{"restart", system.SingBoxService}},
		system.Command{Name: "systemctl", Args: []string{"restart", "nginx"}},
	)
}

// renewNodes walks every registered cluster node and renews each node's
// certificate if it is missing or expiring within RenewBefore. The renewed
// certificate is pushed to the node via the agent API. Per-node failures are
// logged but do not abort the loop; one unreachable node should not block the
// rest of the fleet.
func (r Renewer) renewNodes(ctx context.Context) error {
	registry := cluster.NewRegistry(r.Layout)
	nodes, err := registry.List()
	if err != nil {
		r.logf("list cluster nodes: %v\n", err)
		return nil
	}
	for _, node := range nodes {
		if !node.HasTLSProtocol() {
			continue
		}
		if err := r.renewOneNode(ctx, registry, node); err != nil {
			r.logf("renew node %s (%s): %v\n", node.Alias, node.Domain, err)
		}
	}
	return nil
}

func (r Renewer) renewOneNode(ctx context.Context, registry cluster.Registry, node cluster.Node) error {
	agent := cluster.NewAgentClient(node)
	status, err := agent.Status(ctx)
	if err != nil {
		return fmt.Errorf("fetch status: %w", err)
	}
	if status.CertExpiry != "" {
		expiry, perr := time.Parse(time.RFC3339, status.CertExpiry)
		if perr == nil && r.now().Add(r.RenewBefore).Before(expiry) {
			r.logf("node %s cert not due (expires %s)\n", node.Alias, expiry.Format(time.RFC3339))
			return nil
		}
	}
	creds, err := registry.DNS().FindForDomain(node.Domain)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no DNS credentials configured for root domain matching %s", node.Domain)
		}
		return fmt.Errorf("find dns credentials: %w", err)
	}
	cert, err := r.ACME.Obtain(ctx, acme.Request{
		Domain:      node.Domain,
		DNSProvider: creds.Provider,
		Credentials: creds.EnvMap(),
	})
	if err != nil {
		return fmt.Errorf("acme: %w", err)
	}
	r.logf("issuing renewed cert for node %s (%s)\n", node.Alias, node.Domain)
	if err := agent.DeployCert(ctx, cluster.CertDeploy{
		Domain: node.Domain,
		Cert:   string(cert.CertificatePEM),
		Key:    string(cert.PrivateKeyPEM),
	}); err != nil {
		return fmt.Errorf("push cert to node: %w", err)
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
	if r.ACME == nil {
		issuer := acme.NewLegoIssuer()
		issuer.Output = r.Output
		r.ACME = acme.NewManager(issuer)
	}
}

// cleanupLegacyState removes state files that older releases wrote (challenge
// selection and plaintext DNS credentials). Best-effort; ignores all errors so
// the first renewal after upgrade never fails on stale state.
func (r Renewer) cleanupLegacyState() {
	for _, name := range legacyCertState {
		_ = os.Remove(filepath.Join(r.Layout.StateDir, name))
	}
}

func (r Renewer) now() time.Time { return r.Now() }

func (r Renewer) logf(format string, args ...any) {
	if r.Output != nil {
		fmt.Fprintf(r.Output, format, args...)
	}
}

func (r Renewer) localState() (domain string, err error) {
	store := state.NewStore(r.Layout.StateDir)
	return readState(store, "domain", true)
}

func readState(store state.Store, name string, required bool) (string, error) {
	value, err := store.ReadString(name)
	if err != nil {
		if !required && os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read state %s: %w", name, err)
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("state %s is empty", name)
	}
	return value, nil
}

func certPaths(layout paths.Layout, domain string) (cert, key string) {
	return filepath.Join(layout.TLSDir, domain+".crt"), filepath.Join(layout.TLSDir, domain+".key")
}

func renewalDue(certPath, keyPath, domain string, t time.Time, renewBefore time.Duration) (bool, string, error) {
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, "certificate file is missing", nil
		}
		return false, "", err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, "private key file is missing", nil
		}
		return false, "", err
	}
	if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
		return true, "certificate and private key do not match", nil
	}
	cert, err := firstCertificate(certPEM)
	if err != nil {
		return true, "certificate is invalid", nil
	}
	if t.Before(cert.NotBefore) {
		return true, "certificate is not valid yet", nil
	}
	if !t.Before(cert.NotAfter) {
		return true, "certificate has expired", nil
	}
	if err := cert.VerifyHostname(domain); err != nil {
		return true, "certificate hostname does not match domain", nil
	}
	if !t.Add(renewBefore).Before(cert.NotAfter) {
		return true, fmt.Sprintf("certificate expires at %s", cert.NotAfter.Format(time.RFC3339)), nil
	}
	return false, "", nil
}

func firstCertificate(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, fmt.Errorf("missing certificate PEM block")
	}
	return x509.ParseCertificate(block.Bytes)
}

func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

func runAll(runner system.Runner, cmds ...system.Command) error {
	for _, cmd := range cmds {
		if err := runner.Run(cmd); err != nil {
			return fmt.Errorf("command %q: %w", cmd.String(), err)
		}
	}
	return nil
}
