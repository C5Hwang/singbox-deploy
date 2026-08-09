package deploy

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// RemoveLegacySubscribeToken deletes the derived subscription token that older
// releases persisted next to the salt. Nothing has read it since the status page
// moved to subscription groups, and it is only md5(salt + newline), recomputed
// by SubscriptionToken wherever it is actually needed. Keeping a second copy of
// a URL secret on disk — one that goes stale the moment a group rotates its salt
// — buys nothing, so both the Hub and the Agent drop it at startup.
func RemoveLegacySubscribeToken(layout paths.Layout) error {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	if err := os.Remove(filepath.Join(layout.StateDir, "subscribe_token")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy subscription token: %w", err)
	}
	return nil
}
