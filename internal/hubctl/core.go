package hubctl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
)

// FleetCoreStatus is one authenticated preflight observation used by a
// coordinated sing-box core change.
type FleetCoreStatus struct {
	HubVersion string
	Spokes     []FleetCoreSpokeStatus
}

// FleetCoreSpokeStatus identifies the exact version a spoke reported.
type FleetCoreSpokeStatus struct {
	Node    nodes.Node
	Version string
	Active  bool
}

// ChangeFleetCore converges the Hub and every installed Spoke on one exact
// stable sing-box release. Spokes change first and the Hub commits last. Any
// failure triggers a best-effort reverse-order rollback to each node's exact
// preflight version; rollback errors are joined to the original failure so
// possible drift is never hidden.
func (c *Controller) ChangeFleetCore(ctx context.Context, target string, log io.Writer) error {
	c.defaults()
	if log == nil {
		log = io.Discard
	}
	target = strings.TrimSpace(target)
	if err := nodeapi.ValidateStableSingBoxTag(target); err != nil {
		return err
	}

	list, err := nodes.Load(c.Layout)
	if err != nil {
		return fmt.Errorf("load spoke registry: %w", err)
	}
	installed := make([]nodes.Node, 0, len(list))
	for _, node := range list {
		if node.Installed {
			installed = append(installed, node)
		}
	}
	total := len(installed) + 2
	emitFleetCoreProgress(c.Progress, 1, total, "Fleet preflight",
		"verify exact Hub and Spoke core versions", "running", nil)
	status, err := c.inspectFleetCore(ctx, installed, log)
	if err != nil {
		emitFleetCoreProgress(c.Progress, 1, total, "Fleet preflight",
			"verify exact Hub and Spoke core versions", "fail", err)
		return err
	}
	emitFleetCoreProgress(c.Progress, 1, total, "Fleet preflight",
		"verify exact Hub and Spoke core versions", "ok", nil)

	type changedSpoke struct {
		node nodes.Node
		old  string
	}
	changed := make([]changedSpoke, 0, len(status.Spokes))
	rollback := func(cause error, localMayHaveChanged bool) error {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		var rollbackErrs []error

		if localMayHaveChanged {
			current, versionErr := c.CurrentCoreVersion(recoveryCtx)
			switch {
			case versionErr != nil:
				rollbackErrs = append(rollbackErrs,
					fmt.Errorf("inspect Hub core during rollback: %w", versionErr))
			case current != status.HubVersion:
				fmt.Fprintf(log, "rolling Hub core back to %s...\n", status.HubVersion)
				if restoreErr := c.ChangeLocalCore(recoveryCtx, status.HubVersion, log); restoreErr != nil {
					rollbackErrs = append(rollbackErrs,
						fmt.Errorf("roll back Hub core to %s: %w", status.HubVersion, restoreErr))
				} else if verifyErr := c.verifyLocalCore(recoveryCtx, status.HubVersion); verifyErr != nil {
					rollbackErrs = append(rollbackErrs,
						fmt.Errorf("verify Hub core rollback: %w", verifyErr))
				}
			}
		}

		for i := len(changed) - 1; i >= 0; i-- {
			item := changed[i]
			fmt.Fprintf(log, "rolling %s core back to %s...\n", item.node.EffectiveAlias(), item.old)
			client := c.NewClient(item.node)
			if restoreErr := client.ChangeCore(recoveryCtx, nodeapi.CoreRequest{
				SingBoxVersion: item.old,
			}, log); restoreErr != nil {
				rollbackErrs = append(rollbackErrs,
					fmt.Errorf("roll back %s core to %s: %w",
						item.node.EffectiveAlias(), item.old, restoreErr))
				continue
			}
			if _, verifyErr := c.verifySpokeCore(recoveryCtx, item.node, item.old); verifyErr != nil {
				rollbackErrs = append(rollbackErrs,
					fmt.Errorf("verify %s core rollback: %w", item.node.EffectiveAlias(), verifyErr))
			}
		}
		if len(rollbackErrs) == 0 {
			fmt.Fprintln(log, "fleet core rollback restored every changed node")
		}
		return errors.Join(cause, errors.Join(rollbackErrs...))
	}

	for i, spoke := range status.Spokes {
		index := i + 2
		label := "Spoke core · " + spoke.Node.EffectiveAlias()
		detail := fmt.Sprintf("converge %s to %s", spoke.Version, target)
		emitFleetCoreProgress(c.Progress, index, total, label, detail, "running", nil)
		if spoke.Version == target {
			emitFleetCoreProgress(c.Progress, index, total, label, "already on "+target, "ok", nil)
			continue
		}

		// Record the old version before sending the mutation. If the response is
		// lost after the Agent commits, rollback still repairs the possibly
		// changed node instead of assuming the failed request was side-effect
		// free.
		changed = append(changed, changedSpoke{node: spoke.Node, old: spoke.Version})
		client := c.NewClient(spoke.Node)
		if err := client.ChangeCore(ctx, nodeapi.CoreRequest{SingBoxVersion: target}, log); err != nil {
			stepErr := fmt.Errorf("change %s core to %s: %w", spoke.Node.EffectiveAlias(), target, err)
			emitFleetCoreProgress(c.Progress, index, total, label, detail, "fail", stepErr)
			return rollback(stepErr, false)
		}
		verified, err := c.verifySpokeCore(ctx, spoke.Node, target)
		if err != nil {
			stepErr := fmt.Errorf("verify %s core after change: %w", spoke.Node.EffectiveAlias(), err)
			emitFleetCoreProgress(c.Progress, index, total, label, detail, "fail", stepErr)
			return rollback(stepErr, false)
		}
		spoke.Node = verified
		changed[len(changed)-1].node = verified
		emitFleetCoreProgress(c.Progress, index, total, label, target+" active", "ok", nil)
	}

	hubIndex := total
	hubDetail := fmt.Sprintf("converge %s to %s", status.HubVersion, target)
	emitFleetCoreProgress(c.Progress, hubIndex, total, "Hub core", hubDetail, "running", nil)
	if status.HubVersion != target {
		if err := c.ChangeLocalCore(ctx, target, log); err != nil {
			stepErr := fmt.Errorf("change Hub core to %s: %w", target, err)
			emitFleetCoreProgress(c.Progress, hubIndex, total, "Hub core", hubDetail, "fail", stepErr)
			return rollback(stepErr, true)
		}
	}
	if err := c.verifyLocalCore(ctx, target); err != nil {
		stepErr := fmt.Errorf("verify Hub core after change: %w", err)
		emitFleetCoreProgress(c.Progress, hubIndex, total, "Hub core", hubDetail, "fail", stepErr)
		return rollback(stepErr, true)
	}
	emitFleetCoreProgress(c.Progress, hubIndex, total, "Hub core", target+" active", "ok", nil)
	fmt.Fprintf(log, "Hub and %d installed spoke(s) now run sing-box %s\n", len(status.Spokes), target)
	return nil
}

func (c *Controller) inspectFleetCore(ctx context.Context, installed []nodes.Node, log io.Writer) (FleetCoreStatus, error) {
	hubVersion, err := c.CurrentCoreVersion(ctx)
	if err != nil {
		return FleetCoreStatus{}, fmt.Errorf("read Hub sing-box version: %w", err)
	}
	if err := nodeapi.ValidateStableSingBoxTag(hubVersion); err != nil {
		return FleetCoreStatus{}, fmt.Errorf("Hub sing-box version %q is not an exact stable tag: %w", hubVersion, err)
	}
	if err := c.LocalCoreActive(); err != nil {
		return FleetCoreStatus{}, fmt.Errorf("Hub sing-box service is not active: %w", err)
	}
	status := FleetCoreStatus{HubVersion: hubVersion, Spokes: make([]FleetCoreSpokeStatus, 0, len(installed))}
	for _, node := range installed {
		checked, err := c.CheckHealth(ctx, node, log)
		if err != nil {
			return FleetCoreStatus{}, fmt.Errorf("preflight %s: %w", node.EffectiveAlias(), err)
		}
		health, err := c.NewClient(checked).Health(ctx)
		if err != nil {
			return FleetCoreStatus{}, fmt.Errorf("read %s core version: %w", checked.EffectiveAlias(), err)
		}
		if !health.OK {
			return FleetCoreStatus{}, fmt.Errorf("agent %s reported unhealthy%s",
				checked.EffectiveAlias(), healthErrorSuffix(health))
		}
		if !health.Installed {
			return FleetCoreStatus{}, fmt.Errorf("registry marks %s installed but its Agent reports no deployment",
				checked.EffectiveAlias())
		}
		if err := nodeapi.ValidateStableSingBoxTag(health.SingBoxVersion); err != nil {
			return FleetCoreStatus{}, fmt.Errorf("%s reports invalid sing-box version %q: %w",
				checked.EffectiveAlias(), health.SingBoxVersion, err)
		}
		if !health.SingBoxActive {
			return FleetCoreStatus{}, fmt.Errorf("%s sing-box service is not active; release any traffic quota stop before changing the fleet core",
				checked.EffectiveAlias())
		}
		status.Spokes = append(status.Spokes, FleetCoreSpokeStatus{
			Node: checked, Version: health.SingBoxVersion, Active: true,
		})
	}
	return status, nil
}

func (c *Controller) verifySpokeCore(ctx context.Context, node nodes.Node, expected string) (nodes.Node, error) {
	health, err := c.NewClient(node).Health(ctx)
	if err != nil {
		return node, err
	}
	if !health.OK {
		return node, fmt.Errorf("agent reported unhealthy%s", healthErrorSuffix(health))
	}
	if !health.Installed {
		return node, fmt.Errorf("agent reports no installed deployment")
	}
	if health.SingBoxVersion != expected {
		return node, fmt.Errorf("reports %q, expected %q", health.SingBoxVersion, expected)
	}
	if !health.SingBoxActive {
		return node, fmt.Errorf("sing-box service is not active")
	}
	if err := c.persistAgentHealth(&node, health); err != nil {
		return node, fmt.Errorf("persist verified core health: %w", err)
	}
	return node, nil
}

func (c *Controller) verifyLocalCore(ctx context.Context, expected string) error {
	version, err := c.CurrentCoreVersion(ctx)
	if err != nil {
		return err
	}
	if version != expected {
		return fmt.Errorf("reports %q, expected %q", version, expected)
	}
	if err := c.LocalCoreActive(); err != nil {
		return fmt.Errorf("sing-box service is not active: %w", err)
	}
	return nil
}

func emitFleetCoreProgress(progress func(deploy.Event), index, total int, label, detail, status string, err error) {
	deploy.EmitProgress(progress, deploy.Event{
		Index: index, Total: total, Label: label, Detail: detail, Status: status, Err: err,
	})
}
