package ui

import (
	"fmt"

	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// Shared host gating for the management screens, which all require a detected,
// supported host running as root without SELinux enforcing.

// hostCanApply reports whether management actions can run on the detected host.
func hostCanApply(host system.Host, hostErr error) bool {
	return hostErr == nil && host.IsRoot && host.Supported() && !host.SELinux
}

// hostApplyBlocker returns the reason hostCanApply is false: rootMsg when not
// running as root, selinuxMsg when SELinux is enforcing, fallback otherwise.
func hostApplyBlocker(host system.Host, hostErr error, rootMsg, selinuxMsg, fallback string) string {
	if hostErr != nil {
		return "failed to detect host: " + hostErr.Error()
	}
	if !host.IsRoot {
		return rootMsg
	}
	if !host.Supported() {
		return fmt.Sprintf("unsupported system: family=%q arch=%q", host.OS.Family, host.Arch)
	}
	if host.SELinux {
		return selinuxMsg
	}
	return fallback
}
