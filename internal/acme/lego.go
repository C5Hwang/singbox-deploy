package acme

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"fmt"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/challenge/http01"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/alidns"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

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
	// HTTP01Port is the port lego's standalone HTTP-01 server binds to. The
	// installer frees this port (stopping Nginx) during issuance. Defaults to
	// "80" when empty.
	HTTP01Port string
	// Staging selects the Let's Encrypt staging directory when true.
	Staging bool
}

// NewLegoIssuer returns a LegoIssuer using the production directory and port 80.
func NewLegoIssuer() *LegoIssuer { return &LegoIssuer{HTTP01Port: "80"} }

// Issue obtains a certificate for r.Domain via Let's Encrypt. The request is
// assumed pre-validated by Manager.Obtain.
func (i *LegoIssuer) Issue(ctx context.Context, r Request) (Certificate, error) {
	accountKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certificate{}, fmt.Errorf("generate account key: %w", err)
	}
	user := &legoUser{email: r.Email, key: accountKey}

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

// configureChallenge wires the selected challenge provider onto the client.
func (i *LegoIssuer) configureChallenge(client *lego.Client, r Request) error {
	switch r.Challenge {
	case ChallengeHTTP01:
		port := i.HTTP01Port
		if port == "" {
			port = "80"
		}
		return client.Challenge.SetHTTP01Provider(http01.NewProviderServer("", port))
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
