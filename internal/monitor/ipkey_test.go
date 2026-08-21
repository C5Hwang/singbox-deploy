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
		// A landing node's registry ID rides along, lowercased like the address.
		{raw: "relay:AABB|203.0.113.7", want: "relay:aabb|203.0.113.7"},
		{raw: "relay:aabb|2001:DB8::1", want: "relay:aabb|2001:db8::1"},
		// Anything that is not a registry ID is dropped rather than forwarded to
		// a query, which leaves an honest relay key with no landing named.
		{raw: "relay:../etc|203.0.113.7", want: "relay:203.0.113.7"},
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
	if address, landing, relayed := DecodeIPKey("relay:aabb|203.0.113.7"); address != "203.0.113.7" || landing != "aabb" || !relayed {
		t.Fatalf("DecodeIPKey = %q, %q, %v", address, landing, relayed)
	}
	// A key written before the marker existed decodes as direct traffic, which
	// is what keeps a pre-upgrade database readable.
	if address, landing, relayed := DecodeIPKey("203.0.113.7"); address != "203.0.113.7" || landing != "" || relayed {
		t.Fatalf("DecodeIPKey = %q, %q, %v", address, landing, relayed)
	}
	// A key written before landings were told apart still reads as relayed
	// traffic, to a destination this release simply cannot name.
	if address, landing, relayed := DecodeIPKey("relay:203.0.113.7"); address != "203.0.113.7" || landing != "" || !relayed {
		t.Fatalf("DecodeIPKey = %q, %q, %v", address, landing, relayed)
	}
	// An IPv6 address is full of separators of its own; only the landing marker
	// splits, and only at its first occurrence.
	if address, landing, relayed := DecodeIPKey("relay:aabb|2001:db8::1"); address != "2001:db8::1" || landing != "aabb" || !relayed {
		t.Fatalf("DecodeIPKey = %q, %q, %v", address, landing, relayed)
	}
}
