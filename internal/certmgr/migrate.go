package certmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

const (
	legacyMigrationMarker  = "certmgr_schema_version"
	legacyMigrationVersion = "2"
)

var legacyMigrationMu sync.Mutex

// SeedLegacyCredentials upgrades certificate-manager state. Version 1 bridges
// a pre-hub-spoke install into the credential store. Version 2 removes ACME
// account emails persisted by older releases. The marker advances only after
// every required step completes, so a partial failure can be retried safely.
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
	storedVersion := 0
	if version != "" {
		storedVersion, err = strconv.Atoi(version)
		if err != nil {
			return fmt.Errorf("invalid certificate migration schema version %q", version)
		}
		currentVersion, _ := strconv.Atoi(legacyMigrationVersion)
		if storedVersion >= currentVersion {
			return nil
		}
	}

	// Remove the retired personal data before doing any unrelated legacy import
	// work. If a later step fails, this cleanup is idempotent on the next run.
	if storedVersion < 2 {
		if err := removeLegacyACMEEmailState(layout); err != nil {
			return err
		}
	}
	if storedVersion < 1 {
		if err := seedLegacyCredentialState(layout, store); err != nil {
			return err
		}
	}
	if err := store.WriteString(legacyMigrationMarker, legacyMigrationVersion+"\n", 0o600); err != nil {
		return fmt.Errorf("write certificate migration marker: %w", err)
	}
	return nil
}

// seedLegacyCredentialState imports the version-0 flat certificate state.
func seedLegacyCredentialState(layout paths.Layout, store state.Store) error {
	domain, err := store.ReadValue("domain", false)
	if err != nil {
		return fmt.Errorf("read legacy certificate domain: %w", err)
	}
	if strings.TrimSpace(domain) == "" {
		return nil
	}
	domain, err = NormalizeDomain(domain)
	if err != nil {
		return fmt.Errorf("migrate legacy certificate: %w", err)
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
	if err := Register(layout, domain); err != nil {
		return err
	}
	return nil
}

// removeLegacyACMEEmailState deletes the old flat email and atomically rewrites
// both entry trees using their current schemas, which drops each legacy email
// field while preserving the certificate and DNS credential records.
func removeLegacyACMEEmailState(layout paths.Layout) error {
	if err := os.Remove(filepath.Join(layout.StateDir, "email")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy ACME email: %w", err)
	}
	if err := rewriteEntryTreeIfPresent(dnsCredentialsPath(layout), decodeDNSCredential, encodeDNSCredential); err != nil {
		return fmt.Errorf("remove legacy ACME emails from DNS credentials: %w", err)
	}
	if err := rewriteEntryTreeIfPresent(certsPath(layout), decodeRegisteredCert, encodeRegisteredCert); err != nil {
		return fmt.Errorf("remove legacy ACME emails from certificate registry: %w", err)
	}
	return nil
}

func rewriteEntryTreeIfPresent[T any](dir string, decode func(string) T, encode func(T) map[string]string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("entry tree %s is not a directory", dir)
	}
	_, err = state.TransactEntryDirs(dir, decode, encode, func(current []T) ([]T, error) {
		return current, nil
	})
	return err
}
