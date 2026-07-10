// Package agentbin embeds the spoke agent binaries into the hub binary so a
// spoke can be provisioned fully offline over SSH. The embedded binaries are
// generated in this directory and gitignored; scripts/build.sh (and the release
// workflow) cross-compiles them before the hub is built.
package agentbin

import (
	_ "embed"
	"fmt"
)

//go:embed singbox-deploy-agent-linux-amd64
var agentAMD64 []byte

//go:embed singbox-deploy-agent-linux-arm64
var agentARM64 []byte

// Binary returns the embedded agent binary for the given normalized arch
// ("amd64" or "arm64").
func Binary(arch string) ([]byte, error) {
	switch arch {
	case "amd64":
		return agentAMD64, nil
	case "arm64":
		return agentARM64, nil
	default:
		return nil, fmt.Errorf("no embedded agent for architecture %q", arch)
	}
}
