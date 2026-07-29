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
	Detail     string
	Generation spokeRegistryGeneration
	Apply      func(*nodes.Node) error
	// Restore reverts only fields that still equal applied. If another Hub
	// process changed an owned field after this transaction's registry commit,
	// that concurrent winner must not be overwritten during rollback.
	Restore func(current *nodes.Node, original, applied nodes.Node)
}

type spokeRegistryGeneration int

const (
	spokeRegistryGenerationNone spokeRegistryGeneration = iota
	spokeRegistryGenerationProtocol
	spokeRegistryGenerationSubscription
)

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
	if !validSpokeRegistryGeneration(change.Generation) {
		return fmt.Errorf("spoke registry change generation is required")
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
		generation := spokeChangeGeneration(*current, change.Generation)
		if generation == ^uint64(0) {
			return fmt.Errorf("spoke settings generation is exhausted")
		}
		setSpokeChangeGeneration(current, change.Generation, generation+1)
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
		restored := cloneSpokeNode(original)
		rollbackStateErr := nodes.Mutate(layout, nodeID, func(current *nodes.Node) error {
			if spokeChangeGeneration(*current, change.Generation) ==
				spokeChangeGeneration(updated, change.Generation) {
				change.Restore(current, original, updated)
			}
			// Preserve fields owned by concurrent settings/status operations in
			// the remote rollback request as well as in the Hub registry. Using
			// the stale pre-apply snapshot here would otherwise make the two
			// sides diverge after an otherwise successful rollback.
			restored = cloneSpokeNode(*current)
			return nil
		})
		rollbackCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		rollbackRemoteErr := rollbackRemote(rollbackCtx, restored, logs)
		cancel()
		if rollbackStateErr != nil || rollbackRemoteErr != nil {
			return fmt.Errorf("apply spoke settings over WireGuard: %w (rollback state: %v; rollback spoke: %v)", err, rollbackStateErr, rollbackRemoteErr)
		}
		return fmt.Errorf("apply spoke settings over WireGuard: %w (previous settings restored)", err)
	}
	return nil
}

func spokeChangeGeneration(node nodes.Node, generation spokeRegistryGeneration) uint64 {
	switch generation {
	case spokeRegistryGenerationProtocol:
		return node.ProtocolSettingsGeneration
	case spokeRegistryGenerationSubscription:
		return node.SubscriptionSettingsGeneration
	default:
		return 0
	}
}

func validSpokeRegistryGeneration(generation spokeRegistryGeneration) bool {
	switch generation {
	case spokeRegistryGenerationProtocol, spokeRegistryGenerationSubscription:
		return true
	default:
		return false
	}
}

func setSpokeChangeGeneration(node *nodes.Node, generation spokeRegistryGeneration, value uint64) {
	switch generation {
	case spokeRegistryGenerationProtocol:
		node.ProtocolSettingsGeneration = value
	case spokeRegistryGenerationSubscription:
		node.SubscriptionSettingsGeneration = value
	}
}

func cloneSpokeNode(node nodes.Node) nodes.Node {
	node.EnabledProtocols = append([]string(nil), node.EnabledProtocols...)
	return node
}
