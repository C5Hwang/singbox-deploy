package certmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/acme"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// DefaultRenewBefore is how long before expiry a certificate is renewed.
const DefaultRenewBefore = 30 * 24 * time.Hour

type issuanceSemaphore struct{ token chan struct{} }

var issuanceSemaphores sync.Map

// NoCredentialError reports that no stored DNS credential covers a domain, so
// issuance cannot proceed until one is added. The TUI uses this to redirect the
// operator to the credential-management page.
type NoCredentialError struct{ Domain string }

func (e *NoCredentialError) Error() string {
	return fmt.Sprintf("no DNS credential covers %s", e.Domain)
}

// Manager issues and renews certificates using the stored DNS credentials.
type Manager struct {
	Layout paths.Layout
	// ACME performs issuance; defaults to a lego-backed DNS-01 issuer that
	// persists the shared ACME account key under the state directory.
	ACME *acme.Manager
	// Now is the clock, defaulting to time.Now (overridable in tests).
	Now func() time.Time
	// Output receives lego's informational logs during issuance.
	Output io.Writer
	// AfterRenew, when set, is called for each domain whose certificate was
	// successfully renewed by RenewDue, so the caller can reload local services
	// or push the new pair to a spoke. A returned error is aggregated but does
	// not stop the remaining renewals.
	AfterRenew func(domain string) error
}

func (m *Manager) defaults() {
	if m.Layout.Root == "" {
		m.Layout = paths.DefaultLayout()
	}
	if m.Now == nil {
		m.Now = time.Now
	}
	if m.ACME == nil {
		issuer := acme.NewLegoIssuer()
		issuer.Output = m.Output
		issuer.AccountKeyPath = acme.AccountKeyPathFor(m.Layout.StateDir)
		m.ACME = acme.NewManager(issuer)
	}
}

// Issue obtains (or reissues) a certificate for domain now, selecting the DNS
// credential by suffix match. It returns a NoCredentialError when no stored
// credential covers the domain.
func (m *Manager) Issue(ctx context.Context, domain string) (CertInfo, error) {
	m.defaults()
	var err error
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return CertInfo{}, err
	}
	unlock, err := m.lockIssuance(ctx)
	if err != nil {
		return CertInfo{}, err
	}
	defer unlock()
	return m.issueLocked(ctx, domain)
}

// issueLocked performs one forced order while the cross-process issuance lock
// is held. All domains share the lock because lego also shares one persisted
// ACME account key; serializing creation prevents two Hub processes from
// racing account initialization or writing the same certificate pair.
func (m *Manager) issueLocked(ctx context.Context, domain string) (CertInfo, error) {
	creds, err := LoadCredentials(m.Layout)
	if err != nil {
		return CertInfo{}, err
	}
	cred, ok := SelectCredential(creds, domain)
	if !ok {
		return CertInfo{}, &NoCredentialError{Domain: domain}
	}
	cert, err := m.ACME.Obtain(ctx, acme.Request{
		Domain:      domain,
		Challenge:   acme.ChallengeDNS01,
		DNSProvider: cred.Provider,
		Credentials: cred.Env(),
	})
	if err != nil {
		return CertInfo{}, err
	}
	now := m.Now()
	if _, err := ValidateCertificatePair(cert.CertificatePEM, cert.PrivateKeyPEM, domain, now); err != nil {
		return CertInfo{}, fmt.Errorf("ACME returned an unusable certificate for %s: %w", domain, err)
	}
	certPath, keyPath, err := certPaths(m.Layout, domain)
	if err != nil {
		return CertInfo{}, err
	}
	if err := state.WriteFilePair(keyPath, cert.PrivateKeyPEM, 0o600, certPath, cert.CertificatePEM, 0o644); err != nil {
		return CertInfo{}, err
	}
	if err := Register(m.Layout, domain); err != nil {
		return CertInfo{}, err
	}
	return inspectAt(m.Layout, domain, now), nil
}

// EnsureIssued issues a certificate for domain only when it is missing or due
// for renewal; a currently-valid certificate is left untouched. It reports
// whether issuance ran.
func (m *Manager) EnsureIssued(ctx context.Context, domain string, renewBefore time.Duration) (CertInfo, bool, error) {
	m.defaults()
	var err error
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return CertInfo{}, false, err
	}
	if renewBefore <= 0 {
		renewBefore = DefaultRenewBefore
	}
	unlock, err := m.lockIssuance(ctx)
	if err != nil {
		return CertInfo{}, false, err
	}
	defer unlock()
	info := inspectAt(m.Layout, domain, m.Now())
	if due, _ := renewalDue(info, m.Now(), renewBefore); !due {
		return info, false, nil
	}
	issued, err := m.issueLocked(ctx, domain)
	if err != nil {
		return CertInfo{}, false, err
	}
	return issued, true, nil
}

// RenewDue renews every managed certificate that is missing, invalid, or
// expiring within renewBefore. It returns the domains that were renewed; the
// AfterRenew hook fires for each. Per-domain failures are aggregated into the
// returned error without stopping the sweep.
func (m *Manager) RenewDue(ctx context.Context, renewBefore time.Duration) ([]string, error) {
	m.defaults()
	if renewBefore <= 0 {
		renewBefore = DefaultRenewBefore
	}
	inventory, err := Inventory(m.Layout)
	if err != nil {
		return nil, err
	}
	var renewed []string
	var errs []error
	for _, info := range inventory {
		due, _ := renewalDue(info, m.Now(), renewBefore)
		if !due {
			continue
		}
		_, issued, err := m.EnsureIssued(ctx, info.Domain, renewBefore)
		if err != nil {
			errs = append(errs, fmt.Errorf("renew %s: %w", info.Domain, err))
			continue
		}
		// Another Hub process may have renewed this domain while this sweep was
		// waiting for the issuance lock. Do not create a duplicate order or fire
		// a second distribution hook in that case.
		if !issued {
			continue
		}
		renewed = append(renewed, info.Domain)
		if m.AfterRenew != nil {
			if err := m.AfterRenew(info.Domain); err != nil {
				errs = append(errs, fmt.Errorf("post-renew %s: %w", info.Domain, err))
			}
		}
	}
	return renewed, joinErrors(errs)
}

func (m *Manager) lockIssuance(ctx context.Context) (func(), error) {
	lockPath := filepath.Join(m.Layout.StateDir, ".certmgr-issue.lock")
	key, err := filepath.Abs(filepath.Clean(lockPath))
	if err != nil {
		key = filepath.Clean(lockPath)
	}
	candidate := &issuanceSemaphore{token: make(chan struct{}, 1)}
	actual, _ := issuanceSemaphores.LoadOrStore(key, candidate)
	semaphore := actual.(*issuanceSemaphore)
	select {
	case semaphore.token <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	processUnlock := func() { <-semaphore.token }

	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		processUnlock()
		return nil, fmt.Errorf("create certificate lock directory: %w", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		processUnlock()
		return nil, fmt.Errorf("open certificate issuance lock: %w", err)
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() {
				_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
				_ = f.Close()
				processUnlock()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			processUnlock()
			return nil, fmt.Errorf("lock certificate issuance: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			processUnlock()
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// renewalDue reports whether a certificate needs (re)issuance and why.
func renewalDue(info CertInfo, now time.Time, renewBefore time.Duration) (bool, string) {
	if !info.Present {
		return true, "certificate is missing"
	}
	if info.NotAfter.IsZero() {
		return true, "certificate is invalid"
	}
	if now.Before(info.NotBefore) {
		return true, "certificate is not valid yet"
	}
	if !now.Before(info.NotAfter) {
		return true, "certificate has expired"
	}
	if !info.Valid {
		return true, "certificate does not match its domain"
	}
	if !now.Add(renewBefore).Before(info.NotAfter) {
		return true, fmt.Sprintf("certificate expires at %s", info.NotAfter.Format(time.RFC3339))
	}
	return false, ""
}

func joinErrors(errs []error) error {
	switch len(errs) {
	case 0:
		return nil
	case 1:
		return errs[0]
	default:
		msg := "multiple certificate errors:"
		for _, e := range errs {
			msg += "\n  - " + e.Error()
		}
		return fmt.Errorf("%s", msg)
	}
}
