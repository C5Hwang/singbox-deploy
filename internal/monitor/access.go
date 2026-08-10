package monitor

import (
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// AccessTokenFile is the state file the monitor service reads its API token
// from. The token deliberately stays out of the systemd unit: an ExecStart line
// is readable by anyone who can run `systemctl cat` or see the process table.
const AccessTokenFile = "monitor_token"

// ReadAccessToken returns the monitor API token recorded under stateDir. A
// missing or unreadable file reads as an empty token, which leaves the API
// open — the state every installation made before the token existed is in.
func ReadAccessToken(stateDir string) string {
	value, err := state.NewStore(stateDir).ReadValue(AccessTokenFile, false)
	if err != nil {
		return ""
	}
	return value
}

// WriteAccessToken persists the monitor API token. Monitor settings are saved
// only after the service has been restarted, so the token needs its own write
// ahead of that restart; the install state write later repeats the same value.
func WriteAccessToken(stateDir, token string) error {
	return state.NewStore(stateDir).WriteString(AccessTokenFile, strings.TrimSpace(token)+"\n", 0o600)
}
