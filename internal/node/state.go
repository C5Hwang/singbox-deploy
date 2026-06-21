// Package node implements the persistent agent that runs on every remote
// node. It serves the master-facing HTTP API on the WireGuard interface and
// applies config/cert/upgrade requests locally by reusing the existing
// internal/deploy and internal/config packages.
package node

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

const (
	// APIPort is the well-known port the node agent listens on, bound to
	// the WireGuard interface.
	APIPort = 19091

	// agentTokenFile holds the shared bearer secret the master uses to call
	// the agent's API.
	agentTokenFile = "agent_api_token"

	// agentWGIPFile holds the WireGuard IP the agent should bind to, written
	// during initial provisioning.
	agentWGIPFile = "agent_wg_ip"

	// agentMasterPubKeyFile holds the master's WireGuard public key so the
	// node can rewrite its own wg-quick config without contacting the master.
	agentMasterPubKeyFile = "agent_master_public_key"

	// agentMasterEndpointFile holds the master's reachable host:port for
	// WireGuard handshakes.
	agentMasterEndpointFile = "agent_master_endpoint"
)

// AgentState holds the small set of values an agent needs to bootstrap itself
// independently of the master.
type AgentState struct {
	APIToken          string
	WGIP              string
	MasterPublicKey   string
	MasterEndpoint    string
}

// LoadAgentState reads the agent's persisted state.
func LoadAgentState(layout paths.Layout) (AgentState, error) {
	state := AgentState{
		APIToken:        readString(layout.StateDir, agentTokenFile),
		WGIP:            readString(layout.StateDir, agentWGIPFile),
		MasterPublicKey: readString(layout.StateDir, agentMasterPubKeyFile),
		MasterEndpoint:  readString(layout.StateDir, agentMasterEndpointFile),
	}
	if state.APIToken == "" {
		return AgentState{}, fmt.Errorf("agent api token not configured (run setup first)")
	}
	if state.WGIP == "" {
		return AgentState{}, fmt.Errorf("agent WireGuard IP not configured (run setup first)")
	}
	return state, nil
}

// SaveAgentState writes the agent state to its small-file backing store.
// Empty fields skip writing so this can be used to set values incrementally.
func SaveAgentState(layout paths.Layout, state AgentState) error {
	if err := os.MkdirAll(layout.StateDir, 0o700); err != nil {
		return err
	}
	values := map[string]string{
		agentTokenFile:          state.APIToken,
		agentWGIPFile:           state.WGIP,
		agentMasterPubKeyFile:   state.MasterPublicKey,
		agentMasterEndpointFile: state.MasterEndpoint,
	}
	for name, value := range values {
		if value == "" {
			continue
		}
		path := filepath.Join(layout.StateDir, name)
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// DeleteAgentState removes every agent state file. Called by Teardown.
func DeleteAgentState(layout paths.Layout) error {
	for _, name := range []string{agentTokenFile, agentWGIPFile, agentMasterPubKeyFile, agentMasterEndpointFile} {
		if err := os.Remove(filepath.Join(layout.StateDir, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func readString(dir, name string) string {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
