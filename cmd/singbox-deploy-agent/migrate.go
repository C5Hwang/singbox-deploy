package main

import (
	"context"

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
