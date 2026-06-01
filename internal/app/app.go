// Package app exposes static metadata about the singbox-deploy program.
package app

// Info holds invariant facts about where the program lives on disk.
type Info struct {
	Name       string
	ConfigRoot string
	BinaryPath string
}

// Metadata returns the canonical program metadata.
func Metadata() Info {
	return Info{
		Name:       "singbox-deploy",
		ConfigRoot: "/etc/singbox-deploy",
		BinaryPath: "/usr/bin/singbox-deploy",
	}
}
