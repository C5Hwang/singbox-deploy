package parameters

import (
	"strings"
	"testing"
)

func TestValidateSharedParameterValuePortRejects443(t *testing.T) {
	for _, key := range []string{"reality_vision_port", "reality_grpc_port", "hysteria2_port", "tuic_port", "anytls_port"} {
		if err := ValidateSharedParameterValue(key, "443"); err == nil {
			t.Errorf("%s: expected error for 443", key)
		} else if !strings.Contains(err.Error(), "masquerade") {
			t.Errorf("%s: expected masquerade reason, got %v", key, err)
		}
	}
}

func TestValidateSharedParameterValuePortAcceptsLegal(t *testing.T) {
	if err := ValidateSharedParameterValue("hysteria2_port", "9443"); err != nil {
		t.Errorf("9443 should be legal: %v", err)
	}
}

func TestValidateSharedParameterValuePortAcceptsEmpty(t *testing.T) {
	if err := ValidateSharedParameterValue("tuic_port", ""); err != nil {
		t.Errorf("empty input means random, expected nil: %v", err)
	}
}

func TestValidateSharedParameterValuePortRejectsOutOfRange(t *testing.T) {
	if err := ValidateSharedParameterValue("anytls_port", "70000"); err == nil {
		t.Errorf("out-of-range port accepted")
	}
}
