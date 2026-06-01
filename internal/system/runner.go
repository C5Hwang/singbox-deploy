// Package system performs OS detection and plans/executes the privileged
// commands needed to install and manage singbox-deploy. Command execution is
// behind the Runner interface so tests use a recording fake and production uses
// a real exec runner.
package system

import "strings"

// Command is a single program invocation, kept as name+args so it can be both
// rendered for display and executed without a shell.
type Command struct {
	Name string
	Args []string
}

// String renders the command as a human-readable line for the UI/logs.
func (c Command) String() string {
	return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
}

// Runner executes commands. Implementations may record (tests) or exec (prod).
type Runner interface {
	Run(Command) error
}
