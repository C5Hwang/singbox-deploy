package main

import (
	"bytes"
	"context"
	"debug/elf"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/agentfirewall"
	"github.com/C5Hwang/singbox-deploy/internal/certmgr"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/core"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/monitor"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
	"github.com/C5Hwang/singbox-deploy/internal/system"
	"github.com/C5Hwang/singbox-deploy/internal/uninstall"
)

const testInstallTransactionID = "0123456789abcdef0123456789abcdef"

type handlerRecordingRunner struct {
	commands     []string
	failContains string
}

func (r *handlerRecordingRunner) Run(cmd system.Command) error {
	rendered := cmd.String()
	r.commands = append(r.commands, rendered)
	if r.failContains != "" && strings.Contains(rendered, r.failContains) {
		return errors.New("injected command failure")
	}
	return nil
}

func TestAgentMutationsSerializeAndWaitHonorsContext(t *testing.T) {
	h := &agentHandler{}
	if err := h.beginMutation(context.Background()); err != nil {
		t.Fatalf("acquire first mutation: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- h.beginMutation(ctx)
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting mutation error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiting mutation ignored context cancellation")
	}

	h.endMutation()
	if err := h.beginMutation(context.Background()); err != nil {
		t.Fatalf("gate was not released after cancelled waiter: %v", err)
	}
	h.endMutation()
}

func TestAgentMutationsRejectCommittedRestartAndShutdown(t *testing.T) {
	t.Run("restart", func(t *testing.T) {
		h := &agentHandler{restartPending: true}
		err := h.beginMutation(context.Background())
		if err == nil || !strings.Contains(err.Error(), "restart is pending") {
			t.Fatalf("beginMutation error = %v", err)
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		h := &agentHandler{shutdownPending: true}
		err := h.beginMutation(context.Background())
		if err == nil || !strings.Contains(err.Error(), "shutdown is pending") {
			t.Fatalf("beginMutation error = %v", err)
		}
	})
}

func TestAgentProgressLoggerEmitsOnlyTerminalEvents(t *testing.T) {
	var log bytes.Buffer
	progress := agentProgressLogger(&log)
	progress(deploy.Event{
		Index: 1, Total: 2, Label: "Packages", Detail: "install dependencies", Status: "running",
	})
	progress(deploy.Event{
		Index: 1, Total: 2, Label: "Packages", Detail: "install dependencies", Status: "ok",
	})
	progress(deploy.Event{
		Index: 2, Total: 2, Label: "Services", Detail: "restart services", Status: "running",
	})
	progress(deploy.Event{
		Index:  2,
		Total:  2,
		Label:  "Services",
		Detail: "restart services",
		Status: "fail",
		Err:    errors.New("injected activation failure"),
	})

	const want = "" +
		"[1/2] Packages: complete - install dependencies\n" +
		"[2/2] Services: failed - restart services: injected activation failure\n"
	if got := log.String(); got != want {
		t.Fatalf("progress log = %q, want %q", got, want)
	}
}

func TestAgentFullInstallRejectsExistingStandaloneBeforeMutation(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := state.NewStore(layout.StateDir).WriteString("domain", "standalone.example.com\n", 0o600); err != nil {
		t.Fatal(err)
	}
	runnerCreated := false
	h := &agentHandler{
		layout: layout,
		newRunner: func(context.Context, io.Writer) system.Runner {
			runnerCreated = true
			return &handlerRecordingRunner{}
		},
	}
	err := h.Install(context.Background(), nodeapi.InstallRequest{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "automatic standalone-to-spoke conversion is disabled") {
		t.Fatalf("Install error = %v", err)
	}
	if runnerCreated {
		t.Fatal("Install created a command runner before rejecting existing deployment")
	}
	domain, readErr := state.NewStore(layout.StateDir).ReadValue("domain", true)
	if readErr != nil || domain != "standalone.example.com" {
		t.Fatalf("existing domain changed: domain=%q err=%v", domain, readErr)
	}
}

func TestAgentFullInstallRequiresRollbackOwnershipBeforeMutation(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	runnerCreated := false
	h := &agentHandler{
		layout: layout,
		newRunner: func(context.Context, io.Writer) system.Runner {
			runnerCreated = true
			return &handlerRecordingRunner{}
		},
	}
	err := h.Install(context.Background(), nodeapi.InstallRequest{}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "invalid install transaction ID") {
		t.Fatalf("Install error = %v", err)
	}
	if runnerCreated {
		t.Fatal("Install created a command runner without rollback ownership")
	}
	if _, err := os.Stat(agentConfigDir(layout)); !os.IsNotExist(err) {
		t.Fatalf("Install wrote Agent state before validating ownership: %v", err)
	}
}

func TestAgentHealthReportsStateReadFailure(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := os.MkdirAll(filepath.Join(layout.StateDir, "domain"), 0o700); err != nil {
		t.Fatal(err)
	}
	health := (&agentHandler{layout: layout}).Health()
	if health.OK || !strings.Contains(health.Error, "read deployment state") {
		t.Fatalf("Health = %+v, want explicit state read failure", health)
	}
}

func TestAgentHealthReportsNormalizedExactCoreTag(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := state.NewStore(layout.StateDir).WriteString("domain", "spoke.example.com\n", 0o600); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{
		layout: layout,
		readCoreVersion: func(context.Context) (string, error) {
			return "1.12.4", nil
		},
		coreActive: func(context.Context) bool { return true },
	}
	health := h.Health()
	if !health.OK || !health.Installed || !health.SingBoxActive ||
		health.SingBoxVersion != "v1.12.4" || health.Error != "" {
		t.Fatalf("Health = %+v", health)
	}
}

func TestAgentHealthReportsCoreInspectionFailure(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := state.NewStore(layout.StateDir).WriteString("domain", "spoke.example.com\n", 0o600); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{
		layout: layout,
		readCoreVersion: func(context.Context) (string, error) {
			return "", errors.New("malformed sing-box version output")
		},
		coreActive: func(context.Context) bool { return true },
	}
	health := h.Health()
	if health.OK || !health.Installed || health.SingBoxVersion != "" ||
		!strings.Contains(health.Error, "inspect sing-box core version") {
		t.Fatalf("Health = %+v", health)
	}
}

func TestAgentHealthReportsQuotaStop(t *testing.T) {
	newHandler := func(t *testing.T, active bool, quotaStopped func() (bool, error)) *agentHandler {
		t.Helper()
		layout := paths.LayoutForRoot(t.TempDir())
		if err := state.NewStore(layout.StateDir).WriteString("domain", "spoke.example.com\n", 0o600); err != nil {
			t.Fatal(err)
		}
		return &agentHandler{
			layout: layout,
			readCoreVersion: func(context.Context) (string, error) {
				return "v1.12.4", nil
			},
			coreActive:   func(context.Context) bool { return active },
			quotaStopped: quotaStopped,
		}
	}
	t.Run("inactive with quota marker", func(t *testing.T) {
		h := newHandler(t, false, func() (bool, error) { return true, nil })
		health := h.Health()
		if !health.OK || health.SingBoxActive || !health.QuotaStopped {
			t.Fatalf("Health = %+v", health)
		}
	})
	t.Run("active never reads the marker", func(t *testing.T) {
		h := newHandler(t, true, func() (bool, error) {
			t.Fatal("quota state must not be read while sing-box is active")
			return true, nil
		})
		health := h.Health()
		if !health.OK || !health.SingBoxActive || health.QuotaStopped {
			t.Fatalf("Health = %+v", health)
		}
	})
	t.Run("marker read failure fails closed", func(t *testing.T) {
		h := newHandler(t, false, func() (bool, error) {
			return true, errors.New("monitor store is locked")
		})
		health := h.Health()
		if !health.OK || health.SingBoxActive || health.QuotaStopped {
			t.Fatalf("Health = %+v", health)
		}
	})
}

func TestAgentCoreChangeUsesManagerAndVerifiesResult(t *testing.T) {
	var (
		gotAction core.Action
		gotTag    string
	)
	h := &agentHandler{
		runCoreManager: func(_ context.Context, action core.Action, tag string, _ io.Writer) (core.Result, error) {
			gotAction, gotTag = action, tag
			return core.Result{Tag: tag}, nil
		},
		readCoreVersion: func(context.Context) (string, error) {
			return "v1.12.4", nil
		},
		coreActive: func(context.Context) bool { return true },
	}
	var log bytes.Buffer
	if err := h.ChangeCore(context.Background(), nodeapi.CoreRequest{
		SingBoxVersion: "v1.12.4",
	}, &log); err != nil {
		t.Fatalf("ChangeCore: %v", err)
	}
	if gotAction != core.ActionChangeStable || gotTag != "v1.12.4" {
		t.Fatalf("manager call = %q %q", gotAction, gotTag)
	}
	if !strings.Contains(log.String(), "verified sing-box core v1.12.4") {
		t.Fatalf("verification log = %q", log.String())
	}
}

func TestAgentCoreChangeRejectsInvalidAndUnverifiedTargets(t *testing.T) {
	t.Run("invalid tag", func(t *testing.T) {
		called := false
		h := &agentHandler{
			runCoreManager: func(context.Context, core.Action, string, io.Writer) (core.Result, error) {
				called = true
				return core.Result{}, nil
			},
		}
		err := h.ChangeCore(context.Background(), nodeapi.CoreRequest{}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "required") || called {
			t.Fatalf("ChangeCore error=%v managerCalled=%v", err, called)
		}
	})
	t.Run("reported mismatch", func(t *testing.T) {
		h := &agentHandler{
			runCoreManager: func(_ context.Context, _ core.Action, tag string, _ io.Writer) (core.Result, error) {
				return core.Result{Tag: tag}, nil
			},
			readCoreVersion: func(context.Context) (string, error) {
				return "v1.12.3", nil
			},
			coreActive: func(context.Context) bool { return true },
		}
		err := h.ChangeCore(context.Background(), nodeapi.CoreRequest{
			SingBoxVersion: "v1.12.4",
		}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), `reports "v1.12.3"`) {
			t.Fatalf("mismatch error = %v", err)
		}
	})
	t.Run("service inactive", func(t *testing.T) {
		h := &agentHandler{
			runCoreManager: func(_ context.Context, _ core.Action, tag string, _ io.Writer) (core.Result, error) {
				return core.Result{Tag: tag}, nil
			},
			readCoreVersion: func(context.Context) (string, error) {
				return "v1.12.4", nil
			},
			coreActive: func(context.Context) bool { return false },
		}
		err := h.ChangeCore(context.Background(), nodeapi.CoreRequest{
			SingBoxVersion: "v1.12.4",
		}, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "not active") {
			t.Fatalf("inactive-service error = %v", err)
		}
	})
}

func TestAgentCoreChangeWaitsOnMutationGate(t *testing.T) {
	called := false
	h := &agentHandler{
		runCoreManager: func(context.Context, core.Action, string, io.Writer) (core.Result, error) {
			called = true
			return core.Result{}, nil
		},
	}
	if err := h.beginMutation(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.ChangeCore(ctx, nodeapi.CoreRequest{SingBoxVersion: "v1.12.4"}, io.Discard)
	h.endMutation()
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("ChangeCore error=%v managerCalled=%v", err, called)
	}
}

func TestAgentFullInstallRequiresExactCorePinBeforeRunner(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	runnerCreated := false
	h := &agentHandler{
		layout: layout,
		newRunner: func(context.Context, io.Writer) system.Runner {
			runnerCreated = true
			return &handlerRecordingRunner{}
		},
	}
	err := h.Install(context.Background(), nodeapi.InstallRequest{
		InstallTransactionID: testInstallTransactionID,
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "sing-box version is required for full install") {
		t.Fatalf("Install error = %v", err)
	}
	if runnerCreated {
		t.Fatal("full install created a runner before validating the core pin")
	}
}

func TestSpokeOrchestratorPinsRequestedCoreVersion(t *testing.T) {
	h := &agentHandler{layout: paths.LayoutForRoot(t.TempDir())}
	orch := h.newSpokeOrchestrator(
		context.Background(),
		nodeapi.InstallRequest{SingBoxVersion: "v1.12.4"},
		system.Host{Arch: "arm64"},
		&handlerRecordingRunner{},
		io.Discard,
	)
	tag, err := orch.LatestSingBox(context.Background())
	if err != nil || tag != "v1.12.4" {
		t.Fatalf("pinned resolver = %q, %v", tag, err)
	}
	if orch.GOARCH != "arm64" {
		t.Fatalf("orchestrator GOARCH = %q, want arm64", orch.GOARCH)
	}
}

func TestConfigOnlyInstallSelectsReconfigureWithoutFullInstall(t *testing.T) {
	var full, reconfigure bool
	h := &agentHandler{layout: paths.LayoutForRoot(t.TempDir())}
	orch := h.newSpokeOrchestrator(
		context.Background(),
		nodeapi.InstallRequest{ConfigOnly: true},
		system.Host{Arch: "amd64"},
		&handlerRecordingRunner{},
		io.Discard,
	)
	err := runSpokeDeployment(
		true,
		func() error {
			full = true
			_, err := orch.LatestSingBox(context.Background())
			return err
		},
		func() error {
			reconfigure = true
			return nil
		},
	)
	if err != nil || full || !reconfigure {
		t.Fatalf("runSpokeDeployment error=%v full=%v reconfigure=%v", err, full, reconfigure)
	}
}

func TestProtocolPatchChangesOnlyTargetProtocolAndStateRoundTrips(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	original, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	cfg := deploy.Config{
		Domain: "spoke.example.com", SpokeMode: true,
		DisplayName:            "Tokyo untouched",
		Salt:                   "protocol-patch-salt",
		SiteTemplate:           deploy.DefaultSiteTemplate,
		Enabled:                []config.Protocol{config.ProtocolHysteria2, config.ProtocolTUIC},
		RealityServerName:      "www.example.com",
		RealityHandshakePort:   443,
		DeployMonitor:          true,
		MonitorAlias:           "monitor untouched",
		MonitorInterface:       "eth9",
		MonitorPort:            19090,
		MonitorIntervalSeconds: 75,
		TrafficTotalLimitBytes: 123456,
		ResetDay:               7,
		ResetHour:              8,
		Ports:                  config.Ports{RealityVision: 8443, RealityGRPC: 8444, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		Creds:                  original,
	}
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	replacement, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	override := protocolCredentialsFromDeploy(replacement)
	h := &agentHandler{layout: layout}
	next, err := h.buildProtocolPatchConfig(nodeapi.ProtocolPatch{
		Protocol: "tuic", Port: 20443, Credentials: override,
	})
	if err != nil {
		t.Fatalf("buildProtocolPatchConfig: %v", err)
	}
	if next.Creds.TUICPassword != replacement.TUICPassword ||
		next.Creds.TUICUUID != replacement.TUICUUID ||
		next.Creds.TUICPassword == original.TUICPassword {
		t.Fatalf("target credential patch not applied: %+v", next.Creds)
	}
	if next.Creds.HysteriaPassword != original.HysteriaPassword ||
		next.Creds.RealityVisionUUID != original.RealityVisionUUID ||
		next.Creds.RealityGRPCUUID != original.RealityGRPCUUID ||
		next.Creds.AnyTLSPassword != original.AnyTLSPassword ||
		next.Creds.RealityPrivateKey != original.RealityPrivateKey ||
		next.Creds.RealityPublicKey != original.RealityPublicKey ||
		next.Creds.RealityShortID != original.RealityShortID {
		t.Fatalf("patch overwrote another protocol credential: %+v", next.Creds)
	}
	if next.Domain != cfg.Domain || next.DisplayName != cfg.DisplayName ||
		next.Salt != cfg.Salt || next.SiteTemplate != cfg.SiteTemplate ||
		next.RealityServerName != cfg.RealityServerName ||
		next.RealityHandshakePort != cfg.RealityHandshakePort ||
		next.MonitorAlias != cfg.MonitorAlias ||
		next.MonitorInterface != cfg.MonitorInterface ||
		next.MonitorIntervalSeconds != cfg.MonitorIntervalSeconds ||
		next.TrafficTotalLimitBytes != cfg.TrafficTotalLimitBytes ||
		next.ResetDay != cfg.ResetDay || next.ResetHour != cfg.ResetHour {
		t.Fatalf("patch overwrote non-protocol state: before=%+v after=%+v", cfg, next)
	}
	if next.Ports.TUIC != 20443 || next.Ports.Hysteria2 != cfg.Ports.Hysteria2 ||
		next.Ports.RealityVision != cfg.Ports.RealityVision ||
		next.Ports.RealityGRPC != cfg.Ports.RealityGRPC ||
		next.Ports.AnyTLS != cfg.Ports.AnyTLS {
		t.Fatalf("patch overwrote another protocol port: %+v", next.Ports)
	}

	if err := deploy.WriteInstallState(layout.StateDir, next); err != nil {
		t.Fatal(err)
	}
	stateResponse, err := h.ProtocolState(context.Background())
	if err != nil {
		t.Fatalf("ProtocolState: %v", err)
	}
	if stateResponse.Credentials.TUICPassword != replacement.TUICPassword ||
		stateResponse.Ports.TUIC != 20443 || stateResponse.Domain != "spoke.example.com" ||
		stateResponse.Revision == "" {
		t.Fatalf("protocol state = %+v", stateResponse)
	}
	if err := nodeapi.ValidateProtocolStateResponse(stateResponse); err != nil {
		t.Fatalf("invalid protocol state response: %v", err)
	}
}

func TestAgentRejectsStaleProtocolRevisionBeforeMutation(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	cfg := deploy.Config{
		Domain: "spoke.example.com", SpokeMode: true,
		Salt:                 "protocol-revision-test-salt",
		Enabled:              []config.Protocol{config.ProtocolHysteria2},
		RealityServerName:    "www.example.com",
		RealityHandshakePort: 443,
		Ports:                config.Ports{RealityVision: 8443, RealityGRPC: 8444, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		Creds:                creds,
	}
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{layout: layout}
	before, err := h.ProtocolState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	override := protocolCredentialsFromDeploy(creds)
	override.HysteriaPassword += "-new"
	err = h.Install(context.Background(), nodeapi.InstallRequest{
		ConfigOnly:               true,
		ExpectedProtocolRevision: strings.Repeat("0", 64),
		ProtocolPatch: &nodeapi.ProtocolPatch{
			Protocol: "hysteria2", Port: 19443, Credentials: override,
		},
	}, io.Discard)
	if !nodeapi.IsProtocolRevisionConflict(err) {
		t.Fatalf("stale revision error = %v", err)
	}
	after, stateErr := h.ProtocolState(context.Background())
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if after.Revision != before.Revision ||
		after.Credentials.HysteriaPassword != before.Credentials.HysteriaPassword {
		t.Fatalf("stale CAS mutated protocol state: before=%+v after=%+v", before, after)
	}
}

func TestProtocolPatchOwnsOnlySelectedProtocolFields(t *testing.T) {
	tests := []struct {
		protocol config.Protocol
		port     int
		apply    func(*deploy.Credentials, *config.Ports, deploy.Credentials, int)
	}{
		{config.ProtocolRealityVision, 18443, func(c *deploy.Credentials, p *config.Ports, n deploy.Credentials, port int) {
			c.RealityVisionUUID, p.RealityVision = n.RealityVisionUUID, port
		}},
		{config.ProtocolRealityGRPC, 18444, func(c *deploy.Credentials, p *config.Ports, n deploy.Credentials, port int) {
			c.RealityGRPCUUID, p.RealityGRPC = n.RealityGRPCUUID, port
		}},
		{config.ProtocolHysteria2, 19443, func(c *deploy.Credentials, p *config.Ports, n deploy.Credentials, port int) {
			c.HysteriaPassword, p.Hysteria2 = n.HysteriaPassword, port
		}},
		{config.ProtocolTUIC, 20443, func(c *deploy.Credentials, p *config.Ports, n deploy.Credentials, port int) {
			c.TUICUUID, c.TUICPassword, p.TUIC = n.TUICUUID, n.TUICPassword, port
		}},
		{config.ProtocolAnyTLS, 21443, func(c *deploy.Credentials, p *config.Ports, n deploy.Credentials, port int) {
			c.AnyTLSPassword, p.AnyTLS = n.AnyTLSPassword, port
		}},
	}
	for _, tt := range tests {
		t.Run(string(tt.protocol), func(t *testing.T) {
			layout := paths.LayoutForRoot(t.TempDir())
			original, err := deploy.GenerateCredentials()
			if err != nil {
				t.Fatal(err)
			}
			replacement, err := deploy.GenerateCredentials()
			if err != nil {
				t.Fatal(err)
			}
			ports := config.Ports{RealityVision: 8443, RealityGRPC: 8444, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443}
			cfg := deploy.Config{
				Domain: "spoke.example.com", Salt: "patch-table-salt",
				Enabled: config.AllProtocols, Ports: ports, Creds: original,
			}
			if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
				t.Fatal(err)
			}
			got, err := (&agentHandler{layout: layout}).buildProtocolPatchConfig(nodeapi.ProtocolPatch{
				Protocol: string(tt.protocol), Port: tt.port,
				Credentials: protocolCredentialsFromDeploy(replacement),
			})
			if err != nil {
				t.Fatal(err)
			}
			wantCreds, wantPorts := original, ports
			tt.apply(&wantCreds, &wantPorts, replacement, tt.port)
			if got.Creds != wantCreds || got.Ports != wantPorts {
				t.Fatalf("patch result credentials/ports = %+v / %+v, want %+v / %+v", got.Creds, got.Ports, wantCreds, wantPorts)
			}
		})
	}
}

func TestAgentInstallProtocolPatchTouchesOnlyTargetRuntimeState(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	const domain = "legacy.example.com"
	cfg := deploy.Config{
		Domain: domain, DisplayName: "legacy display", Salt: "legacy-patch-salt",
		SiteTemplate:      deploy.DefaultSiteTemplate,
		Enabled:           []config.Protocol{config.ProtocolHysteria2},
		RealityServerName: "legacy-reality.example.com", RealityHandshakePort: 443,
		DeployMonitor: true, MonitorAlias: "legacy monitor", MonitorInterface: "eth9",
		MonitorPort: 19090, MonitorIntervalSeconds: 60,
		TrafficTotalLimitBytes: 987654, ResetDay: 6, ResetHour: 7,
		Ports: config.Ports{Hysteria2: 9443},
		// Legacy nodes may not have generated credentials for disabled
		// protocols. Only the enabled Hysteria2 credential is present.
		Creds: deploy.Credentials{HysteriaPassword: "legacy-password"},
	}
	if err := deploy.WriteInstallState(layout.StateDir, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(layout.ConfigJSON), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(layout.ConfigJSON, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	certPath, keyPath := certmgr.CertPaths(layout, domain)
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		t.Fatal(err)
	}
	originalCert := []byte("legacy certificate bytes")
	originalKey := []byte("legacy private key bytes")
	if err := os.WriteFile(certPath, originalCert, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, originalKey, 0o600); err != nil {
		t.Fatal(err)
	}

	runner := &handlerRecordingRunner{}
	monitorReloads := 0
	systemdDir := filepath.Join(layout.Root, "systemd")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	supervisor := newMonitorSupervisor(context.Background(), layout)
	supervisor.now = func() time.Time { return time.Unix(1234, 0).UTC() }
	supervisor.newMonitor = func(*monitor.Store, monitor.Config) (http.Handler, func(context.Context) error) {
		monitorReloads++
		return http.NotFoundHandler(), func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}
	}
	h := &agentHandler{
		layout: layout, monitor: supervisor, systemdDir: systemdDir,
		nginxConfPath: filepath.Join(layout.Root, "nginx", "singbox-deploy.conf"),
		newRunner:     func(context.Context, io.Writer) system.Runner { return runner },
	}
	before, err := h.ProtocolState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	err = h.Install(context.Background(), nodeapi.InstallRequest{
		ConfigOnly: true, ExpectedProtocolRevision: before.Revision,
		// These stale/untrusted general and other-protocol fields must be
		// ignored by a protocol patch.
		Domain: "overwrite.example.com", DisplayName: "overwrite",
		RealityServerName: "overwrite.example.com", EnabledProtocols: []string{"anytls"},
		Ports:          nodeapi.PortSet{AnyTLS: 21443},
		Monitor:        false,
		CertificatePEM: "overwrite certificate",
		PrivateKeyPEM:  "overwrite key",
		ProtocolPatch: &nodeapi.ProtocolPatch{
			Protocol: "hysteria2", Port: 19443,
			Credentials: nodeapi.ProtocolCredentials{HysteriaPassword: "rotated-password"},
		},
	}, io.Discard)
	if err != nil {
		t.Fatalf("Install protocol patch: %v", err)
	}

	after, err := deploy.LoadProtocolConfig(layout)
	if err != nil {
		t.Fatal(err)
	}
	if after.Creds.HysteriaPassword != "rotated-password" || after.Ports.Hysteria2 != 19443 {
		t.Fatalf("target patch not applied: %+v", after)
	}
	if after.Domain != cfg.Domain || after.DisplayName != cfg.DisplayName ||
		after.RealityServerName != cfg.RealityServerName ||
		after.MonitorAlias != cfg.MonitorAlias || !after.DeployMonitor ||
		after.TrafficTotalLimitBytes != cfg.TrafficTotalLimitBytes ||
		strings.Join(protocolNamesForState(after.Enabled), ",") != "hysteria2" {
		t.Fatalf("protocol patch changed unrelated state: before=%+v after=%+v", cfg, after)
	}
	gotCert, _ := os.ReadFile(certPath)
	gotKey, _ := os.ReadFile(keyPath)
	certInfo, certErr := os.Stat(certPath)
	keyInfo, keyErr := os.Stat(keyPath)
	if !bytes.Equal(gotCert, originalCert) || !bytes.Equal(gotKey, originalKey) ||
		certErr != nil || keyErr != nil ||
		certInfo.Mode().Perm() != 0o644 || keyInfo.Mode().Perm() != 0o600 {
		t.Fatalf("protocol patch changed certificate pair or modes: cert=%q/%v key=%q/%v", gotCert, certInfo.Mode(), gotKey, keyInfo.Mode())
	}
	if monitorReloads != 0 {
		t.Fatalf("protocol patch reloaded monitor %d time(s)", monitorReloads)
	}
	for _, command := range runner.commands {
		if strings.Contains(command, "disable --now") ||
			strings.Contains(command, "daemon-reload") ||
			strings.Contains(command, system.MonitorService) {
			t.Fatalf("protocol patch ran unrelated service command %q", command)
		}
	}
}

func TestOrdinaryConfigOnlyPreservesNewerProtocolState(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	original, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	current := deploy.Config{
		Domain: "spoke.example.com", DisplayName: "old display",
		Salt: "ordinary-reconfigure-salt", SiteTemplate: deploy.DefaultSiteTemplate,
		Enabled:           []config.Protocol{config.ProtocolHysteria2, config.ProtocolTUIC},
		RealityServerName: "current.example.com", RealityHandshakePort: 443,
		MonitorAlias: "old monitor", MonitorPort: 19090, MonitorIntervalSeconds: 60,
		Ports: config.Ports{RealityVision: 8443, RealityGRPC: 8444, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		Creds: original,
	}
	if err := deploy.WriteInstallState(layout.StateDir, current); err != nil {
		t.Fatal(err)
	}
	replacement, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{layout: layout}
	patched, err := h.buildProtocolPatchConfig(nodeapi.ProtocolPatch{
		Protocol: "tuic", Port: 20443, Credentials: protocolCredentialsFromDeploy(replacement),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := deploy.WriteInstallState(layout.StateDir, patched); err != nil {
		t.Fatal(err)
	}

	stale := nodeapi.InstallRequest{
		ConfigOnly: true,
		Domain:     "spoke.example.com", DisplayName: "new display", SiteTemplate: "dimension",
		RealityServerName: "stale.example.com", RealityHandshakePort: 8443,
		EnabledProtocols: []string{"hysteria2"},
		Ports:            nodeapi.PortSet{Hysteria2: 19443, TUIC: 10443},
		Monitor:          true, MonitorAlias: "new monitor", MonitorPort: 19090,
		MonitorIntervalSeconds: 90,
	}
	got, err := h.buildSpokeConfig(stale)
	if err != nil {
		t.Fatal(err)
	}
	if got.Creds.TUICUUID != replacement.TUICUUID ||
		got.Creds.TUICPassword != replacement.TUICPassword ||
		got.Ports.TUIC != 20443 ||
		got.RealityServerName != patched.RealityServerName ||
		got.RealityHandshakePort != patched.RealityHandshakePort ||
		strings.Join(protocolNamesForState(got.Enabled), ",") != strings.Join(protocolNamesForState(patched.Enabled), ",") {
		t.Fatalf("ordinary stale reconfigure rolled back protocol state: patched=%+v got=%+v", patched, got)
	}
	if got.DisplayName != "new display" || got.SiteTemplate != "dimension" ||
		got.MonitorAlias != "new monitor" || got.MonitorIntervalSeconds != 90 {
		t.Fatalf("ordinary reconfigure did not apply general settings: %+v", got)
	}
}

func TestExplicitProtocolReplacementChangesFullStateAndRequiresCAS(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	current := deploy.Config{
		Domain: "spoke.example.com", DisplayName: "concurrent display",
		Salt: "replacement-cas-salt", SiteTemplate: "dimension",
		Enabled:           []config.Protocol{config.ProtocolHysteria2},
		RealityServerName: "old.example.com", RealityHandshakePort: 443,
		DeployMonitor: true, MonitorAlias: "concurrent monitor",
		MonitorPort: 19090, MonitorIntervalSeconds: 75,
		TrafficTotalLimitBytes: 456789, ResetDay: 5, ResetHour: 6,
		Ports: config.Ports{RealityVision: 8443, RealityGRPC: 8444, Hysteria2: 9443, TUIC: 10443, AnyTLS: 11443},
		Creds: creds,
	}
	if err := deploy.WriteInstallState(layout.StateDir, current); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{layout: layout}
	stateBefore, err := h.ProtocolState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	req := nodeapi.InstallRequest{
		ConfigOnly: true, ReplaceProtocolState: true,
		ExpectedProtocolRevision: stateBefore.Revision,
		Domain:                   "stale.example.com", DisplayName: "stale display",
		Monitor: false, MonitorAlias: "stale monitor",
		EnabledProtocols:  []string{"vless-reality-vision", "anytls"},
		RealityServerName: "new.example.com", RealityHandshakePort: 8443,
		Ports: nodeapi.PortSet{RealityVision: 18443, RealityGRPC: 8444, Hysteria2: 9443, TUIC: 10443, AnyTLS: 21443},
	}
	emptyReplacement := req
	emptyReplacement.EnabledProtocols = nil
	if _, err := h.buildSpokeConfig(emptyReplacement); err == nil ||
		!strings.Contains(err.Error(), "at least one enabled protocol") {
		t.Fatalf("empty replacement fell through to default-all semantics: %v", err)
	}
	if err := nodeapi.ValidateInstallSingBoxVersion(req); err != nil {
		t.Fatalf("valid explicit replacement: %v", err)
	}
	next, err := h.buildSpokeConfig(req)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(protocolNamesForState(next.Enabled), ",") != "vless-reality-vision,anytls" ||
		next.Ports.RealityVision != 18443 || next.Ports.AnyTLS != 21443 ||
		next.RealityServerName != "new.example.com" || next.RealityHandshakePort != 8443 {
		t.Fatalf("explicit replacement did not apply protocol state: %+v", next)
	}
	if next.Domain != current.Domain || next.DisplayName != current.DisplayName ||
		next.SiteTemplate != current.SiteTemplate || next.MonitorAlias != current.MonitorAlias ||
		!next.DeployMonitor || next.MonitorIntervalSeconds != current.MonitorIntervalSeconds ||
		next.TrafficTotalLimitBytes != current.TrafficTotalLimitBytes ||
		next.ResetDay != current.ResetDay || next.ResetHour != current.ResetHour {
		t.Fatalf("explicit protocol replacement overwrote non-protocol state: before=%+v after=%+v", current, next)
	}

	req.ExpectedProtocolRevision = strings.Repeat("0", 64)
	err = h.Install(context.Background(), req, io.Discard)
	if !nodeapi.IsProtocolRevisionConflict(err) {
		t.Fatalf("stale explicit replacement error = %v", err)
	}
	stateAfter, stateErr := h.ProtocolState(context.Background())
	if stateErr != nil {
		t.Fatal(stateErr)
	}
	if stateAfter.Revision != stateBefore.Revision {
		t.Fatalf("stale explicit replacement mutated state: before=%+v after=%+v", stateBefore, stateAfter)
	}
}

func TestRollbackUninstallRequiresMatchingInstallOwner(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	store := state.NewStore(layout.StateDir)
	agentStore := state.NewStore(agentConfigDir(layout))
	if err := store.WriteString("domain", "standalone.example.com\n", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authorizeRollbackUninstall(layout, testInstallTransactionID); err == nil {
		t.Fatal("rollback without an ownership marker was authorized")
	}
	if err := agentStore.WriteString(installTransactionFile, strings.Repeat("a", 32)+"\n", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authorizeRollbackUninstall(layout, testInstallTransactionID); err == nil || !strings.Contains(err.Error(), "does not own") {
		t.Fatalf("mismatched rollback error = %v", err)
	}
	if err := agentStore.WriteString(installTransactionFile, testInstallTransactionID+"\n", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := authorizeRollbackUninstall(layout, testInstallTransactionID); err != nil {
		t.Fatalf("matching rollback owner rejected: %v", err)
	}
	domain, err := store.ReadValue("domain", true)
	if err != nil || domain != "standalone.example.com" {
		t.Fatalf("authorization checks mutated existing deployment: domain=%q err=%v", domain, err)
	}
}

func TestKeepOverlayUninstallBecomesTerminalAfterOwnedCleanup(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	if err := state.NewStore(agentConfigDir(layout)).WriteString(installTransactionFile, testInstallTransactionID+"\n", 0o600); err != nil {
		t.Fatal(err)
	}
	cleanupCalled := false
	h := &agentHandler{
		layout: layout,
		newRunner: func(context.Context, io.Writer) system.Runner {
			return &handlerRecordingRunner{}
		},
		runUninstall: func(_ context.Context, opts uninstall.Options) error {
			cleanupCalled = true
			if !opts.PreserveAgentState {
				t.Fatal("rollback cleanup did not preserve Agent state")
			}
			return nil
		},
	}
	if err := h.Uninstall(context.Background(), nodeapi.UninstallRequest{
		KeepOverlay:           true,
		RollbackTransactionID: testInstallTransactionID,
	}, io.Discard); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !cleanupCalled || !h.shutdownPending {
		t.Fatalf("cleanupCalled=%v shutdownPending=%v", cleanupCalled, h.shutdownPending)
	}
	if err := h.beginMutation(context.Background()); err == nil || !strings.Contains(err.Error(), "shutdown is pending") {
		t.Fatalf("post-rollback mutation error = %v", err)
	}
}

func TestPrepareAgentTeardownCompletesDurableWorkBeforeFirewall(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	agentDir := filepath.Join(layout.StateDir, "agent")
	rule := agentfirewall.Rule{
		Backend:   system.FirewallUFW,
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}
	if err := agentfirewall.Save(agentDir, rule); err != nil {
		t.Fatal(err)
	}
	teardownPaths := []string{
		filepath.Join(t.TempDir(), "singbox-deploy-agent.service"),
		filepath.Join(t.TempDir(), "sbwg0.conf"),
	}
	for _, path := range teardownPaths {
		if err := os.WriteFile(path, []byte("managed"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runner := &handlerRecordingRunner{}
	if err := prepareAgentAndOverlayTeardown(layout, runner, runner, rule, true, teardownPaths); err != nil {
		t.Fatalf("prepareAgentAndOverlayTeardown: %v", err)
	}
	for _, path := range teardownPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("teardown path %s still exists: %v", path, err)
		}
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("Agent state still exists: %v", err)
	}
	if len(runner.commands) != 4 {
		t.Fatalf("commands = %#v", runner.commands)
	}
	if !strings.HasPrefix(runner.commands[len(runner.commands)-1], "ufw ") {
		t.Fatalf("firewall was not the final command: %#v", runner.commands)
	}
}

func TestPrepareAgentTeardownRestoresFirewallStateOnFailure(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	agentDir := filepath.Join(layout.StateDir, "agent")
	rule := agentfirewall.Rule{
		Backend:   system.FirewallUFW,
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}
	if err := agentfirewall.Save(agentDir, rule); err != nil {
		t.Fatal(err)
	}
	if err := state.NewStore(agentDir).WriteString("token", "still-authenticated\n", 0o600); err != nil {
		t.Fatal(err)
	}
	teardownPaths := []string{
		filepath.Join(t.TempDir(), "singbox-deploy-agent.service"),
		filepath.Join(t.TempDir(), "sbwg0.conf"),
	}
	wantData := [][]byte{[]byte("agent-unit"), []byte("wireguard-config")}
	wantModes := []os.FileMode{0o644, 0o600}
	for i, path := range teardownPaths {
		if err := os.WriteFile(path, wantData[i], wantModes[i]); err != nil {
			t.Fatal(err)
		}
	}
	runner := &handlerRecordingRunner{failContains: "ufw "}
	recovery := &handlerRecordingRunner{}
	err := prepareAgentAndOverlayTeardown(layout, runner, recovery, rule, true, teardownPaths)
	if err == nil || !strings.Contains(err.Error(), "remove Agent firewall rule") {
		t.Fatalf("teardown error = %v", err)
	}
	restored, ok, loadErr := agentfirewall.Load(agentDir)
	if loadErr != nil || !ok {
		t.Fatalf("firewall cleanup state not restored: ok=%v err=%v", ok, loadErr)
	}
	if restored != rule {
		t.Fatalf("restored rule = %#v, want %#v", restored, rule)
	}
	token, tokenErr := state.NewStore(agentDir).ReadValue("token", true)
	if tokenErr != nil || token != "still-authenticated" {
		t.Fatalf("Agent token not restored: token=%q err=%v", token, tokenErr)
	}
	for i, path := range teardownPaths {
		got, readErr := os.ReadFile(path)
		if readErr != nil || !bytes.Equal(got, wantData[i]) {
			t.Fatalf("control-plane file %s not restored: data=%q err=%v", path, got, readErr)
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			t.Fatalf("stat restored control-plane file %s: %v", path, statErr)
		}
		if info.Mode().Perm() != wantModes[i] {
			t.Fatalf("control-plane file %s mode=%v, want %v", path, info.Mode().Perm(), wantModes[i])
		}
	}
	recoveryLog := strings.Join(recovery.commands, "\n")
	for _, want := range []string{
		"systemctl daemon-reload",
		"systemctl enable wg-quick@sbwg0.service",
		"systemctl enable singbox-deploy-agent.service",
		"ufw allow in on sbwg0",
	} {
		if !strings.Contains(recoveryLog, want) {
			t.Fatalf("recovery commands missing %q:\n%s", want, recoveryLog)
		}
	}

	retryRunner := &handlerRecordingRunner{}
	if err := prepareAgentAndOverlayTeardown(layout, retryRunner, &handlerRecordingRunner{}, restored, true, teardownPaths); err != nil {
		t.Fatalf("retry teardown: %v", err)
	}
	for _, unit := range []string{"singbox-deploy-agent.service", "wg-quick@sbwg0.service"} {
		if !strings.Contains(strings.Join(retryRunner.commands, "\n"), "systemctl disable "+unit) {
			t.Fatalf("retry did not disable restored unit %s: %#v", unit, retryRunner.commands)
		}
	}
	if _, err := os.Stat(agentDir); !os.IsNotExist(err) {
		t.Fatalf("Agent state still exists after retry: %v", err)
	}
	for _, path := range teardownPaths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("restored teardown path %s survived successful retry: %v", path, err)
		}
	}
}

func TestPrepareAgentTeardownRestoresFirewalldBeforeRetry(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	agentDir := filepath.Join(layout.StateDir, "agent")
	rule := agentfirewall.Rule{
		Backend:   system.FirewallFirewalld,
		Zone:      "public",
		Interface: "sbwg0",
		HubIP:     "10.90.0.1",
		ListenIP:  "10.90.0.2",
		Port:      19091,
	}
	if err := agentfirewall.Save(agentDir, rule); err != nil {
		t.Fatal(err)
	}
	if err := state.NewStore(agentDir).WriteString("token", "retry-token\n", 0o600); err != nil {
		t.Fatal(err)
	}
	first := &handlerRecordingRunner{failContains: "firewall-cmd --reload"}
	recovery := &handlerRecordingRunner{}
	err := prepareAgentAndOverlayTeardown(layout, first, recovery, rule, true, nil)
	if err == nil {
		t.Fatal("expected injected firewalld reload failure")
	}
	if token, readErr := state.NewStore(agentDir).ReadValue("token", true); readErr != nil || token != "retry-token" {
		t.Fatalf("full Agent state not restored: token=%q err=%v", token, readErr)
	}
	recoveryLog := strings.Join(recovery.commands, "\n")
	if !strings.Contains(recoveryLog, "--add-rich-rule") || !strings.Contains(recoveryLog, "firewall-cmd --reload") {
		t.Fatalf("firewalld rule was not reopened after ambiguous failure:\n%s", recoveryLog)
	}
	if _, statErr := os.Stat(filepath.Join(agentDir, "firewall_cleanup_next")); !os.IsNotExist(statErr) {
		t.Fatalf("firewall cleanup progress was not reset after reopening rule: %v", statErr)
	}

	retry := &handlerRecordingRunner{}
	if err := prepareAgentAndOverlayTeardown(layout, retry, &handlerRecordingRunner{}, rule, true, nil); err != nil {
		t.Fatalf("retry teardown: %v", err)
	}
	joined := strings.Join(retry.commands, "\n")
	if !strings.Contains(joined, "--remove-rich-rule") {
		t.Fatalf("retry did not restart firewall cleanup after reopening the rule:\n%s", joined)
	}
	if !strings.Contains(joined, "firewall-cmd --reload") {
		t.Fatalf("retry did not resume at reload:\n%s", joined)
	}
}

func TestAgentUpgradeAtomicallyReplacesAndSchedulesRestart(t *testing.T) {
	payload := readHostELF(t)
	target := filepath.Join(t.TempDir(), "singbox-deploy-agent")
	old := []byte("old-agent")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	restarted := false
	inspected := false
	h := &agentHandler{
		agentExecutable: func() (string, error) { return target, nil },
		inspectAgent: func(_ context.Context, staged, expected string) error {
			inspected = true
			if expected != "v9.8.7" {
				t.Fatalf("expected version = %q", expected)
			}
			got, err := os.ReadFile(staged)
			if err != nil {
				return err
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("staged payload mismatch")
			}
			return nil
		},
		scheduleRestart: func() error {
			restarted = true
			return nil
		},
	}
	if err := h.Upgrade(context.Background(), nodeapi.NewUpgradeRequest("v9.8.7", payload), io.Discard); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) || !inspected || !restarted {
		t.Fatalf("upgrade did not commit/inspect/restart: equal=%v inspected=%v restarted=%v", bytes.Equal(got, payload), inspected, restarted)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("upgraded mode = %v, err=%v", info.Mode().Perm(), err)
	}
	backup, err := os.ReadFile(agentBackupPath(target))
	if err != nil {
		t.Fatalf("read recoverable Agent backup: %v", err)
	}
	if !bytes.Equal(backup, old) {
		t.Fatalf("Agent backup = %q, want old executable", backup)
	}
}

func TestAgentUpgradeScheduleFailureRestoresOldExecutableAndAllowsRetry(t *testing.T) {
	payload := readHostELF(t)
	target := filepath.Join(t.TempDir(), "singbox-deploy-agent")
	old := []byte("known-good-old-agent")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	scheduleErr := errors.New("cannot queue transient restart")
	scheduleCalls := 0
	h := &agentHandler{
		agentExecutable: func() (string, error) { return target, nil },
		inspectAgent:    func(context.Context, string, string) error { return nil },
		scheduleRestart: func() error {
			scheduleCalls++
			if scheduleCalls == 1 {
				return scheduleErr
			}
			return nil
		},
	}
	req := nodeapi.NewUpgradeRequest("v2.0.0", payload)
	err := h.Upgrade(context.Background(), req, io.Discard)
	if !errors.Is(err, scheduleErr) || !strings.Contains(err.Error(), "upgrade can be retried") {
		t.Fatalf("schedule failure = %v, want retryable restoration error", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(got, old) {
		t.Fatalf("Agent after schedule failure = %q, err=%v; want old executable", got, readErr)
	}
	if _, statErr := os.Stat(agentBackupPath(target)); !os.IsNotExist(statErr) {
		t.Fatalf("restored backup was not cleaned up: %v", statErr)
	}
	if h.restartPending || h.pendingAgentRestore != nil {
		t.Fatalf("restored upgrade remained blocked: restart=%v recovery=%+v", h.restartPending, h.pendingAgentRestore)
	}

	if err := h.Upgrade(context.Background(), req, io.Discard); err != nil {
		t.Fatalf("retry Agent upgrade: %v", err)
	}
	got, readErr = os.ReadFile(target)
	if readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("Agent after retry = %d bytes, err=%v; want candidate", len(got), readErr)
	}
	if scheduleCalls != 2 || !h.restartPending {
		t.Fatalf("retry scheduling calls=%d restartPending=%v", scheduleCalls, h.restartPending)
	}
}

func TestAgentUpgradeRestoreFailureRetainsBackupAndRetryRecovers(t *testing.T) {
	payload := readHostELF(t)
	target := filepath.Join(t.TempDir(), "singbox-deploy-agent")
	old := []byte("known-good-old-agent")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	scheduleErr := errors.New("cannot queue transient restart")
	restoreErr := errors.New("injected restore rename failure")
	scheduleCalls := 0
	failedRestore := false
	h := &agentHandler{
		agentExecutable: func() (string, error) { return target, nil },
		inspectAgent:    func(context.Context, string, string) error { return nil },
		scheduleRestart: func() error {
			scheduleCalls++
			if scheduleCalls == 1 {
				return scheduleErr
			}
			return nil
		},
		renameAgent: func(oldPath, newPath string) error {
			if !failedRestore && newPath == target &&
				strings.Contains(filepath.Base(oldPath), ".copy-") {
				failedRestore = true
				return restoreErr
			}
			return os.Rename(oldPath, newPath)
		},
	}
	req := nodeapi.NewUpgradeRequest("v2.0.0", payload)
	err := h.Upgrade(context.Background(), req, io.Discard)
	if !errors.Is(err, scheduleErr) || !errors.Is(err, restoreErr) {
		t.Fatalf("combined schedule/restore failure = %v", err)
	}
	if h.pendingAgentRestore == nil || h.restartPending {
		t.Fatalf("recovery state = %+v restartPending=%v", h.pendingAgentRestore, h.restartPending)
	}
	backup, readErr := os.ReadFile(agentBackupPath(target))
	if readErr != nil || !bytes.Equal(backup, old) {
		t.Fatalf("retained backup = %q, err=%v; want old executable", backup, readErr)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("committed path before recovery = %d bytes, err=%v; want candidate", len(got), readErr)
	}

	if err := h.Upgrade(context.Background(), req, io.Discard); err != nil {
		t.Fatalf("retry after retained-backup recovery: %v", err)
	}
	if h.pendingAgentRestore != nil || !h.restartPending || scheduleCalls != 2 {
		t.Fatalf("retry state recovery=%+v restart=%v schedules=%d", h.pendingAgentRestore, h.restartPending, scheduleCalls)
	}
	backup, readErr = os.ReadFile(agentBackupPath(target))
	if readErr != nil || !bytes.Equal(backup, old) {
		t.Fatalf("retry backup = %q, err=%v; want recovered old executable", backup, readErr)
	}
}

func TestAgentUpgradeFailurePreservesOldExecutable(t *testing.T) {
	payload := readHostELF(t)
	target := filepath.Join(t.TempDir(), "singbox-deploy-agent")
	old := []byte("known-good-old-agent")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{
		agentExecutable: func() (string, error) { return target, nil },
		inspectAgent: func(context.Context, string, string) error {
			return os.ErrInvalid
		},
		scheduleRestart: func() error {
			t.Fatal("restart scheduled after failed validation")
			return nil
		},
	}
	err := h.Upgrade(context.Background(), nodeapi.NewUpgradeRequest("v2", payload), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "verify staged") {
		t.Fatalf("expected staged validation error, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, old) {
		t.Fatalf("old executable changed after failed upgrade")
	}

	badHash := nodeapi.NewUpgradeRequest("v2", payload)
	badHash.SHA256 = strings.Repeat("0", 64)
	if err := h.Upgrade(context.Background(), badHash, io.Discard); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected SHA-256 mismatch, got %v", err)
	}
	got, _ = os.ReadFile(target)
	if !bytes.Equal(got, old) {
		t.Fatalf("old executable changed after digest failure")
	}
}

func TestAgentUpgradeCommitFailureKeepsOldExecutableAndDoesNotSchedule(t *testing.T) {
	payload := readHostELF(t)
	target := filepath.Join(t.TempDir(), "singbox-deploy-agent")
	old := []byte("known-good-old-agent")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	commitErr := errors.New("injected candidate rename failure")
	h := &agentHandler{
		agentExecutable: func() (string, error) { return target, nil },
		inspectAgent:    func(context.Context, string, string) error { return nil },
		renameAgent: func(oldPath, newPath string) error {
			if newPath == target && strings.Contains(filepath.Base(oldPath), ".upgrade-") {
				return commitErr
			}
			return os.Rename(oldPath, newPath)
		},
		scheduleRestart: func() error {
			t.Fatal("restart scheduled after failed candidate commit")
			return nil
		},
	}
	err := h.Upgrade(
		context.Background(),
		nodeapi.NewUpgradeRequest("v2.0.0", payload),
		io.Discard,
	)
	if !errors.Is(err, commitErr) {
		t.Fatalf("commit failure = %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || !bytes.Equal(got, old) {
		t.Fatalf("Agent after commit failure = %q, err=%v; want old executable", got, readErr)
	}
	if _, statErr := os.Stat(agentBackupPath(target)); !os.IsNotExist(statErr) {
		t.Fatalf("unused backup remains after commit failure: %v", statErr)
	}
}

func TestAgentUpgradeRejectsWrongELFArchitecture(t *testing.T) {
	payload := append([]byte(nil), readHostELF(t)...)
	if len(payload) < 20 {
		t.Fatal("host ELF unexpectedly short")
	}
	other := uint16(elf.EM_X86_64)
	if runtime.GOARCH == "amd64" {
		other = uint16(elf.EM_AARCH64)
	}
	if payload[5] == byte(elf.ELFDATA2MSB) {
		binary.BigEndian.PutUint16(payload[18:20], other)
	} else {
		binary.LittleEndian.PutUint16(payload[18:20], other)
	}
	target := filepath.Join(t.TempDir(), "agent")
	old := []byte("old")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	h := &agentHandler{agentExecutable: func() (string, error) { return target, nil }}
	err := h.Upgrade(context.Background(), nodeapi.NewUpgradeRequest("v2", payload), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "architecture") {
		t.Fatalf("expected architecture rejection, got %v", err)
	}
	got, _ := os.ReadFile(target)
	if !bytes.Equal(got, old) {
		t.Fatal("wrong-architecture payload replaced executable")
	}
}

func TestInspectStagedAgentChecksReportedVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'v3.0.0\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := inspectStagedAgent(context.Background(), path, "v3.0.0"); err != nil {
		t.Fatalf("inspect matching version: %v", err)
	}
	if err := inspectStagedAgent(context.Background(), path, "v4.0.0"); err == nil || !strings.Contains(err.Error(), "reports version") {
		t.Fatalf("expected reported-version mismatch, got %v", err)
	}
}

func TestScheduleAgentRestartUsesIndependentSystemdTimerAndReportsFailure(t *testing.T) {
	var gotName string
	var gotArgs []string
	if err := scheduleAgentRestartWith(func(name string, args ...string) ([]byte, error) {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil, nil
	}); err != nil {
		t.Fatalf("schedule Agent restart: %v", err)
	}
	if gotName != "systemd-run" {
		t.Fatalf("restart scheduler command = %q", gotName)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{
		"--collect", "--no-block", "--on-active=1s",
		"systemctl restart singbox-deploy-agent.service",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("restart scheduler args missing %q: %v", want, gotArgs)
		}
	}

	queueErr := errors.New("systemd manager unavailable")
	err := scheduleAgentRestartWith(func(string, ...string) ([]byte, error) {
		return []byte("failed to create transient timer"), queueErr
	})
	if !errors.Is(err, queueErr) || !strings.Contains(err.Error(), "failed to create transient timer") {
		t.Fatalf("restart scheduling error = %v", err)
	}
}

func TestAgentMonitorHandlerUsesSupervisor(t *testing.T) {
	supervisor := &monitorSupervisor{handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/summary" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	h := &agentHandler{monitor: supervisor}
	rec := httptest.NewRecorder()
	h.MonitorHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	inactive := &agentHandler{monitor: &monitorSupervisor{}}
	rec = httptest.NewRecorder()
	inactive.MonitorHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("inactive status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

// A monitor that exits on its own retires itself under the write lock, so an
// intentional stop must not hold that lock while waiting for the sampler to
// finish. This asserts the property directly: the run function only returns
// once it has proved a reader can still enter while stop is waiting.
func TestMonitorSupervisorStopDoesNotHoldLockWhileWaiting(t *testing.T) {
	supervisor := newMonitorSupervisor(context.Background(), installedSpokeLayout(t))
	supervisor.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	supervisor.newMonitor = func(_ *monitor.Store, _ monitor.Config) (http.Handler, func(context.Context) error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), func(ctx context.Context) error {
				<-ctx.Done()
				// stop is now blocked on done. Taking the read lock deadlocks if it
				// is waiting while holding the write lock.
				supervisor.mu.RLock()
				supervisor.mu.RUnlock()
				return nil
			}
	}
	supervisor.reload()
	supervisor.mu.RLock()
	started := supervisor.done != nil
	supervisor.mu.RUnlock()
	if !started {
		t.Fatal("monitor did not start from installed spoke state")
	}
	if info, err := os.Stat(filepath.Dir(supervisor.layout.MonitorDB)); err != nil {
		t.Fatalf("monitor store directory was not created: %v", err)
	} else if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("monitor store directory mode = %#o, want 0755", got)
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		supervisor.stop()
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop deadlocked waiting for the monitor to exit")
	}
}

func TestMonitorSupervisorFallsBackToHostUTCWhenNetworkTimeFails(t *testing.T) {
	layout := installedSpokeLayout(t)
	supervisor := newMonitorSupervisor(context.Background(), layout)
	supervisor.newNetworkClock = func(context.Context) (*monitor.NetworkClock, error) {
		return nil, errors.New("all network time sources unavailable")
	}
	cfg, err := supervisor.buildConfig(state.NewStore(layout.StateDir))
	if err != nil {
		t.Fatalf("buildConfig rejected host-clock fallback: %v", err)
	}
	now := cfg.Now()
	if now.Location() != time.UTC {
		t.Fatalf("fallback clock location = %v, want UTC", now.Location())
	}
	if delta := time.Since(now); delta < -time.Second || delta > time.Second {
		t.Fatalf("fallback clock differs from host time by %v", delta)
	}
}

func TestMonitorSupervisorRetriesAfterUnexpectedExit(t *testing.T) {
	supervisor := newMonitorSupervisor(context.Background(), installedSpokeLayout(t))
	supervisor.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	supervisor.retryDelay = 10 * time.Millisecond

	var (
		mu        sync.Mutex
		starts    int
		restarted = make(chan struct{})
	)
	supervisor.newMonitor = func(_ *monitor.Store, _ monitor.Config) (http.Handler, func(context.Context) error) {
		mu.Lock()
		starts++
		current := starts
		mu.Unlock()
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
		if current == 1 {
			return handler, func(context.Context) error {
				return errors.New("transient sampler failure")
			}
		}
		return handler, func(ctx context.Context) error {
			select {
			case <-restarted:
			default:
				close(restarted)
			}
			<-ctx.Done()
			return nil
		}
	}

	supervisor.reload()
	select {
	case <-restarted:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not retry after an unexpected exit")
	}

	rec := httptest.NewRecorder()
	supervisor.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("retried monitor status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	supervisor.stop()
	mu.Lock()
	startsAfterStop := starts
	mu.Unlock()
	time.Sleep(4 * supervisor.retryDelay)
	mu.Lock()
	defer mu.Unlock()
	if starts != startsAfterStop {
		t.Fatalf("terminal stop allowed another retry: starts %d -> %d", startsAfterStop, starts)
	}
}

// installedSpokeLayout returns a temporary layout whose state files describe an
// installed, monitored spoke.
func installedSpokeLayout(t *testing.T) paths.Layout {
	t.Helper()
	layout := paths.LayoutForRoot(t.TempDir())
	store := state.NewStore(layout.StateDir)
	for name, value := range map[string]string{
		"domain":                   "spoke.example.com\n",
		"monitor":                  "yes\n",
		"monitor_interface":        "lo\n",
		"monitor_interval_seconds": "3600\n",
	} {
		if err := store.WriteString(name, value, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return layout
}

func TestMonitorSupervisorUsesAgentProcessContext(t *testing.T) {
	processCtx, stopProcess := context.WithCancel(context.Background())
	supervisor := newMonitorSupervisor(processCtx, installedSpokeLayout(t))
	supervisor.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	supervisor.newMonitor = func(_ *monitor.Store, _ monitor.Config) (http.Handler, func(context.Context) error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			}
	}
	supervisor.reload()

	supervisor.mu.RLock()
	done := supervisor.done
	handler := supervisor.handler
	supervisor.mu.RUnlock()
	if done == nil || handler == nil {
		t.Fatal("monitor did not start from installed spoke state")
	}

	// A completed/cancelled HTTP request must have no effect on the process-owned
	// monitor lifecycle.
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelRequest()
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil).WithContext(requestCtx)
	rec := httptest.NewRecorder()
	supervisor.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("summary after request cancellation = %d, want 200", rec.Code)
	}
	select {
	case <-done:
		t.Fatal("monitor stopped with an unrelated request context")
	case <-time.After(50 * time.Millisecond):
	}

	stopProcess()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("monitor did not stop with the agent process context")
	}
	supervisor.stop()
}

func TestAgentTeardownRemovesAllWireGuardSecretsAndTemplates(t *testing.T) {
	paths := make(map[string]bool)
	for _, path := range agentTeardownPaths() {
		paths[path] = true
	}
	for _, want := range []string{
		"/etc/wireguard/sbwg0.conf",
		"/etc/wireguard/sbwg0.conf.singbox-deploy.template",
		"/etc/wireguard/sbwg0.key",
		"/etc/wireguard/sbwg0.key.singbox-deploy.tmp",
		"/usr/bin/singbox-deploy-agent.singbox-deploy-backup",
	} {
		if !paths[want] {
			t.Errorf("teardown does not remove %s", want)
		}
	}
}

func TestLegacyHubArtifactsAreRemovedWhenConvertingToSpoke(t *testing.T) {
	layoutRoot := t.TempDir()
	// Use a synthetic layout only for checking state-relative migration paths;
	// the absolute binary/unit paths are inspected but never removed by this test.
	layout := paths.LayoutForRoot(layoutRoot)
	pathsToRemove := make(map[string]bool)
	for _, path := range legacyHubArtifactPaths(layout) {
		pathsToRemove[path] = true
	}
	for _, want := range []string{
		"/usr/bin/singbox-deploy",
		"/etc/systemd/system/" + system.CertRenewTimer,
		filepath.Join(layout.StateDir, "dns_credentials"),
		filepath.Join(layout.StateDir, "dns_credential"),
		filepath.Join(layout.StateDir, "email"),
		filepath.Join(layout.StateDir, "remotes"),
		filepath.Join(layout.StateDir, "monitor_sources"),
		filepath.Join(layout.StateDir, "spoke_subscriptions"),
		filepath.Join(layout.StateDir, "subscription_groups"),
		filepath.Join(layout.StateDir, "subscription_groups.lock"),
	} {
		if !pathsToRemove[want] {
			t.Errorf("spoke migration does not remove %s", want)
		}
	}
}

// A spoke never publishes subscriptions of its own, so a machine demoted from
// hub to spoke must not keep its group registry — it names nodes this host no
// longer manages and stores the salt behind every URL the old hub served.
func TestLegacyHubArtifactRemovalDropsTheSubscriptionGroupRegistry(t *testing.T) {
	layout := paths.LayoutForRoot(t.TempDir())
	groups := filepath.Join(layout.StateDir, "subscription_groups")
	if err := os.MkdirAll(filepath.Join(groups, "001"), 0o700); err != nil {
		t.Fatalf("seed group registry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(groups, "001", "salt"), []byte("hubsalt\n"), 0o600); err != nil {
		t.Fatalf("seed group salt: %v", err)
	}
	lock := filepath.Join(layout.StateDir, "subscription_groups.lock")
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatalf("seed group lock: %v", err)
	}

	if err := removeLegacyHubArtifacts(layout); err != nil {
		t.Fatalf("removeLegacyHubArtifacts: %v", err)
	}

	for _, path := range []string{groups, lock} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Errorf("%s survived the conversion to a spoke: %v", path, err)
		}
	}
}

func TestDisableLegacyHubServicesSkipsMissingUnits(t *testing.T) {
	systemdDir := t.TempDir()
	runner := &handlerRecordingRunner{}

	disableLegacyHubServices(runner, systemdDir)

	if len(runner.commands) != 0 {
		t.Fatalf("commands for missing legacy units = %v, want none", runner.commands)
	}
}

func TestDisableLegacyHubServicesDisablesInstalledUnits(t *testing.T) {
	systemdDir := t.TempDir()
	for _, unit := range []string{system.CertRenewTimer, system.MonitorService} {
		if err := os.WriteFile(filepath.Join(systemdDir, unit), []byte("[Unit]\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", unit, err)
		}
	}
	runner := &handlerRecordingRunner{}

	disableLegacyHubServices(runner, systemdDir)

	want := []string{
		"systemctl disable --now " + system.CertRenewTimer,
		"systemctl disable --now " + system.MonitorService,
	}
	if strings.Join(runner.commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %v, want %v", runner.commands, want)
	}
}

func TestStopAgentAndOverlayClearsWireGuardUnitState(t *testing.T) {
	var commands []string
	stopAgentAndOverlayWith(func(name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return nil
	})

	want := []string{
		"ip link delete dev sbwg0",
		"systemctl stop wg-quick@sbwg0.service",
		"systemctl reset-failed wg-quick@sbwg0.service",
		"systemctl --no-block stop singbox-deploy-agent.service",
	}
	if strings.Join(commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %v, want %v", commands, want)
	}
}

func readHostELF(t *testing.T) []byte {
	t.Helper()
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("agent upgrades support linux amd64/arm64")
	}
	b, err := os.ReadFile("/bin/true")
	if err != nil {
		t.Fatalf("read host ELF: %v", err)
	}
	return b
}
