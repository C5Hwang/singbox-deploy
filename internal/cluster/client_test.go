package cluster

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// redirectTransport rewrites every outgoing request's host so AgentClient —
// which builds URLs from node.WGIP + a hardcoded port — can be pointed at an
// httptest.Server without touching production code.
type redirectTransport struct{ target *url.URL }

func (rt *redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// TestAgentClientDeployCertIncludesDomain pins the wire contract introduced
// by the add-node cert fix: the JSON body that lands at /api/cert/deploy
// includes Domain, so the agent can resolve tlsPaths without consulting its
// install state (which doesn't exist yet during first-time provisioning).
func TestAgentClientDeployCertIncludesDomain(t *testing.T) {
	var captured CertDeploy
	var gotAuth string
	mux := http.NewServeMux()
	mux.HandleFunc("/api/cert/deploy", func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	node := Node{WGIP: "10.99.99.1", APIToken: "tok"}
	agent := NewAgentClient(node)
	agent.HTTPClient.Transport = &redirectTransport{target: target}

	if err := agent.DeployCert(context.Background(), CertDeploy{
		Domain: "jp.example.com",
		Cert:   "PEM-CERT",
		Key:    "PEM-KEY",
	}); err != nil {
		t.Fatalf("DeployCert: %v", err)
	}
	if captured.Domain != "jp.example.com" {
		t.Errorf("expected Domain=%q on wire, got %q", "jp.example.com", captured.Domain)
	}
	if captured.Cert != "PEM-CERT" || captured.Key != "PEM-KEY" {
		t.Errorf("payload not preserved: %#v", captured)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("expected Bearer tok auth header, got %q", gotAuth)
	}
}

// TestAgentClientDeployCertOmitsEmptyDomain confirms the omitempty tag works
// so that older callers that don't set Domain produce a payload without the
// field at all — letting the agent's install-state fallback kick in cleanly.
func TestAgentClientDeployCertOmitsEmptyDomain(t *testing.T) {
	var raw map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/api/cert/deploy", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	target, _ := url.Parse(srv.URL)

	node := Node{WGIP: "10.99.99.1", APIToken: "tok"}
	agent := NewAgentClient(node)
	agent.HTTPClient.Transport = &redirectTransport{target: target}

	if err := agent.DeployCert(context.Background(), CertDeploy{
		Cert: "PEM-CERT",
		Key:  "PEM-KEY",
	}); err != nil {
		t.Fatalf("DeployCert: %v", err)
	}
	if _, ok := raw["domain"]; ok {
		t.Errorf("expected omitempty to drop the field, raw payload=%#v", raw)
	}
}
