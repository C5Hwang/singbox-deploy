package install

import (
	"os"
	"path/filepath"
	"strconv"

	"github.com/C5Hwang/singbox-deploy/internal/subscription"
)

// writeFile creates parent directories and writes data with the given mode.
func writeFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, perm)
}

func writeStateFile(stateDir, name, value string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, filepath.Clean(name)), []byte(value), 0o600)
}

// SubscriptionToken derives the subscription URL token from the salt.
func SubscriptionToken(salt string) string {
	return subscription.TokenFromSalt(salt)
}

// subscriptionToken is the internal alias used by the orchestrator.
func subscriptionToken(salt string) string { return SubscriptionToken(salt) }

func itoa(n int) string { return strconv.Itoa(n) }
