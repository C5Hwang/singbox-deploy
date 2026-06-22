package deploy

import "testing"

func TestCredentialsApplyOverridesReplacesNonEmptyFields(t *testing.T) {
	c := Credentials{
		RealityVisionUUID: "auto-vision",
		RealityGRPCUUID:   "auto-grpc",
		HysteriaPassword:  "auto-hy2",
		TUICUUID:          "auto-tuic-uuid",
		TUICPassword:      "auto-tuic-pw",
		AnyTLSPassword:    "auto-anytls",
		RealityPrivateKey: "priv",
		RealityPublicKey:  "pub",
		RealityShortID:    "short",
	}
	c.ApplyOverrides(Credentials{
		RealityVisionUUID: "user-vision",
		HysteriaPassword:  "user-hy2",
		TUICPassword:      "  user-tuic-pw  ",
		AnyTLSPassword:    "",
	})
	if c.RealityVisionUUID != "user-vision" {
		t.Errorf("RealityVisionUUID = %q want user-vision", c.RealityVisionUUID)
	}
	if c.RealityGRPCUUID != "auto-grpc" {
		t.Errorf("RealityGRPCUUID overwritten: %q", c.RealityGRPCUUID)
	}
	if c.HysteriaPassword != "user-hy2" {
		t.Errorf("HysteriaPassword = %q want user-hy2", c.HysteriaPassword)
	}
	if c.TUICUUID != "auto-tuic-uuid" {
		t.Errorf("TUICUUID overwritten: %q", c.TUICUUID)
	}
	if c.TUICPassword != "user-tuic-pw" {
		t.Errorf("TUICPassword = %q want trimmed user-tuic-pw", c.TUICPassword)
	}
	if c.AnyTLSPassword != "auto-anytls" {
		t.Errorf("blank override should not clear AnyTLSPassword, got %q", c.AnyTLSPassword)
	}
	if c.RealityPrivateKey != "priv" || c.RealityPublicKey != "pub" || c.RealityShortID != "short" {
		t.Errorf("reality key material must not be touched: %+v", c)
	}
}
