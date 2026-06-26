package node

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/C5Hwang/singbox-deploy/internal/cluster"
	"github.com/C5Hwang/singbox-deploy/internal/config"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/system"
)

// handleStatus returns the agent's health and version info.
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg, _ := deploy.LoadProtocolConfig(s.Layout)
	enabled := make([]string, 0, len(cfg.Enabled))
	for _, p := range cfg.Enabled {
		enabled = append(enabled, string(p))
	}
	status := cluster.Status{
		NodeVersion:      s.Version,
		CoreVersion:      readCoreVersion(s.Layout.SingBoxBin),
		MonitorVersion:   s.Version,
		SingBoxActive:    isUnitActive(s.Runner, system.SingBoxService),
		MonitorActive:    isUnitActive(s.Runner, system.MonitorService),
		EnabledProtocols: enabled,
		Uptime:           formatUptime(s.NowFunc().Sub(s.StartedAt)),
		WGIP:             s.State.WGIP,
		CertExpiry:       certExpiryISO(s.Layout, cfg.Domain),
	}
	writeJSON(w, http.StatusOK, status)
}

// handleConfigUpdate accepts a new protocol/credential set and re-renders the
// node's sing-box config in place. The node then restarts sing-box.
func (s *Server) handleConfigUpdate(w http.ResponseWriter, r *http.Request) {
	var req cluster.ConfigUpdate
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cfg, err := deploy.LoadProtocolConfig(s.Layout)
	if err != nil {
		// Treat first-time config as starting from a blank slate.
		cfg = deploy.Config{}
	}
	if strings.TrimSpace(req.Domain) != "" {
		cfg.Domain = req.Domain
	}
	cfg.Enabled = parseProtocolList(req.EnabledProtocols)
	applyPorts(&cfg.Ports, req.ProtocolPorts)
	applyCredentials(&cfg.Creds, req.Credentials)
	if req.RealityServerName != "" {
		cfg.RealityServerName = req.RealityServerName
	}
	if req.RealityHandshakePort > 0 {
		cfg.RealityHandshakePort = req.RealityHandshakePort
	}
	certPath, keyPath := tlsPaths(s.Layout, cfg.Domain)
	cfgBytes, err := config.Build(serverOptions(cfg, certPath, keyPath))
	if err != nil {
		http.Error(w, "build config: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := os.MkdirAll(s.Layout.FragmentsDir, 0o755); err != nil {
		http.Error(w, "create fragments dir: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(s.Layout.ConfigJSON, cfgBytes, 0o644); err != nil {
		http.Error(w, "write config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := deploy.WriteInstallState(s.Layout.StateDir, cfg); err != nil {
		http.Error(w, "persist state: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Runner.Run(system.Systemctl("restart", system.SingBoxService)); err != nil {
		http.Error(w, "restart sing-box: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleConfigReload restarts the sing-box service without rewriting config.
func (s *Server) handleConfigReload(w http.ResponseWriter, r *http.Request) {
	if err := s.Runner.Run(system.Systemctl("restart", system.SingBoxService)); err != nil {
		http.Error(w, "restart sing-box: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleMonitorConfig accepts a new quota/sampling configuration, re-renders
// the systemd unit from scratch so the new values land in ExecStart, then
// daemon-reloads/enables/restarts the monitor service. Without the re-render,
// limits/reset/interval/alias would stay frozen at install time and any
// subsequent UpdateMonitor would only restart the same stale ExecStart. When
// req.Disabled is true the monitor unit and binary are torn down instead.
func (s *Server) handleMonitorConfig(w http.ResponseWriter, r *http.Request) {
	var req cluster.MonitorUpdate
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Disabled {
		if err := teardownNodeMonitor(s.Runner); err != nil {
			http.Error(w, "teardown monitor: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if err := ensureMonitorBinary(r.Context(), s.Version); err != nil {
		http.Error(w, "fetch singbox-monitor: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeMonitorState(s.Layout, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := writeNodeMonitorUnit(s.Layout, s.State.WGIP, req); err != nil {
		http.Error(w, "render monitor unit: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Runner.Run(system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		http.Error(w, "daemon-reload: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Runner.Run(system.Command{Name: "systemctl", Args: []string{"enable", system.MonitorService}}); err != nil {
		http.Error(w, "enable monitor: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Runner.Run(system.Systemctl("restart", system.MonitorService)); err != nil {
		http.Error(w, "restart monitor: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// teardownNodeMonitor stops and disables the monitor service, removes its
// systemd unit, and deletes the monitor binary. Missing files are ignored so
// the call is safe to invoke when monitor was never installed.
func teardownNodeMonitor(runner system.Runner) error {
	_ = runner.Run(system.Command{Name: "systemctl", Args: []string{"disable", "--now", system.MonitorService}})
	if err := os.Remove(filepath.Join("/etc/systemd/system", system.MonitorService)); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := runner.Run(system.Command{Name: "systemctl", Args: []string{"daemon-reload"}}); err != nil {
		return err
	}
	if err := os.Remove("/usr/bin/singbox-monitor"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// handleUpgrade asks the node to download replacement binaries from the
// configured GitHub release and restart services. Heavy lifting is delegated
// to a hookable upgrader (see Server.Upgrader) so tests can substitute a
// no-op.
func (s *Server) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	var req cluster.UpgradeRequest
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if s.Upgrader == nil {
		s.Upgrader = defaultUpgrader
	}
	if err := s.Upgrader(r.Context(), s, req); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleCertDeploy writes a renewed certificate and key from the master and
// restarts sing-box (and Nginx if installed) so the new material is picked up.
func (s *Server) handleCertDeploy(w http.ResponseWriter, r *http.Request) {
	var req cluster.CertDeploy
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Cert) == "" || strings.TrimSpace(req.Key) == "" {
		http.Error(w, "cert and key are required", http.StatusBadRequest)
		return
	}
	cfg, err := deploy.LoadProtocolConfig(s.Layout)
	if err != nil {
		http.Error(w, "no managed install on this node", http.StatusBadRequest)
		return
	}
	certPath, keyPath := tlsPaths(s.Layout, cfg.Domain)
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(certPath, []byte(req.Cert), 0o644); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := os.WriteFile(keyPath, []byte(req.Key), 0o600); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Runner.Run(system.Systemctl("restart", system.SingBoxService)); err != nil {
		http.Error(w, "restart sing-box: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Nginx is only present when the node has TLS-needing protocols.
	if isUnitActive(s.Runner, "nginx.service") {
		_ = s.Runner.Run(system.Systemctl("reload", "nginx.service"))
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleSiteDeploy installs Nginx (if not present), unpacks the requested
// masquerade site template, writes the node Nginx config, and reloads Nginx.
func (s *Server) handleSiteDeploy(w http.ResponseWriter, r *http.Request) {
	var req cluster.SiteDeploy
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Domain) == "" {
		http.Error(w, "domain is required", http.StatusBadRequest)
		return
	}
	host, err := system.DetectHost()
	if err != nil {
		http.Error(w, "detect host: "+err.Error(), http.StatusInternalServerError)
		return
	}
	// Install Nginx (idempotent: nginx package install on an already-installed
	// system exits 0 with the apt/dnf scripts in deploy/nginx.go).
	for _, cmd := range deploy.NginxInstallCommands(host.OS) {
		if err := s.Runner.Run(cmd); err != nil {
			http.Error(w, "install nginx: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	if err := deploy.DeploySiteTemplate(s.Layout, req.SiteTemplate); err != nil {
		http.Error(w, "deploy site: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := deploy.WriteNodeNginxConfig(s.Layout, req.Domain, "/etc/nginx/conf.d/singbox-deploy.conf"); err != nil {
		http.Error(w, "write nginx config: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.Runner.Run(system.Command{Name: "systemctl", Args: []string{"enable", "--now", "nginx"}}); err != nil {
		// enable --now may have already enabled it; try restart instead.
		if err := s.Runner.Run(system.Systemctl("restart", "nginx")); err != nil {
			http.Error(w, "start nginx: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleTeardown runs the full uninstall sequence and exits. The HTTP response
// is written before the agent process terminates so the master sees success.
func (s *Server) handleTeardown(w http.ResponseWriter, r *http.Request) {
	if s.Teardown == nil {
		s.Teardown = defaultTeardown
	}
	go func() {
		// Give the response a head start.
		time.Sleep(500 * time.Millisecond)
		_ = s.Teardown(s)
		os.Exit(0)
	}()
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func parseProtocolList(values []string) []config.Protocol {
	allowed := map[config.Protocol]bool{}
	for _, p := range config.AllProtocols {
		allowed[p] = true
	}
	var out []config.Protocol
	seen := map[config.Protocol]bool{}
	for _, v := range values {
		p := config.Protocol(strings.TrimSpace(v))
		if !allowed[p] || seen[p] {
			continue
		}
		seen[p] = true
	}
	for _, p := range config.AllProtocols {
		if seen[p] {
			out = append(out, p)
		}
	}
	return out
}

func applyPorts(ports *config.Ports, m map[string]int) {
	for name, value := range m {
		switch config.Protocol(name) {
		case config.ProtocolRealityVision:
			ports.RealityVision = value
		case config.ProtocolRealityGRPC:
			ports.RealityGRPC = value
		case config.ProtocolHysteria2:
			ports.Hysteria2 = value
		case config.ProtocolTUIC:
			ports.TUIC = value
		case config.ProtocolAnyTLS:
			ports.AnyTLS = value
		}
	}
}

func applyCredentials(creds *deploy.Credentials, m map[string]string) {
	for k, v := range m {
		switch k {
		case "reality_vision_uuid":
			creds.RealityVisionUUID = v
		case "reality_grpc_uuid":
			creds.RealityGRPCUUID = v
		case "hysteria2_password":
			creds.HysteriaPassword = v
		case "tuic_uuid":
			creds.TUICUUID = v
		case "tuic_password":
			creds.TUICPassword = v
		case "anytls_password":
			creds.AnyTLSPassword = v
		case "reality_private_key":
			creds.RealityPrivateKey = v
		case "reality_public_key":
			creds.RealityPublicKey = v
		case "reality_short_id":
			creds.RealityShortID = v
		}
	}
}
