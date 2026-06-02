// Package system performs OS detection and plans/executes the privileged
// commands needed to install and manage singbox-deploy. Command execution is
// behind the Runner interface so tests use a recording fake and production uses
// a real exec runner.
package system

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Command is a single program invocation, kept as name+args so it can be both
// rendered for display and executed without a shell.
type Command struct {
	Name string
	Args []string
	// Env holds extra environment entries ("KEY=value") for this command, used
	// e.g. to pass repo-setup variables. The process inherits the parent
	// environment plus these.
	Env []string
}

// String renders the command as a human-readable line for the UI/logs.
func (c Command) String() string {
	return strings.TrimSpace(c.Name + " " + strings.Join(c.Args, " "))
}

// Runner executes commands. Implementations may record (tests) or exec (prod).
type Runner interface {
	Run(Command) error
}

// ExecRunner runs commands with os/exec, streaming combined stdout/stderr to
// Output so the UI can show live progress. A nil Output discards command output.
type ExecRunner struct {
	Output io.Writer
	ctx    context.Context
}

// NewExecRunner returns an ExecRunner writing command output to out.
func NewExecRunner(out io.Writer) *ExecRunner {
	return &ExecRunner{Output: out, ctx: context.Background()}
}

// WithContext returns a copy of the runner bound to ctx for cancellation.
func (r *ExecRunner) WithContext(ctx context.Context) *ExecRunner {
	cp := *r
	cp.ctx = ctx
	return &cp
}

// Run executes the command, streaming its output.
func (r *ExecRunner) Run(c Command) error {
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if len(c.Env) > 0 {
		cmd.Env = append(cmd.Environ(), c.Env...)
	}
	if r.Output != nil {
		cmd.Stdout = r.Output
		cmd.Stderr = r.Output
	}
	return cmd.Run()
}

// DryRunRunner prints commands instead of executing them. It is used by the UI
// dry-run mode so operators can review the exact system interactions first.
type DryRunRunner struct {
	Output    io.Writer
	OnCommand func(Command)
}

// NewDryRunRunner returns a runner that writes commands to out and never
// invokes the operating system.
func NewDryRunRunner(out io.Writer) *DryRunRunner {
	return &DryRunRunner{Output: out}
}

// Run prints c and reports success without executing it.
func (r *DryRunRunner) Run(c Command) error {
	if r.OnCommand != nil {
		r.OnCommand(c)
	}
	if r.Output == nil {
		return nil
	}
	_, err := fmt.Fprint(r.Output, prefixDryRunLines(c.String()))
	return err
}

func prefixDryRunLines(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, line := range lines {
		lines[i] = "[dry-run] " + line
	}
	return strings.Join(lines, "\n") + "\n"
}
