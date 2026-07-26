package deploy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestRunStepsStopsBeforeNextStepWhenContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ranSecond bool
	err := RunSteps(ctx, nil, []Step{
		{
			Label: "first",
			Run: func(context.Context) error {
				cancel()
				return nil
			},
		},
		{
			Label: "second",
			Run: func(context.Context) error {
				ranSecond = true
				return nil
			},
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("RunSteps error = %v, want context.Canceled", err)
	}
	if ranSecond {
		t.Fatal("RunSteps started a new mutation step after cancellation")
	}
}

func TestPublicWritersRepairLayoutRootPermissions(t *testing.T) {
	tests := []struct {
		name string
		run  func(paths.Layout, Config) error
	}{
		{
			name: "subscriptions",
			run: func(layout paths.Layout, cfg Config) error {
				return WriteSubscriptions(layout, cfg)
			},
		},
		{
			name: "nginx config",
			run: func(layout paths.Layout, cfg Config) error {
				return WriteManagedNginxConfig(layout, cfg, filepath.Join(layout.Root, "nginx.conf"))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout := paths.LayoutForRoot(filepath.Join(t.TempDir(), "singbox-deploy"))
			if err := os.MkdirAll(layout.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(layout.Root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(layout.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}

			if err := tt.run(layout, testConfig(t)); err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]os.FileMode{
				layout.Root:     0o755,
				layout.StateDir: 0o700,
			} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if got := info.Mode().Perm(); got != want {
					t.Fatalf("%s mode = %#o, want %#o", path, got, want)
				}
			}
		})
	}
}
