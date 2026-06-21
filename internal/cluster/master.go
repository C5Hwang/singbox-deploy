package cluster

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/wireguard"
)

// MasterKeys reads the master's persisted WireGuard key pair. ErrNoMasterKeys
// is returned when the master has not yet been initialised; callers should
// invoke EnsureMasterKeys to generate and persist a pair on first cluster use.
func (r Registry) MasterKeys() (wireguard.KeyPair, error) {
	priv, err := os.ReadFile(filepath.Join(r.layout.StateDir, MasterPrivateKeyFile))
	if err != nil {
		if os.IsNotExist(err) {
			return wireguard.KeyPair{}, ErrNoMasterKeys
		}
		return wireguard.KeyPair{}, err
	}
	pub, err := os.ReadFile(filepath.Join(r.layout.StateDir, MasterPublicKeyFile))
	if err != nil {
		if os.IsNotExist(err) {
			return wireguard.KeyPair{}, ErrNoMasterKeys
		}
		return wireguard.KeyPair{}, err
	}
	return wireguard.KeyPair{
		PrivateKey: trim(string(priv)),
		PublicKey:  trim(string(pub)),
	}, nil
}

// EnsureMasterKeys returns the master's WireGuard key pair, generating and
// persisting one on first use. Subsequent calls return the same pair without
// regenerating.
func (r Registry) EnsureMasterKeys() (wireguard.KeyPair, error) {
	pair, err := r.MasterKeys()
	if err == nil {
		return pair, nil
	}
	if err != ErrNoMasterKeys {
		return wireguard.KeyPair{}, err
	}
	pair, err = wireguard.GenerateKeyPair()
	if err != nil {
		return wireguard.KeyPair{}, err
	}
	if err := os.MkdirAll(r.layout.StateDir, 0o700); err != nil {
		return wireguard.KeyPair{}, err
	}
	if err := os.WriteFile(filepath.Join(r.layout.StateDir, MasterPrivateKeyFile), []byte(pair.PrivateKey+"\n"), 0o600); err != nil {
		return wireguard.KeyPair{}, err
	}
	if err := os.WriteFile(filepath.Join(r.layout.StateDir, MasterPublicKeyFile), []byte(pair.PublicKey+"\n"), 0o644); err != nil {
		return wireguard.KeyPair{}, err
	}
	return pair, nil
}

// DeleteMasterKeys removes the master key pair from disk. Used by uninstall
// when tearing down the entire cluster.
func (r Registry) DeleteMasterKeys() error {
	for _, name := range []string{MasterPrivateKeyFile, MasterPublicKeyFile} {
		if err := os.Remove(filepath.Join(r.layout.StateDir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// ErrNoMasterKeys is returned when MasterKeys is called before the cluster
// has been initialised.
var ErrNoMasterKeys = fmt.Errorf("master WireGuard keys not initialised")

func trim(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r' || s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}
