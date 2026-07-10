package certmgr

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

const (
	legacyMigrationMarker  = "certmgr_schema_version"
	legacyMigrationVersion = "1"
)

var legacyMigrationMu sync.Mutex

// SeedLegacyCredentials bridges a pre-hub-spoke install into the credential
// store. Older installs kept a single domain's DNS-01 details as flat state
// files (domain, dns_provider, dns_credential, email). When those flat files
// describe a supported DNS-01 setup, this merges a credential scoped to the
// legacy domain without discarding existing entries, then registers the legacy
// certificate so renewal keeps working after the upgrade. Migration is marked
// only after both the credential merge and registration complete, and can
// safely resume after a partial failure.
func SeedLegacyCredentials(layout paths.Layout) error {
	legacyMigrationMu.Lock()
	defer legacyMigrationMu.Unlock()

	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	store := state.NewStore(layout.StateDir)
	version, err := store.ReadValue(legacyMigrationMarker, false)
	if err != nil {
		return fmt.Errorf("read certificate migration marker: %w", err)
	}
	if version != "" {
		storedVersion, err := strconv.Atoi(version)
		if err != nil {
			return fmt.Errorf("invalid certificate migration schema version %q", version)
		}
		currentVersion, _ := strconv.Atoi(legacyMigrationVersion)
		if storedVersion >= currentVersion {
			return nil
		}
	}

	domain, err := store.ReadValue("domain", false)
	if err != nil {
		return fmt.Errorf("read legacy certificate domain: %w", err)
	}
	if strings.TrimSpace(domain) == "" {
		return store.WriteString(legacyMigrationMarker, legacyMigrationVersion+"\n", 0o600)
	}
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return fmt.Errorf("migrate legacy certificate: %w", err)
	}
	email, err := store.ReadValue("email", false)
	if err != nil {
		return fmt.Errorf("read legacy certificate email: %w", err)
	}
	challenge, err := store.ReadValue("acme_challenge", false)
	if err != nil {
		return fmt.Errorf("read legacy ACME challenge: %w", err)
	}

	if strings.EqualFold(strings.TrimSpace(challenge), "dns-01") {
		provider, err := store.ReadValue("dns_provider", false)
		if err != nil {
			return fmt.Errorf("read legacy DNS provider: %w", err)
		}
		credential, err := store.ReadValue("dns_credential", false)
		if err != nil {
			return fmt.Errorf("read legacy DNS credential: %w", err)
		}
		legacy := DNSCredential{
			Domain:     domain,
			Provider:   strings.TrimSpace(provider),
			Credential: strings.TrimSpace(credential),
			Email:      strings.TrimSpace(email),
		}
		if err := legacy.Validate(); err != nil {
			return fmt.Errorf("migrate legacy DNS credential: %w", err)
		}
		if _, err := AddCredentialIfAbsent(layout, legacy); err != nil {
			return err
		}
	}

	// Always register the old hub domain, including HTTP-01 installs. Inventory
	// derives NeedsDNSCredential from current credential coverage, so HTTP-01
	// certificates remain usable until expiry while clearly requiring DNS-01
	// setup for their next renewal.
	if err := Register(layout, domain, email); err != nil {
		return err
	}
	if err := store.WriteString(legacyMigrationMarker, legacyMigrationVersion+"\n", 0o600); err != nil {
		return fmt.Errorf("write certificate migration marker: %w", err)
	}
	return nil
}
