package templatefs

import (
	"strings"
	"testing"
)

func TestRenderNginxTemplate(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":     2096,
		"MonitorPublicPort": 2097,
		"Domain":            "example.com",
		"CertificatePath":   "/etc/singbox-deploy/tls/example.com.crt",
		"KeyPath":           "/etc/singbox-deploy/tls/example.com.key",
		"WebRoot":           "/etc/singbox-deploy/www",
		"SubscribeDir":      "/etc/singbox-deploy/subscribe",
		"EnableMonitor":     true,
		"MonitorPort":       19090,
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	for _, want := range []string{
		"listen 80 default_server;",
		"return 301 https://$host$request_uri;",
		"listen 443 ssl default_server;",
		"try_files $uri",
		"listen 2096 ssl;",
		"listen 2097 ssl;",
		"http2 on;",
		"server_name example.com;",
		"location /s/",
		"charset utf-8;",
		"proxy_pass http://127.0.0.1:19090/",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderNginxTemplateWithoutMonitor(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":     2096,
		"MonitorPublicPort": 2097,
		"Domain":            "example.com",
		"CertificatePath":   "/etc/singbox-deploy/tls/example.com.crt",
		"KeyPath":           "/etc/singbox-deploy/tls/example.com.key",
		"WebRoot":           "/etc/singbox-deploy/www",
		"SubscribeDir":      "/etc/singbox-deploy/subscribe",
		"EnableMonitor":     false,
		"MonitorPort":       19090,
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	for _, absent := range []string{"/monitor/", "127.0.0.1:19090", "2097"} {
		if strings.Contains(out, absent) {
			t.Fatalf("rendered output should not include monitor proxy %q:\n%s", absent, out)
		}
	}
	for _, want := range []string{"listen 80 default_server;", "return 301 https://", "listen 443 ssl default_server;"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered output missing default block %q:\n%s", want, out)
		}
	}
}

func TestRenderNginxTemplateSubscribeOn443(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":     443,
		"MonitorPublicPort": 2097,
		"Domain":            "example.com",
		"CertificatePath":   "/etc/singbox-deploy/tls/example.com.crt",
		"KeyPath":           "/etc/singbox-deploy/tls/example.com.key",
		"WebRoot":           "/etc/singbox-deploy/www",
		"SubscribeDir":      "/etc/singbox-deploy/subscribe",
		"EnableMonitor":     true,
		"MonitorPort":       19090,
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if !strings.Contains(out, "location /s/") {
		t.Fatalf("rendered output missing subscription location:\n%s", out)
	}
	// /s/ should be inside the 443 default block, no separate subscribe server block
	if strings.Contains(out, "listen 443 ssl;") {
		t.Fatalf("rendered output should not have a separate subscribe server block on 443:\n%s", out)
	}
	// monitor should still be on a separate port
	if !strings.Contains(out, "listen 2097 ssl;") {
		t.Fatalf("rendered output missing monitor server block:\n%s", out)
	}
}

func TestRenderNginxTemplateMonitorOn443(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":     2096,
		"MonitorPublicPort": 443,
		"Domain":            "example.com",
		"CertificatePath":   "/etc/singbox-deploy/tls/example.com.crt",
		"KeyPath":           "/etc/singbox-deploy/tls/example.com.key",
		"WebRoot":           "/etc/singbox-deploy/www",
		"SubscribeDir":      "/etc/singbox-deploy/subscribe",
		"EnableMonitor":     true,
		"MonitorPort":       19090,
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	for _, want := range []string{
		"listen 443 ssl default_server;",
		"proxy_pass http://127.0.0.1:19090/;",
		"proxy_pass http://127.0.0.1:19090/api/;",
		"try_files $uri",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, out)
		}
	}
	// Monitor locations should be inside the 443 default block, not a separate server block.
	count := strings.Count(out, "listen 443")
	if count != 1 {
		t.Fatalf("expected exactly 1 listen-443 directive (default block), got %d:\n%s", count, out)
	}
}

func TestRenderMissingKeyFails(t *testing.T) {
	_, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{"Domain": "example.com"})
	if err == nil {
		t.Fatalf("expected error for missing template key")
	}
}
