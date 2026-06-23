package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/sshexec"
)

// validateSSHReachable opens a one-shot SSH session against target with the
// supplied auth and immediately closes it. Surfaces transport and
// authentication failures so the operator can fix bad input before the
// add-node orchestrator starts the long provisioning run.
func validateSSHReachable(ctx context.Context, target sshexec.Target, auth sshexec.Auth) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	client, err := sshexec.Dial(ctx, target, auth)
	if err != nil {
		return fmt.Errorf("ssh check failed: %w", err)
	}
	_ = client.Close()
	return nil
}
