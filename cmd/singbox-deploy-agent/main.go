// Command singbox-deploy-agent is the headless daemon that runs on every spoke.
// It has no TUI: it brings the monitor sampler up in-process (bound to the
// WireGuard overlay address) and serves the hub↔agent control API so the hub
// can install, reconfigure, push certificates to, and uninstall the spoke over
// the overlay. Its version tracks the hub it was shipped with.
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodeapi"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// version is injected at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "singbox-deploy-agent:", err)
		os.Exit(1)
	}
}

func run() error {
	layout := paths.DefaultLayout()
	if err := removeLegacyAgentACMEEmail(layout); err != nil {
		// Keep the authenticated repair surface available. The cleanup is
		// idempotent and will be retried on the next Agent start.
		fmt.Fprintln(os.Stderr, "warning: remove legacy ACME email:", err)
	}
	if err := deploy.RemoveLegacySubscribeToken(layout); err != nil {
		fmt.Fprintln(os.Stderr, "warning: remove legacy subscription token:", err)
	}
	cfg, err := loadAgentConfig(layout)
	if err != nil {
		return err
	}
	if _, err := migrateAgentSubscriptions(context.Background(), layout); err != nil {
		// Keep the authenticated control API available so the Hub can inspect or
		// repair the spoke. The absent marker makes the next Agent start retry.
		fmt.Fprintln(os.Stderr, "warning: migrate subscriptions:", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// The monitor sampler runs in-process and can be restarted after a
	// reconfigure that changes its settings.
	sup := newMonitorSupervisor(ctx, layout)
	sup.reload()
	defer sup.stop()

	handler := &agentHandler{layout: layout, monitor: sup}
	server := &nodeapi.Server{Token: cfg.Token, Handler: handler}
	httpServer := &http.Server{
		Addr:              net.JoinHostPort(cfg.ListenIP, strconv.Itoa(cfg.AgentPort)),
		Handler:           server.Mux(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 << 10,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// agentConfig is the small bootstrap-written configuration the agent needs to
// start: the API token, the overlay address it binds to, and the API port.
type agentConfig struct {
	Token     string
	ListenIP  string
	AgentPort int
}

func loadAgentConfig(layout paths.Layout) (agentConfig, error) {
	store := state.NewStore(agentConfigDir(layout))
	token, err := store.ReadValue("token", true)
	if err != nil {
		return agentConfig{}, fmt.Errorf("agent not provisioned (missing token); the hub writes this during bootstrap: %w", err)
	}
	listenIP, err := store.ReadValue("listen_ip", true)
	if err != nil {
		return agentConfig{}, err
	}
	portStr, err := store.ReadValue("agent_port", false)
	if err != nil {
		return agentConfig{}, err
	}
	port, _ := strconv.Atoi(portStr)
	if port <= 0 {
		port = nodes.DefaultAgentPort
	}
	return agentConfig{Token: token, ListenIP: listenIP, AgentPort: port}, nil
}

func agentConfigDir(layout paths.Layout) string {
	return layout.StateDir + "/agent"
}
