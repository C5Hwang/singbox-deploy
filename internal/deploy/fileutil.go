package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// WriteFile creates parent directories and writes data with the given mode.
func WriteFile(path string, data []byte, perm os.FileMode) error {
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

// RunCommands executes system commands sequentially, stopping at the first error.
func RunCommands(r system.Runner, cmds ...system.Command) error {
	for _, c := range cmds {
		if err := r.Run(c); err != nil {
			return fmt.Errorf("command %q: %w", c.String(), err)
		}
	}
	return nil
}

// EmitProgress reports a progress event if a progress callback is set.
func EmitProgress(progress func(Event), e Event) {
	if progress != nil {
		progress(e)
	}
}
