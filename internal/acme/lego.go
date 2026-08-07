package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	stdlog "log"
	"os"
	"path/filepath"
	"sync"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"

	"github.com/C5Hwang/singbox-deploy/internal/state"
)

var legoLoggerMu sync.Mutex

// legoUser implements lego's registration.User.
type legoUser struct {
	key          crypto.PrivateKey
	registration *registration.Resource
}

// GetEmail satisfies registration.User. Accounts are registered without a
// contact address, which lego turns into an empty ACME contact list.
func (u *legoUser) GetEmail() string                        { return "" }
func (u *legoUser) GetRegistration() *registration.Resource { return u.registration }
func (u *legoUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// LegoIssuer is the production Issuer backed by lego and Let's Encrypt.
type LegoIssuer struct {
	// Staging selects the Let's Encrypt staging directory when true.
	Staging bool
	// Output receives lego's own informational logs. When nil, lego keeps its
	// default logger.
	Output io.Writer
	// AccountKeyPath persists the ACME account private key so every issuance
	// and renewal reuses one Let's Encrypt account instead of registering a
	// fresh one (repeated registrations hit LE's accounts-per-IP rate limit).
	// When empty, an ephemeral key is used.
	AccountKeyPath string
}

// NewLegoIssuer returns a LegoIssuer using the production directory.
func NewLegoIssuer() *LegoIssuer { return &LegoIssuer{} }

// AccountKeyPathFor returns the canonical location of the persisted ACME
// account key inside the managed state directory.
func AccountKeyPathFor(stateDir string) string {
	return filepath.Join(stateDir, "acme_account_key")
}

// Issue obtains a certificate for r.Domain via Let's Encrypt. The request is
// assumed pre-validated by Manager.Obtain.
func (i *LegoIssuer) Issue(ctx context.Context, r Request) (Certificate, error) {
	return i.withLegoLogger(func() (Certificate, error) {
		return i.issue(ctx, r)
	})
}

func (i *LegoIssuer) withLegoLogger(fn func() (Certificate, error)) (Certificate, error) {
	legoLoggerMu.Lock()
	defer legoLoggerMu.Unlock()

	if i.Output == nil {
		return fn()
	}

	previous := legolog.Logger
	legolog.Logger = stdlog.New(i.Output, "", stdlog.LstdFlags)
	defer func() {
		legolog.Logger = previous
	}()

	return fn()
}

func (i *LegoIssuer) issue(ctx context.Context, r Request) (Certificate, error) {
	accountKey, err := i.accountKey()
	if err != nil {
		return Certificate{}, err
	}
	user := &legoUser{key: accountKey}

	cfg := lego.NewConfig(user)
	if i.Staging {
		cfg.CADirURL = lego.LEDirectoryStaging
	} else {
		cfg.CADirURL = lego.LEDirectoryProduction
	}

	client, err := lego.NewClient(cfg)
	if err != nil {
		return Certificate{}, fmt.Errorf("new acme client: %w", err)
	}

	if err := i.configureChallenge(client, r); err != nil {
		return Certificate{}, err
	}

	// Registering an already-known key returns the existing account, so with a
	// persisted key this reuses one LE account across issuances and renewals.
	reg, err := client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
	if err != nil {
		return Certificate{}, fmt.Errorf("register account: %w", err)
	}
	user.registration = reg

	res, err := client.Certificate.Obtain(certificate.ObtainRequest{
		Domains: []string{r.Domain},
		Bundle:  true,
	})
	if err != nil {
		return Certificate{}, fmt.Errorf("obtain certificate: %w", err)
	}
	return Certificate{CertificatePEM: res.Certificate, PrivateKeyPEM: res.PrivateKey}, nil
}

// accountKey loads the persisted ACME account key, creating and persisting a
// new one on first use. Without AccountKeyPath it returns an ephemeral key.
func (i *LegoIssuer) accountKey() (crypto.PrivateKey, error) {
	if i.AccountKeyPath == "" {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate account key: %w", err)
		}
		return key, nil
	}
	pemBytes, err := os.ReadFile(i.AccountKeyPath)
	switch {
	case err == nil:
		block, _ := pem.Decode(pemBytes)
		if block == nil {
			return nil, fmt.Errorf("acme account key %s is not valid PEM", i.AccountKeyPath)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse acme account key %s: %w", i.AccountKeyPath, err)
		}
		return key, nil
	case os.IsNotExist(err):
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate account key: %w", err)
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, fmt.Errorf("encode account key: %w", err)
		}
		pemBytes := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
		if err := state.WriteFileAtomic(i.AccountKeyPath, pemBytes, 0o600); err != nil {
			return nil, fmt.Errorf("persist account key: %w", err)
		}
		return key, nil
	default:
		return nil, fmt.Errorf("read acme account key %s: %w", i.AccountKeyPath, err)
	}
}

// configureChallenge wires the DNS-01 provider onto the client.
func (i *LegoIssuer) configureChallenge(client *lego.Client, r Request) error {
	switch r.Challenge {
	case ChallengeDNS01:
		provider, err := dnsProvider(r)
		if err != nil {
			return err
		}
		return client.Challenge.SetDNS01Provider(provider)
	default:
		return fmt.Errorf("unsupported challenge %q", r.Challenge)
	}
}

// dnsProvider constructs the lego DNS-01 provider for Cloudflare or Aliyun from
// the request credentials.
func dnsProvider(r Request) (challengeProvider, error) {
	switch r.DNSProvider {
	case "cloudflare":
		cfg := cloudflare.NewDefaultConfig()
		cfg.AuthToken = r.Credentials["CF_API_TOKEN"]
		if cfg.AuthToken == "" {
			cfg.AuthEmail = r.Credentials["CF_API_EMAIL"]
			cfg.AuthKey = r.Credentials["CF_API_KEY"]
		}
		return cloudflare.NewDNSProviderConfig(cfg)
	case "aliyun":
		cfg := alidns.NewDefaultConfig()
		cfg.APIKey = r.Credentials["ALICLOUD_ACCESS_KEY"]
		cfg.SecretKey = r.Credentials["ALICLOUD_SECRET_KEY"]
		return alidns.NewDNSProviderConfig(cfg)
	default:
		return nil, fmt.Errorf("unsupported DNS provider %q", r.DNSProvider)
	}
}

// challengeProvider is the minimal lego DNS provider interface we depend on.
type challengeProvider = interface {
	Present(domain, token, keyAuth string) error
	CleanUp(domain, token, keyAuth string) error
}
