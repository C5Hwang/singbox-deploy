package monitor

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newTokenTestMonitor(t *testing.T, token string) *Monitor {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return New(store, Config{Alias: "local", AccessToken: token}, nil)
}

func apiStatus(t *testing.T, m *Monitor, header, value string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	if header != "" {
		req.Header.Set(header, value)
	}
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, req)
	return rec.Code
}

// An installation made before the token existed records none, and its
// dashboard has to keep working after the upgrade.
func TestAPIWithoutConfiguredTokenStaysOpen(t *testing.T) {
	m := newTokenTestMonitor(t, "")
	if code := apiStatus(t, m, "", ""); code != http.StatusOK {
		t.Fatalf("status = %d, want %d", code, http.StatusOK)
	}
}

func TestAPIRequiresConfiguredToken(t *testing.T) {
	const token = "s3cret-monitor-token"
	m := newTokenTestMonitor(t, token)

	for _, tc := range []struct {
		name   string
		header string
		value  string
		want   int
	}{
		{name: "no credential", want: http.StatusUnauthorized},
		{name: "wrong bearer", header: "Authorization", value: "Bearer nope", want: http.StatusUnauthorized},
		{name: "wrong header token", header: "X-Monitor-Token", value: "nope", want: http.StatusUnauthorized},
		{name: "bearer", header: "Authorization", value: "Bearer " + token, want: http.StatusOK},
		{name: "header token", header: "X-Monitor-Token", value: token, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if code := apiStatus(t, m, tc.header, tc.value); code != tc.want {
				t.Fatalf("status = %d, want %d", code, tc.want)
			}
		})
	}
}

// The query string reaches Nginx logs and browser history, so it must never be
// accepted as a credential.
func TestAPIRejectsTokenPassedInQuery(t *testing.T) {
	const token = "s3cret-monitor-token"
	m := newTokenTestMonitor(t, token)
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/summary?token="+token, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

// The bundle has to load unauthenticated or the dashboard could never ask for
// the token.
func TestStaticUIIsServedWithoutToken(t *testing.T) {
	m := newTokenTestMonitor(t, "s3cret-monitor-token")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatalf("body is not the dashboard bundle: %s", rec.Body.String())
	}
}

func TestAccessTokenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := ReadAccessToken(dir); got != "" {
		t.Fatalf("missing token file read as %q, want empty", got)
	}
	if err := WriteAccessToken(dir, "  written-token  "); err != nil {
		t.Fatalf("WriteAccessToken: %v", err)
	}
	if got := ReadAccessToken(dir); got != "written-token" {
		t.Fatalf("ReadAccessToken = %q, want %q", got, "written-token")
	}
}
