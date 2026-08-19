package monitor

import "testing"

func TestParseIPKeyAcceptsDirectAndRelayedForms(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "203.0.113.7", want: "203.0.113.7"},
		{raw: " 203.0.113.7 ", want: "203.0.113.7"},
		{raw: "relay:203.0.113.7", want: "relay:203.0.113.7"},
		{raw: " relay:203.0.113.7 ", want: "relay:203.0.113.7"},
		// The address part is canonicalized, not echoed.
		{raw: "relay:2001:DB8::1", want: "relay:2001:db8::1"},
	}
	for _, c := range cases {
		got, err := ParseIPKey(c.raw)
		if err != nil {
			t.Fatalf("ParseIPKey(%q): %v", c.raw, err)
		}
		if got != c.want {
			t.Fatalf("ParseIPKey(%q) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestParseIPKeyRejectsAnythingButAnAddress(t *testing.T) {
	for _, raw := range []string{"", "not-an-address", "relay:", "relay:not-an-address", "relay:relay:203.0.113.7", "../etc/passwd"} {
		if got, err := ParseIPKey(raw); err == nil {
			t.Fatalf("ParseIPKey(%q) = %q, want an error", raw, got)
		}
	}
}

func TestDecodeIPKeySplitsTheRelayMarker(t *testing.T) {
	if address, relayed := DecodeIPKey("relay:203.0.113.7"); address != "203.0.113.7" || !relayed {
		t.Fatalf("DecodeIPKey = %q, %v", address, relayed)
	}
	// A key written before the marker existed decodes as direct traffic, which
	// is what keeps a pre-upgrade database readable.
	if address, relayed := DecodeIPKey("203.0.113.7"); address != "203.0.113.7" || relayed {
		t.Fatalf("DecodeIPKey = %q, %v", address, relayed)
	}
}
