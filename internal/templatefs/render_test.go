package templatefs

import (
	"strings"
	"testing"
)

// nginxTemplateData is a complete, hub-shaped set of template values. Tests
// override only the keys they exercise, so adding a template key does not mean
// editing every case.
func nginxTemplateData(overrides map[string]any) map[string]any {
	data := map[string]any{
		"SubscribePort":          2096,
		"MonitorPublicPort":      2097,
		"Domain":                 "example.com",
		"CertificatePath":        "/etc/singbox-deploy/tls/example.com.crt",
		"KeyPath":                "/etc/singbox-deploy/tls/example.com.key",
		"MonitorDomain":          "example.com",
		"MonitorCertificatePath": "/etc/singbox-deploy/tls/example.com.crt",
		"MonitorKeyPath":         "/etc/singbox-deploy/tls/example.com.key",
		"MonitorSharesSiteBlock": false,
		"WebRoot":                "/etc/singbox-deploy/www",
		"SubscribeDir":           "/etc/singbox-deploy/subscribe",
		"EnableMonitor":          true,
		"EnableMonitorFrontend":  true,
		"PublicMonitor":          true,
		"MonitorPort":            19090,

		"SubscriptionDomain":          "example.com",
		"SubscriptionCertificatePath": "/etc/singbox-deploy/tls/example.com.crt",
		"SubscriptionKeyPath":         "/etc/singbox-deploy/tls/example.com.key",
		"SubscriptionInSiteBlock":     false,
		"SubscriptionInMonitorBlock":  false,
		"SubscriptionOwnBlock":        true,
	}
	for key, value := range overrides {
		data[key] = value
	}
	return data
}

func TestRenderNginxTemplate(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(nil))
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
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(map[string]any{
		"EnableMonitor":         false,
		"EnableMonitorFrontend": false,
	}))
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

func TestRenderNginxTemplateWithoutFrontend(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(map[string]any{
		"EnableMonitorFrontend": false,
	}))
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if !strings.Contains(out, "listen 2097 ssl;") {
		t.Fatalf("rendered output missing monitor server block:\n%s", out)
	}
	if !strings.Contains(out, "/monitor/api/") {
		t.Fatalf("rendered output missing API proxy:\n%s", out)
	}
	if strings.Contains(out, "return 302 /monitor/") {
		t.Fatalf("rendered output should not redirect to frontend when disabled:\n%s", out)
	}
	if strings.Contains(out, "location /monitor/ {") {
		t.Fatalf("rendered output should not include frontend proxy when disabled:\n%s", out)
	}
}

func TestRenderNginxTemplateSubscribeOn443(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(map[string]any{
		"SubscribePort":           443,
		"SubscriptionInSiteBlock": true,
		"SubscriptionOwnBlock":    false,
	}))
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
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(map[string]any{
		"MonitorPublicPort":      443,
		"MonitorSharesSiteBlock": true,
	}))
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

// A monitor published under its own name gets its own server block, its own
// certificate, and a catch-all that drops every other name on that port.
func TestRenderNginxTemplateSeparateMonitorDomain(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(map[string]any{
		"MonitorDomain":          "monitor.example.com",
		"MonitorCertificatePath": "/etc/singbox-deploy/tls/monitor.example.com.crt",
		"MonitorKeyPath":         "/etc/singbox-deploy/tls/monitor.example.com.key",

		"SubscriptionDomain":          "monitor.example.com",
		"SubscriptionCertificatePath": "/etc/singbox-deploy/tls/monitor.example.com.crt",
		"SubscriptionKeyPath":         "/etc/singbox-deploy/tls/monitor.example.com.key",
	}))
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	for _, want := range []string{
		"server_name monitor.example.com;",
		"ssl_certificate /etc/singbox-deploy/tls/monitor.example.com.crt;",
		"ssl_certificate_key /etc/singbox-deploy/tls/monitor.example.com.key;",
		"listen 2097 ssl default_server;",
		"ssl_reject_handshake on;",
		"return 444;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, out)
		}
	}
	// The masquerade name must not reach the monitor port.
	monitorBlock := out[strings.Index(out, "listen 2097 ssl;"):]
	if strings.Contains(monitorBlock, "server_name example.com;") {
		t.Fatalf("monitor block should not answer to the masquerade domain:\n%s", monitorBlock)
	}
}

// On 443 the camouflage default server already absorbs every unmatched name, so
// the monitor only adds its own SNI-selected block and no reject block.
func TestRenderNginxTemplateSeparateMonitorDomainOn443(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(map[string]any{
		"MonitorPublicPort":      443,
		"MonitorDomain":          "monitor.example.com",
		"MonitorCertificatePath": "/etc/singbox-deploy/tls/monitor.example.com.crt",
		"MonitorKeyPath":         "/etc/singbox-deploy/tls/monitor.example.com.key",

		"SubscriptionDomain":          "monitor.example.com",
		"SubscriptionCertificatePath": "/etc/singbox-deploy/tls/monitor.example.com.crt",
		"SubscriptionKeyPath":         "/etc/singbox-deploy/tls/monitor.example.com.key",
	}))
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if !strings.Contains(out, "listen 443 ssl default_server;") {
		t.Fatalf("rendered output missing camouflage default block:\n%s", out)
	}
	if !strings.Contains(out, "listen 443 ssl;\n    http2 on;\n    server_name monitor.example.com;") {
		t.Fatalf("rendered output missing the monitor block on 443:\n%s", out)
	}
	if strings.Contains(out, "ssl_reject_handshake on;") {
		t.Fatalf("443 already has a default server; no reject block should be emitted:\n%s", out)
	}
}

// Answering on the monitor's name and the monitor's port, the subscription has
// no block of its own: /s/ joins the monitor's, which already carries that name
// and its certificate.
func TestRenderNginxTemplateSubscriptionInsideTheMonitorBlock(t *testing.T) {
	out, err := Render("nginx/singbox-deploy.conf.tmpl", nginxTemplateData(map[string]any{
		"SubscribePort":          2097,
		"MonitorDomain":          "monitor.example.com",
		"MonitorCertificatePath": "/etc/singbox-deploy/tls/monitor.example.com.crt",
		"MonitorKeyPath":         "/etc/singbox-deploy/tls/monitor.example.com.key",

		"SubscriptionDomain":          "monitor.example.com",
		"SubscriptionCertificatePath": "/etc/singbox-deploy/tls/monitor.example.com.crt",
		"SubscriptionKeyPath":         "/etc/singbox-deploy/tls/monitor.example.com.key",
		"SubscriptionInMonitorBlock":  true,
		"SubscriptionOwnBlock":        false,
	}))
	if err != nil {
		t.Fatalf("Render error: %v", err)
	}
	if strings.Count(out, "listen 2097 ssl;") != 1 {
		t.Fatalf("the subscription must not add a second block on the monitor's port:\n%s", out)
	}
	monitorBlock := out[strings.Index(out, "listen 2097 ssl;"):]
	for _, want := range []string{"server_name monitor.example.com;", "location /s/", "proxy_pass http://127.0.0.1:19090/api/;"} {
		if !strings.Contains(monitorBlock, want) {
			t.Fatalf("monitor block missing %q:\n%s", want, monitorBlock)
		}
	}
}

func TestRenderMissingKeyFails(t *testing.T) {
	_, err := Render("nginx/singbox-deploy.conf.tmpl", map[string]any{"Domain": "example.com"})
	if err == nil {
		t.Fatalf("expected error for missing template key")
	}
}
