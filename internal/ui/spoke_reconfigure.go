package ui

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

// spokeRegistryChange describes the registry fields owned by one settings
// screen. Restore must only replace those fields so unrelated status updates
// recorded while the Agent request is in flight are retained.
type spokeRegistryChange struct {
	Detail  string
	Apply   func(*nodes.Node) error
	Restore func(*nodes.Node, nodes.Node)
}

type spokeReconfigureFunc func(context.Context, nodes.Node, io.Writer) error

// applySpokeRegistryReconfigure persists a settings change by stable node ID,
// applies it through the authenticated WireGuard Agent path, and restores both
// the registry fields and the previous remote configuration if that apply
// fails. The registry callbacks complete before any Agent call, so a nodes
// transaction is never nested around Controller.Reconfigure.
func applySpokeRegistryReconfigure(
	ctx context.Context,
	layout paths.Layout,
	nodeID string,
	logs io.Writer,
	progress func(deploy.Event),
	change spokeRegistryChange,
	applyRemote, rollbackRemote spokeReconfigureFunc,
) error {
	if change.Apply == nil || change.Restore == nil {
		return fmt.Errorf("spoke registry change is incomplete")
	}
	if applyRemote == nil || rollbackRemote == nil {
		return fmt.Errorf("spoke reconfigure callback is required")
	}

	var original, updated nodes.Node
	registryEvent := deploy.Event{
		Index: 1, Total: 5, Label: "Registry settings",
		Detail: change.Detail, Status: "running",
	}
	deploy.EmitProgress(progress, registryEvent)
	if err := nodes.Mutate(layout, nodeID, func(current *nodes.Node) error {
		original = cloneSpokeNode(*current)
		if err := change.Apply(current); err != nil {
			return err
		}
		updated = cloneSpokeNode(*current)
		return nil
	}); err != nil {
		registryEvent.Status = "fail"
		registryEvent.Err = err
		deploy.EmitProgress(progress, registryEvent)
		return err
	}
	registryEvent.Status = "ok"
	deploy.EmitProgress(progress, registryEvent)

	if err := applyRemote(ctx, updated, logs); err != nil {
		rollbackStateErr := nodes.Mutate(layout, nodeID, func(current *nodes.Node) error {
			change.Restore(current, original)
			return nil
		})
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		rollbackRemoteErr := rollbackRemote(rollbackCtx, original, logs)
		cancel()
		if rollbackStateErr != nil || rollbackRemoteErr != nil {
			return fmt.Errorf("apply spoke settings over WireGuard: %w (rollback state: %v; rollback spoke: %v)", err, rollbackStateErr, rollbackRemoteErr)
		}
		return fmt.Errorf("apply spoke settings over WireGuard: %w (previous settings restored)", err)
	}
	return nil
}

func cloneSpokeNode(node nodes.Node) nodes.Node {
	node.EnabledProtocols = append([]string(nil), node.EnabledProtocols...)
	return node
}
