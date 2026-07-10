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
)

// fakeHandler is a scripted agent implementation for the round-trip test.
type fakeHandler struct {
	installReq InstallRequest
	upgradeReq UpgradeRequest
	failWith   string
	monitor    http.Handler
}

func (h *fakeHandler) Health() HealthResponse {
	return HealthResponse{OK: true, Version: "test", Installed: true, SingBoxActive: true}
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

func (h *fakeHandler) Subscription(format string) ([]byte, error) {
	return []byte("body-for-" + format), nil
}

func (h *fakeHandler) MonitorHandler() http.Handler { return h.monitor }

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
	if !health.OK || health.Version != "test" || !health.Installed {
		t.Fatalf("unexpected health: %+v", health)
	}
}

func TestInstallStreamsLogAndSucceeds(t *testing.T) {
	h := &fakeHandler{}
	client, closeFn := newTestServer(t, h, "secret")
	defer closeFn()
	var log bytes.Buffer
	req := InstallRequest{Domain: "spoke.example.com", EnabledProtocols: []string{"hysteria2"}}
	if err := client.Install(context.Background(), req, &log); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if h.installReq.Domain != "spoke.example.com" {
		t.Fatalf("agent received wrong request: %+v", h.installReq)
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
	err := client.Install(context.Background(), InstallRequest{}, io.Discard)
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
		{path: "/api/subscription?format=default", wrong: http.MethodPost, allow: http.MethodGet},
		{path: "/api/monitor/summary", wrong: http.MethodPost, allow: http.MethodGet},
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
	} {
		body, err := client.Monitor(context.Background(), tc.endpoint)
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
	if _, err := client.Monitor(context.Background(), MonitorEndpoint("http://169.254.169.254")); err == nil || !strings.Contains(err.Error(), "unsupported") {
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
