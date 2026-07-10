package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

// TeardownAll removes every registered spoke and then brings the hub's
// WireGuard interface down. It is deliberately fail-closed: if any spoke does
// not acknowledge removal, the registry and overlay stay available so the
// operator can retry instead of stranding a live agent with no control path.
func (c *Controller) TeardownAll(ctx context.Context, log io.Writer) error {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	list, err := nodes.Load(c.Layout)
	if err != nil {
		return err
	}
	var errs []error
	for _, n := range list {
		fmt.Fprintf(log, "removing spoke %s...\n", n.EffectiveAlias())
		if err := c.RemoveNode(ctx, n, log); err != nil {
			fmt.Fprintf(log, "warning: removing %s failed: %v\n", n.EffectiveAlias(), err)
			errs = append(errs, fmt.Errorf("remove %s: %w", n.EffectiveAlias(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("spoke teardown incomplete; hub overlay retained: %w", errors.Join(errs...))
	}
	// Bring the overlay down and remove its config so a reinstall starts clean.
	mgr := c.wgManager()
	configPath := mgr.ConfigPath(wgnet.InterfaceName)
	_, identityPresent, identityErr := nodes.LoadHubIdentity(c.Layout)
	if identityErr != nil {
		errs = append(errs, fmt.Errorf("inspect hub overlay identity: %w", identityErr))
	}
	_, configErr := os.Stat(configPath)
	configPresent := configErr == nil
	if configErr != nil && !os.IsNotExist(configErr) {
		errs = append(errs, fmt.Errorf("inspect hub overlay config: %w", configErr))
	}
	// On a never-installed Hub there is no wg-quick unit/config to disable;
	// systemd reports that as an error, but uninstall should remain a no-op.
	if identityPresent || configPresent {
		if err := mgr.DisableStop(wgnet.InterfaceName); err != nil {
			errs = append(errs, fmt.Errorf("stop hub overlay: %w", err))
		}
	}
	if err := os.Remove(configPath); err != nil && !os.IsNotExist(err) {
		errs = append(errs, fmt.Errorf("remove hub overlay config: %w", err))
	}
	if err := nodes.SetHubInstalled(c.Layout, false); err != nil {
		errs = append(errs, fmt.Errorf("clear hub installed state: %w", err))
	}
	return errors.Join(errs...)
}
