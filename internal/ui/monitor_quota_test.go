package ui

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

type monitorQuotaRunner struct {
	commands []string
	startErr error
}

func (r *monitorQuotaRunner) Run(cmd system.Command) error {
	r.commands = append(r.commands, cmd.String())
	if cmd.String() == system.Systemctl("start", system.SingBoxService).String() {
		return r.startErr
	}
	return nil
}

func setUIQuotaMarker(t *testing.T, dbPath string, stopped bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		t.Fatalf("create monitor store directory: %v", err)
	}
	store, err := monitor.OpenStore(dbPath)
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

func uiQuotaMarker(t *testing.T, dbPath string) bool {
	t.Helper()
	store, err := monitor.OpenStore(dbPath)
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

func TestHubMonitorStopActionReleasesQuotaOwnership(t *testing.T) {
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
				"systemctl stop " + system.MonitorService,
			},
		},
		{
			name:   "owned stop restored",
			marker: true,
			wantCommands: []string{
				"systemctl stop " + system.MonitorService,
				"systemctl start " + system.SingBoxService,
			},
		},
		{
			name:     "failed restore retains ownership",
			marker:   true,
			startErr: startErr,
			wantCommands: []string{
				"systemctl stop " + system.MonitorService,
				"systemctl start " + system.SingBoxService,
				"systemctl start " + system.MonitorService,
			},
			wantMarker: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			setUIQuotaMarker(t, layout.MonitorDB, tt.marker)
			runner := &monitorQuotaRunner{startErr: tt.startErr}
			err := runMonitorServiceAction(layout, runner, "stop")
			if tt.startErr != nil {
				if !errors.Is(err, tt.startErr) {
					t.Fatalf("stop monitor error = %v, want %v", err, tt.startErr)
				}
			} else if err != nil {
				t.Fatalf("stop monitor: %v", err)
			}
			if !reflect.DeepEqual(runner.commands, tt.wantCommands) {
				t.Fatalf("commands = %v, want %v", runner.commands, tt.wantCommands)
			}
			if got := uiQuotaMarker(t, layout.MonitorDB); got != tt.wantMarker {
				t.Fatalf("quota stop marker = %v, want %v", got, tt.wantMarker)
			}
		})
	}
}
