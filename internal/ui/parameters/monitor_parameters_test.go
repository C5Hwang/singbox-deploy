package parameters

import "testing"

func TestValidateMonitorToken(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "blank keeps the default", value: ""},
		{name: "clearing sentinel", value: MonitorTokenNone},
		{name: "ordinary token", value: "s3cret-monitor-token"},
		{name: "base64url token", value: "Gk8dQ2_zVn-4Lm0pXyTb1aWc"},
		{name: "too short", value: "short12", wantErr: true},
		{name: "contains a space", value: "has a space here", wantErr: true},
		{name: "contains non-ASCII", value: "令牌令牌令牌令牌", wantErr: true},
		{name: "too long", value: repeat("a", maxMonitorTokenLength+1), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMonitorToken(tc.value)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateMonitorToken(%q) = nil, want an error", tc.value)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateMonitorToken(%q) = %v", tc.value, err)
			}
		})
	}
}

// The sentinel is the only way to say "publish without a token"; a blank answer
// means the form default applies.
func TestMonitorTokenValue(t *testing.T) {
	if got := MonitorTokenValue("  s3cret-monitor-token  "); got != "s3cret-monitor-token" {
		t.Fatalf("MonitorTokenValue = %q", got)
	}
	if got := MonitorTokenValue(MonitorTokenNone); got != "" {
		t.Fatalf("MonitorTokenValue(%q) = %q, want empty", MonitorTokenNone, got)
	}
}

// The sentinel has to stay outside the range a real token can take, or clearing
// the gate would be ambiguous with setting it.
func TestMonitorTokenNoneIsNotAValidToken(t *testing.T) {
	if len(MonitorTokenNone) >= minMonitorTokenLength {
		t.Fatalf("clearing sentinel %q is long enough to be a real token", MonitorTokenNone)
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for range n {
		out = append(out, s...)
	}
	return string(out)
}
