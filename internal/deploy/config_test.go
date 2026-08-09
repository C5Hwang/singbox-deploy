package deploy

import (
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/config"
)

func TestValidatePorts(t *testing.T) {
	base := func() Config {
		return Config{
			Enabled:           []config.Protocol{config.ProtocolRealityVision, config.ProtocolHysteria2},
			Ports:             config.Ports{RealityVision: 8443, Hysteria2: 9443},
			SubscribePort:     2096,
			DeployMonitor:     true,
			MonitorPublicPort: 2097,
			MonitorPort:       19090,
		}
	}

	t.Run("valid", func(t *testing.T) {
		if err := base().ValidatePorts(); err != nil {
			t.Fatalf("expected valid config, got %v", err)
		}
	})

	t.Run("protocol on 443 conflicts with nginx", func(t *testing.T) {
		cfg := base()
		cfg.Ports.RealityVision = 443
		if err := cfg.ValidatePorts(); err == nil {
			t.Fatal("protocol port 443 must conflict with the Nginx camouflage site")
		}
	})

	t.Run("protocol on 80 conflicts with nginx", func(t *testing.T) {
		cfg := base()
		cfg.Ports.Hysteria2 = 80
		if err := cfg.ValidatePorts(); err == nil {
			t.Fatal("protocol port 80 must conflict with the Nginx HTTP redirect")
		}
	})

	t.Run("duplicate protocol and subscription port", func(t *testing.T) {
		cfg := base()
		cfg.Ports.RealityVision = 2096
		if err := cfg.ValidatePorts(); err == nil {
			t.Fatal("protocol sharing the subscription port must conflict")
		}
	})

	t.Run("subscription may fold onto 443", func(t *testing.T) {
		cfg := base()
		cfg.SubscribePort = 443
		if err := cfg.ValidatePorts(); err != nil {
			t.Fatalf("subscription on 443 folds into the Nginx block, got %v", err)
		}
	})

	t.Run("monitor public may fold onto 443", func(t *testing.T) {
		cfg := base()
		cfg.MonitorPublicPort = 443
		if err := cfg.ValidatePorts(); err != nil {
			t.Fatalf("monitor public on 443 folds into the Nginx block, got %v", err)
		}
	})

	t.Run("monitor service cannot fold onto 443", func(t *testing.T) {
		cfg := base()
		cfg.MonitorPort = 443
		if err := cfg.ValidatePorts(); err == nil {
			t.Fatal("monitor service port 443 must conflict with Nginx")
		}
	})

	t.Run("spoke ignores unused public web ports", func(t *testing.T) {
		cfg := base()
		cfg.SpokeMode = true
		cfg.Ports.RealityVision = cfg.SubscribePort
		cfg.Ports.Hysteria2 = cfg.MonitorPublicPort
		if err := cfg.ValidatePorts(); err != nil {
			t.Fatalf("spoke protocols may reuse unbound public web ports: %v", err)
		}
	})

	for _, tt := range []struct {
		name string
		port int
	}{
		{name: "monitor service", port: 19090},
		{name: "nginx HTTP", port: 80},
		{name: "nginx HTTPS", port: 443},
	} {
		t.Run("spoke protocol conflicts with "+tt.name, func(t *testing.T) {
			cfg := base()
			cfg.SpokeMode = true
			cfg.Ports.RealityVision = tt.port
			if err := cfg.ValidatePorts(); err == nil {
				t.Fatalf("spoke protocol port %d must conflict with %s", tt.port, tt.name)
			}
		})
	}
}

func TestMonitorHostAndCertificateDomain(t *testing.T) {
	base := Config{Domain: "example.com", DeployMonitor: true}
	cases := []struct {
		name           string
		mutate         func(*Config)
		wantHost       string
		wantCertDomain string
		wantErr        bool
	}{
		{
			name:           "unset falls back to the install domain",
			mutate:         func(*Config) {},
			wantHost:       "example.com",
			wantCertDomain: "",
		},
		{
			name:           "own name needs its own certificate",
			mutate:         func(c *Config) { c.MonitorDomain = "monitor.example.com" },
			wantHost:       "monitor.example.com",
			wantCertDomain: "monitor.example.com",
		},
		{
			name:           "same name shares the install certificate",
			mutate:         func(c *Config) { c.MonitorDomain = "Example.COM." },
			wantHost:       "Example.COM.",
			wantCertDomain: "",
		},
		{
			name:           "a disabled monitor needs no certificate",
			mutate:         func(c *Config) { c.MonitorDomain, c.DeployMonitor = "monitor.example.com", false },
			wantHost:       "monitor.example.com",
			wantCertDomain: "",
		},
		{
			name:           "a spoke publishes no monitor",
			mutate:         func(c *Config) { c.MonitorDomain, c.SpokeMode = "monitor.example.com", true },
			wantHost:       "monitor.example.com",
			wantCertDomain: "",
		},
		{
			name:    "an unusable name is reported, not silently skipped",
			mutate:  func(c *Config) { c.MonitorDomain = "not a domain" },
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			tc.mutate(&cfg)
			certDomain, err := cfg.MonitorCertificateDomain()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %q", certDomain)
				}
				return
			}
			if err != nil {
				t.Fatalf("MonitorCertificateDomain error: %v", err)
			}
			if cfg.MonitorHost() != tc.wantHost {
				t.Fatalf("MonitorHost = %q, want %q", cfg.MonitorHost(), tc.wantHost)
			}
			if certDomain != tc.wantCertDomain {
				t.Fatalf("MonitorCertificateDomain = %q, want %q", certDomain, tc.wantCertDomain)
			}
		})
	}
}
