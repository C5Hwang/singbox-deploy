package templatefs

import (
	"strings"
	"testing"
)

func TestRenderNginxTemplate(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":   443,
		"TrafficPort":     2097,
		"Domain":          "example.com",
		"CertificatePath": "/etc/singbox-deploy/tls/example.com.crt",
		"KeyPath":         "/etc/singbox-deploy/tls/example.com.key",
		"WebRoot":         "/etc/singbox-deploy/www",
		"SubscribeDir":    "/etc/singbox-deploy/subscribe",
		"EnableMonitor":   true,
		"MonitorPort":     19090,
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	for _, want := range []string{"listen 443 ssl http2;", "listen 2097 ssl http2;", "server_name example.com;", "location /s/", "charset utf-8;", "proxy_pass http://127.0.0.1:19090/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, out)
		}
	}
}

func TestRenderNginxTemplateWithoutMonitor(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{
		"SubscribePort":   443,
		"TrafficPort":     2097,
		"Domain":          "example.com",
		"CertificatePath": "/etc/singbox-deploy/tls/example.com.crt",
		"KeyPath":         "/etc/singbox-deploy/tls/example.com.key",
		"WebRoot":         "/etc/singbox-deploy/www",
		"SubscribeDir":    "/etc/singbox-deploy/subscribe",
		"EnableMonitor":   false,
		"MonitorPort":     19090,
	})
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if strings.Contains(out, "/traffic/") || strings.Contains(out, "127.0.0.1:19090") || strings.Contains(out, "2097") {
		t.Fatalf("rendered output should not include monitor proxy:\n%s", out)
	}
}

func TestRenderMissingKeyFails(t *testing.T) {
	_, err := Render("site/default/index.html.tmpl", map[string]any{"Title": "Hi"})
	if err == nil {
		t.Fatalf("expected error for missing Subtitle key")
	}
}
