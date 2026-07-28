package ui

import (
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/nodes"
)

func TestAddSpokeFormValidatesSubscriptionAliasSeparately(t *testing.T) {
	m := newNodeManager()
	m.list = []nodes.Node{{
		ID: "11111111111111111111111111111111", Alias: "Tokyo UI", SubscriptionAlias: "Japan",
		Domain: "a.example.com",
	}}

	// Management and subscription aliases occupy separate namespaces.
	if err := m.validateForm(field{key: "alias"}, "Japan", nil); err != nil {
		t.Fatalf("management alias matching another subscription alias was rejected: %v", err)
	}
	values := map[string]string{"alias": "London UI"}
	if err := m.validateForm(field{key: "subscription_alias"}, " japan ", values); err == nil ||
		!strings.Contains(err.Error(), "subscription alias is already used") {
		t.Fatalf("duplicate subscription alias = %v", err)
	}
	if err := m.validateForm(field{key: "subscription_alias"}, "UK", values); err != nil {
		t.Fatalf("distinct subscription alias rejected: %v", err)
	}
	values["alias"] = "Japan"
	if err := m.validateForm(field{key: "subscription_alias"}, "", values); err == nil {
		t.Fatal("blank subscription alias fallback collision was accepted")
	}
}

func TestAddSpokeFormCollectsIndependentAliases(t *testing.T) {
	m := &nodeManager{form: newParameterForm(nil)}
	m.beginForm()
	found := false
	for _, f := range m.form.fields {
		if f.key == "subscription_alias" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("add-spoke form is missing subscription_alias")
	}

	m.form.values = map[string]string{
		"alias":                    "UK UI",
		"subscription_alias":       "United Kingdom",
		"ssh_host":                 "192.0.2.20",
		"ssh_port":                 "36169",
		"ssh_user":                 "root",
		"ssh_auth":                 "password",
		"ssh_password":             "memory-only",
		"domain":                   "uk.example.com",
		"protocols":                "hysteria2",
		"reality_sni":              "www.cloudflare.com",
		"reality_vision_port":      "18001",
		"reality_grpc_port":        "18002",
		"hysteria2_port":           "18003",
		"tuic_port":                "18004",
		"anytls_port":              "18005",
		"monitor":                  "no",
		"monitor_interval_seconds": "60",
		"traffic_in_limit":         "0",
		"traffic_out_limit":        "0",
		"traffic_total_limit":      "0",
		"reset_day":                "1",
		"reset_hour":               "0",
	}
	m.completeForm()

	if got := m.pendingRegistry; got.Alias != "UK UI" || got.SubscriptionAlias != "United Kingdom" {
		t.Fatalf("management/subscription aliases = %q/%q", got.Alias, got.SubscriptionAlias)
	}
}

func TestSpokeSubscriptionFormUsesSubscriptionAliasNamespace(t *testing.T) {
	sm := &subscriptionManager{
		nodes: []nodes.Node{
			{ID: "11111111111111111111111111111111", Alias: "Tokyo UI", SubscriptionAlias: "Tokyo"},
			{ID: "22222222222222222222222222222222", Alias: "Osaka UI", SubscriptionAlias: "Osaka"},
		},
		editNodeIndex: 1,
	}
	aliasField := field{key: "spoke_alias"}
	if err := sm.validateSpokeField(aliasField, "Osaka", nil); err != nil {
		t.Fatalf("keeping the node's own subscription alias was rejected: %v", err)
	}
	if err := sm.validateSpokeField(aliasField, "tokyo", nil); err == nil ||
		!strings.Contains(err.Error(), "already used by") {
		t.Fatalf("renaming onto another subscription alias = %v", err)
	}
	if err := sm.validateSpokeField(aliasField, "Kyoto", nil); err != nil {
		t.Fatalf("distinct subscription alias rejected: %v", err)
	}
	if err := sm.validateSpokeField(aliasField, "", nil); err != nil {
		t.Fatalf("blank alias should fall back to the management alias: %v", err)
	}
}
