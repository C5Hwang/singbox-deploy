package hubctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/bootstrap"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/wgnet"
)

// stagingCoreHandler is a fleet agent that can stage core archives. When
// githubBlocked is set its own GitHub download fails, as from mainland China.
type stagingCoreHandler struct {
	*fleetCoreHandler
	githubBlocked bool

	stageMu sync.Mutex
	stages  []nodeapi.CoreStageRequest
}

func (h *stagingCoreHandler) StageCore(_ context.Context, req nodeapi.CoreStageRequest, _ io.Writer) error {
	h.stageMu.Lock()
	defer h.stageMu.Unlock()
	h.stages = append(h.stages, req)
	if !req.Pushed() && h.githubBlocked {
		return errors.New("download sing-box from GitHub: connection reset")
	}
	return nil
}

func (h *stagingCoreHandler) stageLog() []string {
	h.stageMu.Lock()
	defer h.stageMu.Unlock()
	var out []string
	for _, req := range h.stages {
		kind := "direct"
		if req.Pushed() {
			kind = "pushed:" + req.ArchiveName
		}
		out = append(out, req.SingBoxVersion+" "+kind)
	}
	return out
}

func newStagingTestNode(t *testing.T, id, alias, version string, githubBlocked bool) (fleetCoreTestNode, *stagingCoreHandler) {
	t.Helper()
	base := newFleetCoreTestNode(t, id, alias, version)
	handler := &stagingCoreHandler{fleetCoreHandler: base.handler, githubBlocked: githubBlocked}
	base.server.Close()
	server := httptest.NewServer((&nodeapi.Server{Token: base.node.Token, Handler: handler}).Mux())
	t.Cleanup(server.Close)
	base.server = server
	base.node.Arch = "amd64"
	return base, handler
}

// fakeCoreArchive builds a self-consistent push payload without GitHub.
func fakeCoreArchive(tag, arch string) nodeapi.CoreStageRequest {
	archive := []byte("archive " + tag + " " + arch)
	sum := sha256.Sum256(archive)
	return nodeapi.CoreStageRequest{
		SingBoxVersion:  tag,
		ArchiveName:     "sing-box-" + strings.TrimPrefix(tag, "v") + "-linux-" + arch + ".tar.gz",
		ArchiveSHA256:   hex.EncodeToString(sum[:]),
		Archive:         archive,
		ReleaseMetadata: []byte(`{"assets":[]}`),
	}
}

func countingCoreFetcher(calls *[]string) func(context.Context, string, string) (nodeapi.CoreStageRequest, error) {
	return func(_ context.Context, tag, arch string) (nodeapi.CoreStageRequest, error) {
		*calls = append(*calls, tag+"/"+arch)
		return fakeCoreArchive(tag, arch), nil
	}
}

func TestStageSpokeCorePrefersSpokeDownload(t *testing.T) {
	spoke, handler := newStagingTestNode(t, "11111111111111111111111111111111", "Tokyo", "v1.12.3", false)
	localVersion := "v1.12.4"
	controller := fleetCoreController(t, &localVersion, spoke)
	var fetches []string
	controller.FetchCoreArchive = countingCoreFetcher(&fetches)
	controller.defaults()

	if err := controller.stageSpokeCore(context.Background(), spoke.node, "v1.12.4", controller.newCoreArchives(), io.Discard); err != nil {
		t.Fatalf("stageSpokeCore: %v", err)
	}
	if got := handler.stageLog(); len(got) != 1 || got[0] != "v1.12.4 direct" {
		t.Fatalf("stages = %v", got)
	}
	if len(fetches) != 0 {
		t.Fatalf("Hub downloaded %v although the spoke reached GitHub", fetches)
	}
}

func TestStageSpokeCorePushesWhenSpokeCannotReachGitHub(t *testing.T) {
	spoke, handler := newStagingTestNode(t, "11111111111111111111111111111111", "Shanghai", "v1.12.3", true)
	localVersion := "v1.12.4"
	controller := fleetCoreController(t, &localVersion, spoke)
	var fetches []string
	controller.FetchCoreArchive = countingCoreFetcher(&fetches)
	controller.defaults()

	var log bytes.Buffer
	if err := controller.stageSpokeCore(context.Background(), spoke.node, "v1.12.4", controller.newCoreArchives(), &log); err != nil {
		t.Fatalf("stageSpokeCore: %v", err)
	}
	want := []string{"v1.12.4 direct", "v1.12.4 pushed:sing-box-1.12.4-linux-amd64.tar.gz"}
	if got := handler.stageLog(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("stages = %v, want %v", got, want)
	}
	if len(fetches) != 1 || fetches[0] != "v1.12.4/amd64" {
		t.Fatalf("Hub fetches = %v", fetches)
	}
	if !strings.Contains(log.String(), "connection reset") || !strings.Contains(log.String(), "from the Hub over WireGuard") {
		t.Fatalf("log does not explain the fallback: %q", log.String())
	}
}

func TestStageSpokeCoreReportsBothFailures(t *testing.T) {
	spoke, _ := newStagingTestNode(t, "11111111111111111111111111111111", "Shanghai", "v1.12.3", true)
	localVersion := "v1.12.4"
	controller := fleetCoreController(t, &localVersion, spoke)
	controller.FetchCoreArchive = func(context.Context, string, string) (nodeapi.CoreStageRequest, error) {
		return nodeapi.CoreStageRequest{}, errors.New("hub DNS failure")
	}
	controller.defaults()

	err := controller.stageSpokeCore(context.Background(), spoke.node, "v1.12.4", controller.newCoreArchives(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "connection reset") || !strings.Contains(err.Error(), "hub DNS failure") {
		t.Fatalf("stageSpokeCore error = %v", err)
	}

	unknownArch := spoke.node
	unknownArch.Arch = ""
	err = controller.stageSpokeCore(context.Background(), unknownArch, "v1.12.4", controller.newCoreArchives(), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "architecture is unknown") {
		t.Fatalf("unknown architecture error = %v", err)
	}
}

func TestStageSpokeCoreLeavesLegacyAgentToDownload(t *testing.T) {
	legacy := newFleetCoreTestNode(t, "11111111111111111111111111111111", "London", "v1.12.3")
	localVersion := "v1.12.4"
	controller := fleetCoreController(t, &localVersion, legacy)
	controller.FetchCoreArchive = func(context.Context, string, string) (nodeapi.CoreStageRequest, error) {
		t.Fatal("a legacy agent must not trigger a Hub download")
		return nodeapi.CoreStageRequest{}, nil
	}
	controller.defaults()
	if err := controller.stageSpokeCore(context.Background(), legacy.node, "v1.12.4", controller.newCoreArchives(), io.Discard); err != nil {
		t.Fatalf("stageSpokeCore: %v", err)
	}
}

func TestChangeFleetCoreStagesEverySpokeAndFetchesOncePerArch(t *testing.T) {
	first, firstHandler := newStagingTestNode(t, "11111111111111111111111111111111", "Shanghai", "v1.12.3", true)
	second, secondHandler := newStagingTestNode(t, "22222222222222222222222222222222", "Beijing", "v1.12.3", true)
	third, thirdHandler := newStagingTestNode(t, "33333333333333333333333333333333", "Tokyo", "v1.12.3", false)
	localVersion := "v1.12.3"
	controller := fleetCoreController(t, &localVersion, first, second, third)
	var fetches []string
	controller.FetchCoreArchive = countingCoreFetcher(&fetches)
	controller.ChangeLocalCore = func(_ context.Context, tag string, _ io.Writer) error {
		localVersion = tag
		return nil
	}

	if err := controller.ChangeFleetCore(context.Background(), "v1.12.4", io.Discard); err != nil {
		t.Fatalf("ChangeFleetCore: %v", err)
	}
	if len(fetches) != 1 || fetches[0] != "v1.12.4/amd64" {
		t.Fatalf("Hub fetches = %v, want one per release and architecture", fetches)
	}
	for name, handler := range map[string]*stagingCoreHandler{"Shanghai": firstHandler, "Beijing": secondHandler} {
		if got := handler.stageLog(); len(got) != 2 || !strings.HasPrefix(got[1], "v1.12.4 pushed:") {
			t.Fatalf("%s stages = %v", name, got)
		}
	}
	if got := thirdHandler.stageLog(); len(got) != 1 || got[0] != "v1.12.4 direct" {
		t.Fatalf("Tokyo stages = %v", got)
	}
	for _, handler := range []*stagingCoreHandler{firstHandler, secondHandler, thirdHandler} {
		if version, _ := handler.snapshot(); version != "v1.12.4" {
			t.Fatalf("spoke version = %s", version)
		}
	}
}

func TestChangeFleetCoreRollbackRestagesPreviousRelease(t *testing.T) {
	first, firstHandler := newStagingTestNode(t, "11111111111111111111111111111111", "Shanghai", "v1.12.3", true)
	second, secondHandler := newStagingTestNode(t, "22222222222222222222222222222222", "Beijing", "v1.12.3", true)
	secondHandler.failTarget = "v1.12.4"
	localVersion := "v1.12.3"
	controller := fleetCoreController(t, &localVersion, first, second)
	var fetches []string
	controller.FetchCoreArchive = countingCoreFetcher(&fetches)

	err := controller.ChangeFleetCore(context.Background(), "v1.12.4", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "injected core change failure") {
		t.Fatalf("ChangeFleetCore error = %v", err)
	}
	if version, _ := firstHandler.snapshot(); version != "v1.12.3" {
		t.Fatalf("first spoke not rolled back: %s", version)
	}
	stages := strings.Join(firstHandler.stageLog(), ",")
	if !strings.Contains(stages, "v1.12.3 pushed:") {
		t.Fatalf("rollback did not restage the previous release: %s", stages)
	}
	want := []string{"v1.12.4/amd64", "v1.12.3/amd64"}
	if strings.Join(fetches, ",") != strings.Join(want, ",") {
		t.Fatalf("Hub fetches = %v, want %v", fetches, want)
	}
}

// chinaSpokeAgent wraps the lifecycle agent with a staging endpoint whose own
// GitHub download always fails, recording the order of stage and install.
type chinaSpokeAgent struct {
	*lifecycleHandler
	mu     sync.Mutex
	events []string
}

func (a *chinaSpokeAgent) record(event string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.events = append(a.events, event)
}

func (a *chinaSpokeAgent) StageCore(_ context.Context, req nodeapi.CoreStageRequest, _ io.Writer) error {
	if !req.Pushed() {
		a.record("stage direct " + req.SingBoxVersion)
		return errors.New("download sing-box from GitHub: i/o timeout")
	}
	a.record("stage pushed " + req.ArchiveName)
	return nil
}

func (a *chinaSpokeAgent) Install(ctx context.Context, req nodeapi.InstallRequest, log io.Writer) error {
	a.record("install " + req.SingBoxVersion)
	return a.lifecycleHandler.Install(ctx, req, log)
}

func TestAddNodePushesCoreToSpokeWithoutGitHubBeforeInstall(t *testing.T) {
	dir := t.TempDir()
	layout := paths.LayoutForRoot(dir)
	hubKeyPair, err := wgnet.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	if err := nodes.SaveHubIdentity(layout, nodes.HubIdentity{
		PrivateKey: hubKeyPair.PrivateKey, PublicKey: hubKeyPair.PublicKey, EndpointHost: "hub.example.com",
		ListenPort: wgnet.DefaultListenPort, Subnet: wgnet.DefaultSubnet,
	}); err != nil {
		t.Fatal(err)
	}
	if err := nodes.SetHubInstalled(layout, true); err != nil {
		t.Fatal(err)
	}
	writeCertificatePair(t, layout, "cn.example.com")

	// The install fails after staging so the test needs no post-install
	// health convergence; only the ordering up to install matters here.
	agent := &chinaSpokeAgent{lifecycleHandler: &lifecycleHandler{
		health:     nodeapi.HealthResponse{OK: true, Version: "v-test"},
		installErr: errors.New("stop after install request"),
	}}
	srv := httptest.NewServer((&nodeapi.Server{Token: "api-token", Handler: agent}).Mux())
	defer srv.Close()
	var fetches []string
	ctrl := &Controller{
		Layout:              layout,
		Runner:              &hubCommandRunner{},
		WGConfDir:           filepath.Join(dir, "wireguard"),
		ExpectedVersion:     "v-test",
		ExpectedCoreVersion: "v1.12.4",
		Bootstrapper: &bootstrap.Bootstrapper{Dial: func(context.Context, bootstrap.Target) (bootstrap.Runner, error) {
			return &bootstrapTestRunner{}, nil
		}},
		AgentBinary: func(string) ([]byte, error) { return []byte("agent"), nil },
		NewClient: func(nodes.Node) *nodeapi.Client {
			return &nodeapi.Client{BaseURL: srv.URL, Token: "api-token", HTTP: srv.Client()}
		},
		FetchCoreArchive: countingCoreFetcher(&fetches),
	}
	_, err = ctrl.AddNode(context.Background(), AddNodeParams{
		Node: bootstrap.Target{
			Host: "198.51.100.8", Port: 22, User: "root", HostKeyFingerprint: "SHA256:test",
		},
		Registry: nodes.Node{Alias: "cn", Domain: "cn.example.com"},
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "stop after install request") {
		t.Fatalf("AddNode error = %v", err)
	}
	agent.mu.Lock()
	events := strings.Join(agent.events, ",")
	agent.mu.Unlock()
	want := "stage direct v1.12.4,stage pushed sing-box-1.12.4-linux-arm64.tar.gz,install v1.12.4"
	if events != want {
		t.Fatalf("agent events = %s, want %s", events, want)
	}
	if strings.Join(fetches, ",") != "v1.12.4/arm64" {
		t.Fatalf("Hub fetches = %v", fetches)
	}
}
