package node

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// Server is the long-running HTTP service that implements the node agent API.
// It binds to its WireGuard IP on APIPort and rejects any request without a
// valid bearer token.
type Server struct {
	Layout    paths.Layout
	State     AgentState
	Runner    system.Runner
	Version   string
	StartedAt time.Time

	// Upgrader handles POST /api/upgrade. nil falls back to defaultUpgrader,
	// which downloads new binaries from GitHub Release.
	Upgrader Upgrader
	// Teardown handles POST /api/teardown. nil falls back to defaultTeardown.
	Teardown Teardowner

	// NowFunc is the time source for uptime. Defaults to time.Now.
	NowFunc func() time.Time
}

// NewServer returns a configured Server ready to be wrapped in http.Server.
func NewServer(layout paths.Layout, state AgentState, runner system.Runner, version string) *Server {
	if runner == nil {
		runner = system.NewExecRunner(nil)
	}
	return &Server{
		Layout:    layout,
		State:     state,
		Runner:    runner,
		Version:   version,
		StartedAt: time.Now(),
		NowFunc:   time.Now,
	}
}

// Handler returns the configured http.Handler. Exposed separately from
// ListenAndServe so tests can hit it via httptest.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/status", s.requireAuth(s.handleStatus))
	mux.HandleFunc("/api/config/update", s.requireAuthPost(s.handleConfigUpdate))
	mux.HandleFunc("/api/config/reload", s.requireAuthPost(s.handleConfigReload))
	mux.HandleFunc("/api/monitor/config", s.requireAuthPost(s.handleMonitorConfig))
	mux.HandleFunc("/api/upgrade", s.requireAuthPost(s.handleUpgrade))
	mux.HandleFunc("/api/cert/deploy", s.requireAuthPost(s.handleCertDeploy))
	mux.HandleFunc("/api/teardown", s.requireAuthPost(s.handleTeardown))
	return mux
}

// ListenAndServe binds to the agent's WireGuard IP on APIPort and serves
// HTTP requests until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr := net.JoinHostPort(s.State.WGIP, strconv.Itoa(APIPort))
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	listenErr := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		listenErr <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-listenErr:
		return err
	}
}

// requireAuth wraps h with bearer token validation.
func (s *Server) requireAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.checkAuth(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// requireAuthPost combines requireAuth with a method check that rejects
// anything other than POST.
func (s *Server) requireAuthPost(h http.HandlerFunc) http.HandlerFunc {
	return s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h(w, r)
	})
}

func (s *Server) checkAuth(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	provided := header[len(prefix):]
	expected := s.State.APIToken
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}

func writeJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// Best-effort: already wrote header, can't recover meaningfully.
		_ = err
	}
}

func decodeJSON(r *http.Request, out any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if len(body) == 0 {
		return nil
	}
	return json.Unmarshal(body, out)
}
