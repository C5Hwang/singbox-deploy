// Package certrenew checks managed TLS certificates and renews them via ACME
// when they are near expiry.
package certrenew

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

const DefaultRenewBefore = 30 * 24 * time.Hour

// Renewer performs one certificate renewal check.
type Renewer struct {
	Layout      paths.Layout
	ACME        *acme.Manager
	Runner      system.Runner
	Now         func() time.Time
	RenewBefore time.Duration
	Output      io.Writer
}

// Run renews the managed certificate if it is missing, invalid, or expiring
// within RenewBefore. Otherwise it exits successfully without changes.
func (r Renewer) Run(ctx context.Context) error {
	r.defaults()
	req, err := r.requestFromState()
	if err != nil {
		return err
	}

	certPath, keyPath := deploy.CertificatePaths(r.Layout, req.Domain)
	due, reason, err := renewalDue(certPath, keyPath, req.Domain, r.now(), r.RenewBefore)
	if err != nil {
		return err
	}
	if !due {
		r.logf("certificate for %s is not due for renewal\n", req.Domain)
		return nil
	}
	r.logf("renewing certificate for %s: %s\n", req.Domain, reason)

	stoppedNginx := false
	if req.Challenge == acme.ChallengeHTTP01 {
		_ = r.Runner.Run(system.Systemctl("stop", "nginx"))
		stoppedNginx = true
		defer func() {
			if stoppedNginx {
				_ = r.Runner.Run(system.Systemctl("start", "nginx"))
			}
		}()
	}

	cert, err := r.ACME.Obtain(ctx, req)
	if err != nil {
		return err
	}
	// Replace the pair together so a failure cannot leave a new certificate
	// alongside the old private key (nginx/sing-box would fail to load TLS).
	if err := state.WriteFilePair(keyPath, cert.PrivateKeyPEM, 0o600, certPath, cert.CertificatePEM, 0o644); err != nil {
		return err
	}

	if err := deploy.RunCommands(r.Runner,
		system.Systemctl("restart", system.SingBoxService),
		system.Systemctl("restart", "nginx"),
	); err != nil {
		return err
	}
	stoppedNginx = false
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
		issuer.AccountKeyPath = acme.AccountKeyPathFor(r.Layout.StateDir)
		r.ACME = acme.NewManager(issuer)
	}
}

func (r Renewer) now() time.Time { return r.Now() }

func (r Renewer) logf(format string, args ...any) {
	if r.Output != nil {
		fmt.Fprintf(r.Output, format, args...)
	}
}

func (r Renewer) requestFromState() (acme.Request, error) {
	store := state.NewStore(r.Layout.StateDir)
	domain, err := store.ReadValue("domain", true)
	if err != nil {
		return acme.Request{}, err
	}
	email, err := store.ReadValue("email", false)
	if err != nil {
		return acme.Request{}, err
	}
	challenge, err := store.ReadValue("acme_challenge", true)
	if err != nil {
		return acme.Request{}, err
	}
	dnsProvider, err := store.ReadValue("dns_provider", false)
	if err != nil {
		return acme.Request{}, err
	}
	dnsCredential, err := store.ReadValue("dns_credential", false)
	if err != nil {
		return acme.Request{}, err
	}

	return acme.Request{
		Domain:      domain,
		Email:       email,
		Challenge:   acme.Challenge(challenge),
		DNSProvider: dnsProvider,
		Credentials: deploy.DNSCredentialsForProvider(dnsProvider, dnsCredential),
	}, nil
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
	cert, err := deploy.FirstCertificate(certPEM)
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
