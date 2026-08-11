package nodeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

// fakeHandler is a scripted agent implementation for the round-trip test.
type fakeHandler struct {
	installReq     InstallRequest
	upgradeReq     UpgradeRequest
	coreReq        CoreRequest
	failWith       string
	monitor        http.Handler
	protocolState  ProtocolStateResponse
	trafficUsage   TrafficUsage
	trafficReq     TrafficUsageRequest
	trafficErr     error
	trafficWarning string
}

func (h *fakeHandler) Health() HealthResponse {
	return HealthResponse{
		OK: true, Version: "test", Installed: true,
		SingBoxVersion: "v1.12.4", SingBoxActive: true,
	}
}

func (h *fakeHandler) Install(_ context.Context, req InstallRequest, log io.Writer) error {
	h.installReq = req
	fmt.Fprintln(log, "step 1")
	fmt.Fprintln(log, "step 2")
	if h.failWith != "" {
		return fmt.Errorf("%s", h.failWith)
	}
	return nil
}

func (h *fakeHandler) ApplyCert(_ context.Context, _ CertRequest, log io.Writer) error {
	fmt.Fprintln(log, "cert applied")
	return nil
}

func (h *fakeHandler) Uninstall(_ context.Context, _ UninstallRequest, log io.Writer) error {
	fmt.Fprintln(log, "removed")
	return nil
}

func (h *fakeHandler) Upgrade(_ context.Context, req UpgradeRequest, log io.Writer) error {
	h.upgradeReq = req
	fmt.Fprintf(log, "upgraded to %s\n", req.Version)
	if h.failWith != "" {
		return fmt.Errorf("%s", h.failWith)
	}
	return nil
}

func (h *fakeHandler) ChangeCore(_ context.Context, req CoreRequest, log io.Writer) error {
	h.coreReq = req
	fmt.Fprintf(log, "changed core to %s\n", req.SingBoxVersion)
	if h.failWith != "" {
		return fmt.Errorf("%s", h.failWith)
	}
	return nil
}

func (h *fakeHandler) Subscription(format string) ([]byte, error) {
	return []byte("body-for-" + format), nil
}

func (h *fakeHandler) MonitorHandler() http.Handler { return h.monitor }

func (h *fakeHandler) ProtocolState(context.Context) (ProtocolStateResponse, error) {
	return h.protocolState, nil
}

func (h *fakeHandler) TrafficUsage(context.Context) (TrafficUsage, error) {
	return h.trafficUsage, h.trafficErr
}

func (h *fakeHandler) SetTrafficUsage(_ context.Context, req TrafficUsageRequest) (TrafficUsageUpdate, error) {
	h.trafficReq = req
	if h.trafficErr != nil {
		return TrafficUsageUpdate{}, h.trafficErr
	}
	previous := h.trafficUsage
	h.trafficUsage = TrafficUsage{
		InBytes: req.InBytes, OutBytes: req.OutBytes, CycleStart: req.ExpectedCycleStart,
	}
	return TrafficUsageUpdate{
		Previous: previous, Applied: h.trafficUsage, Warning: h.trafficWarning,
	}, nil
}

func newTestServer(t *testing.T, h Handler, token string) (*Client, func()) {
	t.Helper()
	srv := httptest.NewServer((&Server{Token: token, Handler: h}).Mux())
	client := &Client{BaseURL: srv.URL, Token: token, HTTP: srv.Client()}
	return client, srv.Close
}

func TestHealthRoundTrip(t *testing.T) {
	client, closeFn := newTestServer(t, &fakeHandler{}, "secret")
	defer closeFn()
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if !health.OK || health.Version != "test" || !health.Installed ||
		health.SingBoxVersion != "v1.12.4" {
		t.Fatalf("unexpected health: %+v", health)
	}
}

func TestProtocolStateRoundTripAndCredentialValidation(t *testing.T) {
	creds := testProtocolCredentials(t)
	state := ProtocolStateResponse{
		Domain: "spoke.example.com", EnabledProtocols: []string{"tuic"},
		Ports: PortSet{TUIC: 10443}, Credentials: creds,
	}
	revision, err := ProtocolStateRevision(state)
	if err != nil {
		t.Fatal(err)
	}
	state.Revision = revision
	h := &fakeHandler{protocolState: state}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	got, err := client.ProtocolState(context.Background())
	if err != nil {
		t.Fatalf("ProtocolState: %v", err)
	}
	if got.Domain != h.protocolState.Domain || got.Ports.TUIC != 10443 ||
		got.Credentials.TUICPassword != creds.TUICPassword || got.Revision != revision {
		t.Fatalf("protocol state = %+v", got)
	}
	tampered := state
	tampered.Ports.TUIC++
	if err := ValidateProtocolStateResponse(tampered); err == nil ||
		!strings.Contains(err.Error(), "revision does not match") {
		t.Fatalf("tampered protocol state validation = %v", err)
	}

	bad := creds
	bad.RealityPublicKey = creds.RealityPrivateKey
	if err := ValidateProtocolCredentials(&bad); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched Reality key validation = %v", err)
	}
	badTarget := creds
	badTarget.TUICUUID = "not-a-uuid"
	if err := client.Install(context.Background(), InstallRequest{
		ConfigOnly: true, ExpectedProtocolRevision: revision,
		ProtocolPatch: &ProtocolPatch{
			Protocol: "tuic", Port: 10443, Credentials: badTarget,
		},
	}, io.Discard); err == nil {
		t.Fatal("client accepted invalid credential override")
	}
	if h.installReq.ProtocolPatch != nil {
		t.Fatal("invalid credentials reached the Agent install handler")
	}
	if err := ValidateInstallSingBoxVersion(InstallRequest{
		ConfigOnly: true, ExpectedProtocolRevision: "not-a-digest",
	}); err == nil {
		t.Fatal("revision without a protocol patch was accepted")
	}
	for name, req := range map[string]InstallRequest{
		"missing revision": {
			ConfigOnly:    true,
			ProtocolPatch: &ProtocolPatch{Protocol: "tuic", Port: 10443, Credentials: creds},
		},
		"not config only": {
			ExpectedProtocolRevision: revision,
			ProtocolPatch:            &ProtocolPatch{Protocol: "tuic", Port: 10443, Credentials: creds},
		},
		"unsupported protocol": {
			ConfigOnly: true, ExpectedProtocolRevision: revision,
			ProtocolPatch: &ProtocolPatch{Protocol: "ssh", Port: 10443, Credentials: creds},
		},
		"invalid port": {
			ConfigOnly: true, ExpectedProtocolRevision: revision,
			ProtocolPatch: &ProtocolPatch{Protocol: "tuic", Port: 0, Credentials: creds},
		},
		"replacement missing revision": {
			ConfigOnly: true, ReplaceProtocolState: true,
		},
		"replacement not config only": {
			ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
		},
		"patch and replacement": {
			ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
			ProtocolPatch: &ProtocolPatch{Protocol: "tuic", Port: 10443, Credentials: creds},
		},
		"replacement empty protocols": {
			ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
		},
		"replacement unknown protocol": {
			ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
			EnabledProtocols: []string{"ssh"},
		},
		"replacement duplicate protocol": {
			ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
			EnabledProtocols: []string{"hysteria2", "hysteria2"},
			Ports:            PortSet{Hysteria2: 9443},
		},
		"replacement missing enabled port": {
			ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
			EnabledProtocols: []string{"tuic"},
		},
		"replacement out of range port": {
			ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
			EnabledProtocols: []string{"anytls"},
			Ports:            PortSet{AnyTLS: 65536},
		},
		"replacement invalid handshake port": {
			ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
			EnabledProtocols:     []string{"hysteria2"},
			Ports:                PortSet{Hysteria2: 9443},
			RealityHandshakePort: -1,
		},
	} {
		if err := ValidateInstallSingBoxVersion(req); err == nil {
			t.Errorf("%s request was accepted", name)
		}
	}
	if err := ValidateInstallSingBoxVersion(InstallRequest{
		ConfigOnly: true, ReplaceProtocolState: true, ExpectedProtocolRevision: revision,
		EnabledProtocols: []string{"hysteria2"}, Ports: PortSet{Hysteria2: 9443},
	}); err != nil {
		t.Fatalf("valid complete protocol replacement rejected: %v", err)
	}
}

func TestTrafficUsageRoundTripAndCycleConflict(t *testing.T) {
	h := &fakeHandler{trafficUsage: TrafficUsage{
		InBytes: 100, OutBytes: 200, CycleStart: 1_782_864_000,
	}, trafficWarning: "usage committed; quota state needs inspection"}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	got, err := client.TrafficUsage(context.Background())
	if err != nil {
		t.Fatalf("TrafficUsage: %v", err)
	}
	if got != h.trafficUsage {
		t.Fatalf("TrafficUsage = %+v, want %+v", got, h.trafficUsage)
	}
	req := TrafficUsageRequest{
		InBytes: 300, OutBytes: 400, ExpectedCycleStart: got.CycleStart,
	}
	updated, err := client.SetTrafficUsage(context.Background(), req)
	if err != nil {
		t.Fatalf("SetTrafficUsage: %v", err)
	}
	if h.trafficReq != req || updated.Previous != got ||
		updated.Applied.InBytes != req.InBytes ||
		updated.Applied.OutBytes != req.OutBytes ||
		updated.Applied.CycleStart != req.ExpectedCycleStart ||
		updated.Warning != h.trafficWarning {
		t.Fatalf("traffic update: request=%+v response=%+v", h.trafficReq, updated)
	}

	h.trafficErr = TrafficCycleConflict()
	_, err = client.SetTrafficUsage(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "409") || !IsTrafficCycleConflict(err) {
		t.Fatalf("cycle conflict = %v", err)
	}
}

func TestTrafficUsageRoundTripPreservesMaximallyEscapedWarning(t *testing.T) {
	const cycleStart = int64(1_782_864_000)
	warning := strings.Repeat("<", 2048)
	h := &fakeHandler{
		trafficUsage:   TrafficUsage{InBytes: 1, OutBytes: 2, CycleStart: cycleStart},
		trafficWarning: warning,
	}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	update, err := client.SetTrafficUsage(context.Background(), TrafficUsageRequest{
		InBytes: 3, OutBytes: 4, ExpectedCycleStart: cycleStart,
	})
	if err != nil {
		t.Fatalf("SetTrafficUsage with escaped warning: %v", err)
	}
	if update.Warning != warning || update.Previous.InBytes != 1 ||
		update.Applied.InBytes != 3 {
		t.Fatalf("traffic usage update was not preserved: previous=%+v applied=%+v warning-bytes=%d",
			update.Previous, update.Applied, len(update.Warning))
	}
}

func TestTrafficUsageValidationAndUnsupportedAgent(t *testing.T) {
	h := &fakeHandler{}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	if _, err := client.SetTrafficUsage(context.Background(), TrafficUsageRequest{
		InBytes: 1, OutBytes: 2,
	}); err == nil || !strings.Contains(err.Error(), "cycle start") {
		t.Fatalf("invalid usage request = %v", err)
	}
	if h.trafficReq != (TrafficUsageRequest{}) {
		t.Fatalf("invalid request reached Agent: %+v", h.trafficReq)
	}

	type legacyHandler struct{ Handler }
	legacyClient, legacyClose := newTestServer(t, legacyHandler{Handler: &fakeHandler{}}, "secret")
	defer legacyClose()
	_, err := legacyClient.TrafficUsage(context.Background())
	if err == nil || !strings.Contains(err.Error(), "501") {
		t.Fatalf("legacy Agent error = %v", err)
	}
}

func TestTrafficUsagePutRequiresAuthAndStrictCompleteJSON(t *testing.T) {
	valid := `{"inBytes":1,"outBytes":2,"expectedCycleStart":1782864000}`
	for name, authorization := range map[string]string{
		"missing": "",
		"wrong":   "Bearer wrong",
	} {
		t.Run("auth-"+name, func(t *testing.T) {
			h := &fakeHandler{}
			handler := (&Server{Token: "secret", Handler: h}).Mux()
			req := httptest.NewRequest(http.MethodPut, "/api/monitor/usage", strings.NewReader(valid))
			if authorization != "" {
				req.Header.Set("Authorization", authorization)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized || h.trafficReq != (TrafficUsageRequest{}) {
				t.Fatalf("status=%d request=%+v", rec.Code, h.trafficReq)
			}
		})
	}

	for name, body := range map[string]string{
		"missing field":    `{"inBytes":1,"expectedCycleStart":1782864000}`,
		"unknown field":    `{"inBytes":1,"outBytes":2,"expectedCycleStart":1782864000,"outBytez":3}`,
		"trailing value":   valid + ` {}`,
		"trailing garbage": valid + ` trailing`,
		"oversized":        `{"inBytes":` + strings.Repeat("1", 5000) + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			h := &fakeHandler{}
			handler := (&Server{Token: "secret", Handler: h}).Mux()
			req := httptest.NewRequest(http.MethodPut, "/api/monitor/usage", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || h.trafficReq != (TrafficUsageRequest{}) {
				t.Fatalf("status=%d request=%+v body=%q", rec.Code, h.trafficReq, rec.Body.String())
			}
		})
	}
}

func TestLegacyProtocolStateAndPatchValidateOnlyEnabledTarget(t *testing.T) {
	legacyCreds := ProtocolCredentials{HysteriaPassword: "legacy-hysteria-password"}
	state := ProtocolStateResponse{
		Domain: "legacy.example.com", EnabledProtocols: []string{"hysteria2"},
		Ports: PortSet{Hysteria2: 9443}, Credentials: legacyCreds,
	}
	revision, err := ProtocolStateRevision(state)
	if err != nil {
		t.Fatal(err)
	}
	state.Revision = revision
	h := &fakeHandler{protocolState: state}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	got, err := client.ProtocolState(context.Background())
	if err != nil {
		t.Fatalf("legacy ProtocolState: %v", err)
	}
	if got.Credentials.HysteriaPassword != legacyCreds.HysteriaPassword {
		t.Fatalf("legacy protocol state = %+v", got)
	}

	patch := ProtocolPatch{
		Protocol: "hysteria2", Port: 19443,
		Credentials: ProtocolCredentials{HysteriaPassword: "rotated-password"},
	}
	if err := client.Install(context.Background(), InstallRequest{
		ConfigOnly: true, ExpectedProtocolRevision: revision, ProtocolPatch: &patch,
	}, io.Discard); err != nil {
		t.Fatalf("legacy Hysteria2 patch: %v", err)
	}
	if h.installReq.ProtocolPatch == nil ||
		h.installReq.ProtocolPatch.Credentials.HysteriaPassword != "rotated-password" {
		t.Fatalf("legacy patch did not reach handler: %+v", h.installReq)
	}

	missingTarget := patch
	missingTarget.Credentials.HysteriaPassword = ""
	if err := ValidateProtocolPatch(missingTarget); err == nil ||
		!strings.Contains(err.Error(), "Hysteria2 password") {
		t.Fatalf("missing target credential validation = %v", err)
	}
	wrongTarget := patch
	wrongTarget.Protocol = "tuic"
	if err := ValidateProtocolPatch(wrongTarget); err == nil ||
		!strings.Contains(err.Error(), "TUIC UUID") {
		t.Fatalf("missing TUIC target credentials validation = %v", err)
	}

	// Disabled credentials are not required, but remain covered by the
	// revision so an unseen change cannot pass the CAS.
	tampered := state
	tampered.Credentials.TUICPassword = "disabled-but-changed"
	if err := ValidateProtocolStateResponse(tampered); err == nil ||
		!strings.Contains(err.Error(), "revision does not match") {
		t.Fatalf("disabled credential was omitted from revision coverage: %v", err)
	}
}

func testProtocolCredentials(t *testing.T) ProtocolCredentials {
	t.Helper()
	creds, err := deploy.GenerateCredentials()
	if err != nil {
		t.Fatal(err)
	}
	return ProtocolCredentials{
		RealityVisionUUID: creds.RealityVisionUUID,
		RealityGRPCUUID:   creds.RealityGRPCUUID,
		HysteriaPassword:  creds.HysteriaPassword,
		TUICUUID:          creds.TUICUUID,
		TUICPassword:      creds.TUICPassword,
		AnyTLSPassword:    creds.AnyTLSPassword,
		RealityPrivateKey: creds.RealityPrivateKey,
		RealityPublicKey:  creds.RealityPublicKey,
		RealityShortID:    creds.RealityShortID,
	}
}

func TestStableSingBoxVersionValidationAndNormalization(t *testing.T) {
	for _, tag := range []string{"v0.0.0", "v1.12.4", "v12.345.6789"} {
		if err := ValidateStableSingBoxTag(tag); err != nil {
			t.Errorf("valid stable tag %q rejected: %v", tag, err)
		}
	}
	for _, tag := range []string{
		"", "1.12.4", "v1", "v1.12", "v1.12.04", "v1.12.4-rc.1",
		"v1.12.4+build.1", " v1.12.4", "v1.12.4 ", "latest",
	} {
		if err := ValidateStableSingBoxTag(tag); err == nil {
			t.Errorf("invalid or non-stable tag %q accepted", tag)
		}
	}
	for raw, want := range map[string]string{
		"1.12.4":  "v1.12.4",
		"v1.12.4": "v1.12.4",
	} {
		got, err := NormalizeSingBoxVersion(raw)
		if err != nil || got != want {
			t.Errorf("NormalizeSingBoxVersion(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{"", "latest", "1.12.4-rc.1", "1.12"} {
		if _, err := NormalizeSingBoxVersion(raw); err == nil {
			t.Errorf("non-exact reported version %q normalized", raw)
		}
	}
}

func TestInstallSingBoxVersionValidation(t *testing.T) {
	if err := ValidateInstallSingBoxVersion(InstallRequest{
		SingBoxVersion: "v1.12.4",
	}); err != nil {
		t.Fatalf("valid full-install pin rejected: %v", err)
	}
	if err := ValidateInstallSingBoxVersion(InstallRequest{}); err == nil ||
		!strings.Contains(err.Error(), "required for full install") {
		t.Fatalf("missing full-install pin error = %v", err)
	}
	if err := ValidateInstallSingBoxVersion(InstallRequest{ConfigOnly: true}); err != nil {
		t.Fatalf("config-only request should not require a core download pin: %v", err)
	}
	if err := ValidateInstallSingBoxVersion(InstallRequest{
		ConfigOnly: true, SingBoxVersion: "latest",
	}); err == nil {
		t.Fatal("config-only request accepted a malformed optional core tag")
	}
}

func TestValidateInstallTransactionID(t *testing.T) {
	if err := ValidateInstallTransactionID("0123456789abcdef0123456789abcdef"); err != nil {
		t.Fatalf("valid transaction ID rejected: %v", err)
	}
	for _, value := range []string{"", "short", "0123456789ABCDEF0123456789ABCDEF", strings.Repeat("z", 32)} {
		if err := ValidateInstallTransactionID(value); err == nil {
			t.Fatalf("invalid transaction ID %q accepted", value)
		}
	}
}

func TestInstallStreamsLogAndSucceeds(t *testing.T) {
	h := &fakeHandler{}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	var log bytes.Buffer
	req := InstallRequest{
		Domain: "spoke.example.com", EnabledProtocols: []string{"hysteria2"},
		SingBoxVersion: "v1.12.4",
	}
	if err := client.Install(context.Background(), req, &log); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if h.installReq.Domain != "spoke.example.com" {
		t.Fatalf("agent received wrong request: %+v", h.installReq)
	}
	if h.installReq.SingBoxVersion != "v1.12.4" {
		t.Fatalf("agent received unpinned request: %+v", h.installReq)
	}
	out := log.String()
	if !strings.Contains(out, "step 1") || !strings.Contains(out, "step 2") {
		t.Fatalf("log not forwarded: %q", out)
	}
	if strings.Contains(out, "SINGBOX-DEPLOY") {
		t.Fatalf("sentinel leaked into log: %q", out)
	}
}

func TestInstallPropagatesError(t *testing.T) {
	h := &fakeHandler{failWith: "boom happened"}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	err := client.Install(context.Background(), InstallRequest{
		SingBoxVersion: "v1.12.4",
	}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "boom happened") {
		t.Fatalf("expected error to propagate, got %v", err)
	}
}

func TestAuthRejectsBadToken(t *testing.T) {
	client, closeFn := newTestServer(t, &fakeHandler{}, "secret")
	defer closeFn()
	client.Token = "wrong"
	if _, err := client.Health(context.Background()); err == nil {
		t.Fatalf("expected unauthorized error")
	}
}

func TestAuthRequiresExactBearerScheme(t *testing.T) {
	handler := (&Server{Token: "secret", Handler: &fakeHandler{}}).Mux()
	for _, authorization := range []string{"secret", "bearer secret", "Bearer", "Bearer  secret", "Bearer secret "} {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Header.Set("Authorization", authorization)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: status = %d, want %d", authorization, rec.Code, http.StatusUnauthorized)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Add("Authorization", "Bearer secret")
	req.Header.Add("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization headers: status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestEndpointsRejectWrongMethods(t *testing.T) {
	h := &fakeHandler{monitor: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}
	handler := (&Server{Token: "secret", Handler: h}).Mux()
	tests := []struct {
		path  string
		wrong string
		allow string
	}{
		{path: "/api/health", wrong: http.MethodPost, allow: http.MethodGet},
		{path: "/api/install", wrong: http.MethodGet, allow: http.MethodPost},
		{path: "/api/cert", wrong: http.MethodGet, allow: http.MethodPost},
		{path: "/api/uninstall", wrong: http.MethodGet, allow: http.MethodPost},
		{path: "/api/upgrade", wrong: http.MethodGet, allow: http.MethodPost},
		{path: "/api/core", wrong: http.MethodGet, allow: http.MethodPost},
		{path: "/api/subscription?format=default", wrong: http.MethodPost, allow: http.MethodGet},
		{path: "/api/monitor/summary", wrong: http.MethodPost, allow: http.MethodGet},
		{path: "/api/monitor/usage", wrong: http.MethodPost, allow: http.MethodGet + ", " + http.MethodPut},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest(tc.wrong, tc.path, nil)
			req.Header.Set("Authorization", "Bearer secret")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
			}
			if got := rec.Header().Get("Allow"); got != tc.allow {
				t.Fatalf("Allow = %q, want %q", got, tc.allow)
			}
		})
	}
}

func TestSecurityHeadersCoverSuccessAndErrors(t *testing.T) {
	handler := (&Server{Token: "secret", Handler: &fakeHandler{}}).Mux()
	tests := []struct {
		name string
		req  *http.Request
	}{
		{name: "success", req: httptest.NewRequest(http.MethodGet, "/api/health", nil)},
		{name: "not found", req: httptest.NewRequest(http.MethodGet, "/not-an-api", nil)},
	}
	tests[0].req.Header.Set("Authorization", "Bearer secret")
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, tc.req)
			for header, want := range map[string]string{
				"Cache-Control":          "no-store",
				"Referrer-Policy":        "no-referrer",
				"X-Content-Type-Options": "nosniff",
				"X-Frame-Options":        "DENY",
			} {
				if got := rec.Header().Get(header); got != want {
					t.Errorf("%s = %q, want %q", header, got, want)
				}
			}
			if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'none'") {
				t.Errorf("Content-Security-Policy = %q", got)
			}
		})
	}
}

func TestJSONEndpointsRejectSecondValueAndTrailingGarbage(t *testing.T) {
	handler := (&Server{Token: "secret", Handler: &fakeHandler{}}).Mux()
	tests := []struct {
		path    string
		payload any
	}{
		{path: "/api/install", payload: InstallRequest{Domain: "spoke.example.com"}},
		{path: "/api/cert", payload: CertRequest{Domain: "spoke.example.com"}},
		{path: "/api/uninstall", payload: UninstallRequest{KeepOverlay: true}},
		{path: "/api/upgrade", payload: NewUpgradeRequest("v2.0.0", []byte("agent"))},
		{path: "/api/core", payload: CoreRequest{SingBoxVersion: "v1.12.4"}},
	}
	for _, tc := range tests {
		encoded, err := json.Marshal(tc.payload)
		if err != nil {
			t.Fatal(err)
		}
		for _, trailing := range []string{" {}", " trailing"} {
			t.Run(tc.path+trailing, func(t *testing.T) {
				body := append(append([]byte(nil), encoded...), trailing...)
				req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewReader(body))
				req.Header.Set("Authorization", "Bearer secret")
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want %d; body=%q", rec.Code, http.StatusBadRequest, rec.Body.String())
				}
			})
		}
	}
}

func TestDecodeJSONBoundsBodyAndAllowsWhitespace(t *testing.T) {
	var value map[string]any
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{\"ok\":true}  \n\t"))
	if err := decodeJSON(httptest.NewRecorder(), req, &value, 64); err != nil {
		t.Fatalf("whitespace after JSON: %v", err)
	}

	req = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"value":"too large"}`))
	err := decodeJSON(httptest.NewRecorder(), req, &value, 8)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected MaxBytesReader error, got %v", err)
	}
}

func TestSubscriptionFetch(t *testing.T) {
	client, closeFn := newTestServer(t, &fakeHandler{}, "secret")
	defer closeFn()
	body, err := client.Subscription(context.Background(), FormatDefault)
	if err != nil {
		t.Fatalf("Subscription: %v", err)
	}
	if string(body) != "body-for-default" {
		t.Fatalf("unexpected body: %q", body)
	}
}

func TestMonitorFetchUsesFixedAuthenticatedPathAndStripsQuery(t *testing.T) {
	var gotPath, gotQuery, gotAuthorization string
	h := &fakeHandler{monitor: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"trend":[]}`)
	})}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	for _, tc := range []struct {
		endpoint MonitorEndpoint
		path     string
	}{
		{MonitorSummary, "/api/summary"},
		{MonitorTrafficTrend, "/api/traffic-trend"},
		{MonitorTrafficRecent, "/api/traffic-recent"},
		{MonitorResourceTrend, "/api/resource-trend"},
		{MonitorResourceRecent, "/api/resource-recent"},
		{MonitorPingTrend, "/api/ping-trend"},
		{MonitorIPTraffic, "/api/ip-traffic"},
	} {
		body, err := client.Monitor(context.Background(), tc.endpoint, "203.0.113.7")
		if err != nil {
			t.Fatalf("Monitor(%s): %v", tc.endpoint, err)
		}
		if string(body) != `{"trend":[]}` {
			t.Fatalf("Monitor(%s) body = %q", tc.endpoint, body)
		}
		if gotPath != tc.path || gotQuery != "" || gotAuthorization != "" {
			t.Fatalf("Monitor(%s) internal request path=%q query=%q authorization=%q", tc.endpoint, gotPath, gotQuery, gotAuthorization)
		}
	}

	// A direct caller cannot smuggle the monitor's source/proxy selector through
	// the authenticated route either.
	req := httptest.NewRequest(http.MethodGet, "/api/monitor/resource-recent?source=http://evil.invalid", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	(&Server{Token: "secret", Handler: h}).Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || gotPath != "/api/resource-recent" || gotQuery != "" {
		t.Fatalf("smuggled-query request: status=%d path=%q query=%q", rec.Code, gotPath, gotQuery)
	}
}

func TestMonitorRejectsUnknownEndpointWithoutProxying(t *testing.T) {
	called := false
	h := &fakeHandler{monitor: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true })}
	handler := (&Server{Token: "secret", Handler: h}).Mux()
	req := httptest.NewRequest(http.MethodGet, "/api/monitor/169.254.169.254/latest/meta-data", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || called {
		t.Fatalf("unknown route: status=%d called=%v", rec.Code, called)
	}

	client := &Client{BaseURL: "http://unused.invalid", Token: "secret"}
	if _, err := client.Monitor(context.Background(), MonitorEndpoint("http://169.254.169.254"), ""); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected client allow-list rejection, got %v", err)
	}
}

func TestUpgradeRoundTripValidatesDigest(t *testing.T) {
	h := &fakeHandler{}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	payload := []byte("embedded-agent-binary")
	req := NewUpgradeRequest("v2.3.4", payload)
	var log bytes.Buffer
	if err := client.Upgrade(context.Background(), req, &log); err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if h.upgradeReq.Version != "v2.3.4" || !bytes.Equal(h.upgradeReq.Binary, payload) || h.upgradeReq.SHA256 != UpgradeDigest(payload) {
		t.Fatalf("agent received wrong upgrade: %+v", h.upgradeReq)
	}
	if !strings.Contains(log.String(), "upgraded to v2.3.4") {
		t.Fatalf("upgrade log not streamed: %q", log.String())
	}
}

func TestUpgradeRejectsInvalidVersionAndDigestBeforeSend(t *testing.T) {
	h := &fakeHandler{}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	badDigest := NewUpgradeRequest("v2", []byte("agent"))
	badDigest.SHA256 = strings.Repeat("0", 64)
	if err := client.Upgrade(context.Background(), badDigest, io.Discard); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected digest rejection, got %v", err)
	}
	badVersion := NewUpgradeRequest("version with spaces", []byte("agent"))
	if err := client.Upgrade(context.Background(), badVersion, io.Discard); err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("expected version rejection, got %v", err)
	}
	if len(h.upgradeReq.Binary) != 0 {
		t.Fatal("invalid upgrade reached the agent handler")
	}
}

func TestCoreChangeRoundTripIsAuthenticatedAndPinned(t *testing.T) {
	h := &fakeHandler{}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	var log bytes.Buffer
	req := CoreRequest{SingBoxVersion: "v1.12.4"}
	if err := client.ChangeCore(context.Background(), req, &log); err != nil {
		t.Fatalf("ChangeCore: %v", err)
	}
	if h.coreReq != req {
		t.Fatalf("agent received core request %+v, want %+v", h.coreReq, req)
	}
	if !strings.Contains(log.String(), "changed core to v1.12.4") {
		t.Fatalf("core log not streamed: %q", log.String())
	}

	client.Token = "wrong"
	err := client.ChangeCore(context.Background(), CoreRequest{SingBoxVersion: "v1.12.5"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("unauthenticated core change error = %v", err)
	}
	if h.coreReq.SingBoxVersion != "v1.12.4" {
		t.Fatalf("unauthenticated request reached handler: %+v", h.coreReq)
	}
}

func TestCoreChangeRejectsInvalidTagBeforeSend(t *testing.T) {
	h := &fakeHandler{}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	for _, tag := range []string{"", "latest", "1.12.4", "v1.12.4-beta.1"} {
		if err := client.ChangeCore(context.Background(), CoreRequest{
			SingBoxVersion: tag,
		}, io.Discard); err == nil {
			t.Errorf("ChangeCore accepted %q", tag)
		}
	}
	if h.coreReq.SingBoxVersion != "" {
		t.Fatalf("invalid core request reached handler: %+v", h.coreReq)
	}
}

func TestCoreEndpointRejectsInvalidTagBeforeHandler(t *testing.T) {
	h := &fakeHandler{}
	handler := (&Server{Token: "secret", Handler: h}).Mux()
	for _, tag := range []string{"", "latest", "1.12.4", "v1.12.4-rc.1"} {
		body, err := json.Marshal(CoreRequest{SingBoxVersion: tag})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/core", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("tag %q status = %d, want %d; body=%q", tag, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
	}
	if h.coreReq.SingBoxVersion != "" {
		t.Fatalf("invalid core request reached handler: %+v", h.coreReq)
	}
}

func TestGenerateTokenUnique(t *testing.T) {
	a, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	b, _ := GenerateToken()
	if a == b || len(a) != 64 {
		t.Fatalf("tokens not unique/wrong length: %q %q", a, b)
	}
}

// The per-address drill-down is the one endpoint that carries a parameter. It
// is parsed and written back out on both sides, so what reaches the monitor is
// never the caller's text — and anything that is not an address is refused
// before it gets that far.
func TestMonitorIPDetailForwardsOnlyAParsedAddress(t *testing.T) {
	var gotPath, gotQuery string
	h := &fakeHandler{monitor: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		_, _ = io.WriteString(w, `{"ipDetail":{}}`)
	})}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()

	if _, err := client.Monitor(context.Background(), MonitorIPDetail, " 203.0.113.7 "); err != nil {
		t.Fatalf("Monitor(ip-detail): %v", err)
	}
	if gotPath != "/api/ip-detail" || gotQuery != "ip=203.0.113.7" {
		t.Fatalf("internal request path=%q query=%q", gotPath, gotQuery)
	}

	if _, err := client.Monitor(context.Background(), MonitorIPDetail, "../../etc/passwd"); err == nil {
		t.Fatal("client forwarded a non-address")
	}

	// A direct caller that skips the client cannot smuggle text either.
	req := httptest.NewRequest(http.MethodGet, "/api/monitor/ip-detail?ip=%2Fetc%2Fpasswd&source=http://evil.invalid", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	(&Server{Token: "secret", Handler: h}).Mux().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d for a non-address, want 400", rec.Code)
	}
}
