package deploy

import (
	"path/filepath"
)

func (o *Orchestrator) writeCertificateRenewalState(cfg Config) error {
	state := map[string]string{
		"domain": cfg.Domain,
	}
	for name, value := range state {
		if err := WriteFile(filepath.Join(o.Layout.StateDir, name), []byte(value+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}
