package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/subscription"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// WriteFile creates parent directories and writes data with the given mode.
// The mode is enforced even when the file already exists (os.WriteFile alone
// keeps the old mode), so permission tightening reaches existing installs.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, perm); err != nil {
		return err
	}
	return os.Chmod(path, perm)
}

func writeStateFile(stateDir, name, value string) error {
	return state.NewStore(stateDir).WriteString(name, value, 0o600)
}

// SubscriptionToken derives the subscription URL token from the salt.
func SubscriptionToken(salt string) string {
	return subscription.TokenFromSalt(salt)
}

func itoa(n int) string { return strconv.Itoa(n) }

// RunCommands executes system commands sequentially, stopping at the first error.
func RunCommands(r system.Runner, cmds ...system.Command) error {
	for _, c := range cmds {
		if err := r.Run(c); err != nil {
			return fmt.Errorf("command %q: %w", c.String(), err)
		}
	}
	return nil
}

// Step is one labeled unit of work executed by RunSteps.
type Step struct {
	Label  string
	Detail string
	Run    func(context.Context) error
}

// RunSteps executes steps in order, emitting running/ok/fail progress events.
// It stops at the first failing step and returns its error wrapped with the
// step label.
func RunSteps(ctx context.Context, progress func(Event), steps []Step) error {
	total := len(steps)
	for i, s := range steps {
		if err := ctx.Err(); err != nil {
			return err
		}
		EmitProgress(progress, Event{Index: i + 1, Total: total, Label: s.Label, Detail: s.Detail, Status: "running"})
		if err := s.Run(ctx); err != nil {
			EmitProgress(progress, Event{Index: i + 1, Total: total, Label: s.Label, Detail: s.Detail, Status: "fail", Err: err})
			return fmt.Errorf("%s: %w", s.Label, err)
		}
		EmitProgress(progress, Event{Index: i + 1, Total: total, Label: s.Label, Detail: s.Detail, Status: "ok"})
	}
	return nil
}

// EmitProgress reports a progress event if a progress callback is set.
func EmitProgress(progress func(Event), e Event) {
	if progress != nil {
		progress(e)
	}
}
