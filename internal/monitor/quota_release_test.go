package monitor

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

func writeQuotaStopMarker(t *testing.T, dbPath string, stopped bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("create monitor store directory: %v", err)
	}
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open monitor store: %v", err)
	}
	if err := store.SetQuotaStopped(stopped); err != nil {
		store.Close()
		t.Fatalf("set quota stop marker: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close monitor store: %v", err)
	}
}

func readQuotaStopMarker(t *testing.T, dbPath string) bool {
	t.Helper()
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("open monitor store: %v", err)
	}
	defer store.Close()
	stopped, err := store.QuotaStopped()
	if err != nil {
		t.Fatalf("read quota stop marker: %v", err)
	}
	return stopped
}

func TestQuotaStopStateReportsMarkerWithoutCreatingStore(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monitor", "monitor.db")
	stopped, err := QuotaStopState(dbPath)
	if err != nil || stopped {
		t.Fatalf("QuotaStopState(missing store) = %v, %v; want false, nil", stopped, err)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing monitor store was created: %v", err)
	}
	writeQuotaStopMarker(t, dbPath, true)
	if stopped, err = QuotaStopState(dbPath); err != nil || !stopped {
		t.Fatalf("QuotaStopState(marker set) = %v, %v; want true, nil", stopped, err)
	}
	writeQuotaStopMarker(t, dbPath, false)
	if stopped, err = QuotaStopState(dbPath); err != nil || stopped {
		t.Fatalf("QuotaStopState(marker cleared) = %v, %v; want false, nil", stopped, err)
	}
}

func TestReleaseQuotaStopDoesNotCreateMissingStoreOrStartService(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "monitor", "monitor.db")
	starts := 0
	if err := ReleaseQuotaStop(dbPath, func() error {
		starts++
		return nil
	}); err != nil {
		t.Fatalf("release quota stop: %v", err)
	}
	if starts != 0 {
		t.Fatalf("sing-box starts = %d, want 0", starts)
	}
	if _, err := os.Stat(dbPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing monitor store was created: %v", err)
	}
}

func TestReleaseQuotaStopMarkerSemantics(t *testing.T) {
	startErr := errors.New("sing-box start failed")
	tests := []struct {
		name        string
		marker      bool
		startErr    error
		wantStarts  int
		wantMarker  bool
		wantFailure bool
	}{
		{name: "marker unset", marker: false, wantStarts: 0, wantMarker: false},
		{name: "start succeeds", marker: true, wantStarts: 1, wantMarker: false},
		{name: "start fails", marker: true, startErr: startErr, wantStarts: 1, wantMarker: true, wantFailure: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "monitor.db")
			writeQuotaStopMarker(t, dbPath, tt.marker)
			starts := 0
			err := ReleaseQuotaStop(dbPath, func() error {
				starts++
				return tt.startErr
			})
			if tt.wantFailure {
				if !errors.Is(err, startErr) {
					t.Fatalf("release quota stop error = %v, want %v", err, startErr)
				}
			} else if err != nil {
				t.Fatalf("release quota stop: %v", err)
			}
			if starts != tt.wantStarts {
				t.Fatalf("sing-box starts = %d, want %d", starts, tt.wantStarts)
			}
			if got := readQuotaStopMarker(t, dbPath); got != tt.wantMarker {
				t.Fatalf("quota stop marker = %v, want %v", got, tt.wantMarker)
			}
		})
	}
}

type quotaReleaseRunner struct {
	commands []string
	startErr error
}

func (r *quotaReleaseRunner) Run(cmd system.Command) error {
	r.commands = append(r.commands, cmd.String())
	if cmd.String() == system.Systemctl("start", system.SingBoxService).String() {
		return r.startErr
	}
	return nil
}

func runQuotaReleaseCommands(runner system.Runner, commands ...system.Command) error {
	for _, cmd := range commands {
		if err := runner.Run(cmd); err != nil {
			return err
		}
	}
	return nil
}

func TestDisableManagedHubMonitorReleasesQuotaOwnership(t *testing.T) {
	startErr := errors.New("sing-box start failed")
	tests := []struct {
		name         string
		marker       bool
		startErr     error
		wantCommands []string
		wantMarker   bool
	}{
		{
			name:   "marker unset",
			marker: false,
			wantCommands: []string{
				"systemctl disable --now " + system.MonitorService,
				"systemctl daemon-reload",
			},
		},
		{
			name:   "owned stop restored",
			marker: true,
			wantCommands: []string{
				"systemctl disable --now " + system.MonitorService,
				"systemctl start " + system.SingBoxService,
				"systemctl daemon-reload",
			},
		},
		{
			name:     "failed restore retains ownership",
			marker:   true,
			startErr: startErr,
			wantCommands: []string{
				"systemctl disable --now " + system.MonitorService,
				"systemctl start " + system.SingBoxService,
				"systemctl enable --now " + system.MonitorService,
			},
			wantMarker: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			writeQuotaStopMarker(t, layout.MonitorDB, tt.marker)
			runner := &quotaReleaseRunner{startErr: tt.startErr}
			err := applyManageMonitorService(UpdateOptions{
				Layout:      layout,
				Runner:      runner,
				RunCommands: runQuotaReleaseCommands,
			}, ManageConfig{DeployMonitor: false})
			if tt.startErr != nil {
				if !errors.Is(err, tt.startErr) {
					t.Fatalf("disable monitor error = %v, want %v", err, tt.startErr)
				}
			} else if err != nil {
				t.Fatalf("disable monitor: %v", err)
			}
			if !reflect.DeepEqual(runner.commands, tt.wantCommands) {
				t.Fatalf("commands = %v, want %v", runner.commands, tt.wantCommands)
			}
			if got := readQuotaStopMarker(t, layout.MonitorDB); got != tt.wantMarker {
				t.Fatalf("quota stop marker = %v, want %v", got, tt.wantMarker)
			}
		})
	}
}
