package deploy

import (
	"path/filepath"
)

// certificateRenewalState returns the small state keys the certificate flow
// needs on disk. Renewal itself is driven by the central certificate inventory
// (internal/certmgr) and the DNS credentials it stores, so only the domain and
// email are persisted here. It is the single definition of those keys; both the
// early write (stepServices) and the full install-state write derive from it.
func certificateRenewalState(cfg Config) map[string]string {
	return map[string]string{
		"domain": cfg.Domain,
		"email":  cfg.Email,
	}
}

func (o *Orchestrator) writeCertificateRenewalState(cfg Config) error {
	for name, value := range certificateRenewalState(cfg) {
		if err := WriteFile(filepath.Join(o.Layout.StateDir, name), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}
