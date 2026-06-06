package deploy

import (
	"context"
	"path/filepath"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func (o *Orchestrator) checkConflicts(_ context.Context, _ Config) error {
	var servicePaths []string
	if o.SystemdDir != "" && o.SystemdDir != "/etc/systemd/system" {
		servicePaths = []string{filepath.Join(o.SystemdDir, system.SingBoxService)}
	}
	return system.SingBoxConflictCheck{
		ServicePaths:   servicePaths,
		ExpectedBinary: o.Layout.SingBoxBin,
		ExpectedConfig: o.Layout.ConfigJSON,
	}.Check()
}

func (o *Orchestrator) checkPorts(ctx context.Context, cfg Config) error {
	return system.CheckPorts(ctx, cfg.Domain, cfg.portChecks())
}
