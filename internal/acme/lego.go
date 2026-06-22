package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"
	"io"
	stdlog "log"
	"sync"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	legolog "github.com/go-acme/lego/v4/log"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

var legoLoggerMu sync.Mutex

// legoUser implements lego's registration.User with an ephemeral account key.
type legoUser struct {
	email        string
	key          crypto.PrivateKey
	registration *registration.Resource
}

func (u *legoUser) GetEmail() string                        { return u.email }
func (u *legoUser) GetRegistration() *registration.Resource { return u.registration }
func (u *legoUser) GetPrivateKey() crypto.PrivateKey        { return u.key }

// LegoIssuer is the production Issuer backed by lego and Let's Encrypt.
type LegoIssuer struct {
	// Staging selects the Let's Encrypt staging directory when true.
	Staging bool
	// Output receives lego's own informational logs. When nil, lego keeps its
	// default logger.
	Output io.Writer
}

// NewLegoIssuer returns a LegoIssuer using the production directory.
func NewLegoIssuer() *LegoIssuer { return &LegoIssuer{} }

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
	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certificate{}, fmt.Errorf("generate account key: %w", err)
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

	provider, err := dnsProvider(r)
	if err != nil {
		return Certificate{}, err
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return Certificate{}, fmt.Errorf("configure dns-01: %w", err)
	}

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
