package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/submigration"
)

func migrateAgentSubscriptions(ctx context.Context, layout paths.Layout) (bool, error) {
	return submigration.EnsureCurrent(ctx, layout, func(context.Context) error {
		cfg, err := deploy.LoadProtocolConfig(layout)
		if err != nil {
			return err
		}
		return deploy.WriteSubscriptions(layout, cfg)
	})
}

// removeLegacyAgentACMEEmail cleans the flat contact file that older
// standalone installs could leave behind after they had become spokes.
func removeLegacyAgentACMEEmail(layout paths.Layout) error {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	if err := os.Remove(filepath.Join(layout.StateDir, "email")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove legacy ACME email: %w", err)
	}
	return nil
}
