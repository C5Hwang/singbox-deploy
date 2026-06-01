package subscription

import "testing"

func TestTokenFromSalt(t *testing.T) {
	got := TokenFromSalt("abc")
	want := "0bee89b07a248e27c83fc3d5951213c1"
	if got != want {
		t.Fatalf("TokenFromSalt = %s, want %s", got, want)
	}
}

func TestRewriteRemoteNodeName(t *testing.T) {
	got := RewriteRemoteNodeName("JP-01 Tokyo", "US-vps1")
	want := "🇺🇸 US-vps1-01 Tokyo"
	if got != want {
		t.Fatalf("RewriteRemoteNodeName = %q, want %q", got, want)
	}
}

func TestAddNodePrefixFlagLeavesExistingFlag(t *testing.T) {
	got := AddNodePrefixFlag("🇯🇵 JP-01")
	if got != "🇯🇵 JP-01" {
		t.Fatalf("got %q", got)
	}
}
