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

// subscriptionToken derives the subscription URL token from the salt.
func subscriptionToken(salt string) string {
	return subscription.TokenFromSalt(salt)
}

func itoa(n int) string { return strconv.Itoa(n) }
