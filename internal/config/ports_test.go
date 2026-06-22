package config

import (
	"strings"
	"testing"
)

func TestValidateProtocolPortRejects443(t *testing.T) {
	if err := ValidateProtocolPort(443, nil); err == nil {
		t.Fatalf("ValidateProtocolPort(443, nil) returned nil; want error mentioning masquerade")
	} else if !strings.Contains(err.Error(), "masquerade") {
		t.Errorf("expected masquerade reason, got %v", err)
	}
}

func TestValidateProtocolPortAcceptsLegal(t *testing.T) {
	if err := ValidateProtocolPort(20100, map[int]bool{80: true}); err != nil {
		t.Errorf("legal port rejected: %v", err)
	}
}

func TestValidateProtocolPortRejectsUsed(t *testing.T) {
	used := map[int]bool{12345: true}
	if err := ValidateProtocolPort(12345, used); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("expected conflict error, got %v", err)
	}
}

func TestValidateProtocolPortRejectsOutOfRange(t *testing.T) {
	for _, p := range []int{0, -1, 65536, 99999} {
		if err := ValidateProtocolPort(p, nil); err == nil {
			t.Errorf("ValidateProtocolPort(%d) returned nil; want error", p)
		}
	}
}

func TestRandomProtocolPortStaysInRange(t *testing.T) {
	used := map[int]bool{}
	for range 50 {
		got, err := RandomProtocolPort(used)
		if err != nil {
			t.Fatalf("RandomProtocolPort: %v", err)
		}
		if got < ProtocolPortMin || got > ProtocolPortMax {
			t.Errorf("port %d out of [%d,%d]", got, ProtocolPortMin, ProtocolPortMax)
		}
		if got == MasqueradeSitePort {
			t.Errorf("RandomProtocolPort returned masquerade port 443")
		}
	}
}

func TestRandomProtocolPortMarksUsed(t *testing.T) {
	used := map[int]bool{}
	first, err := RandomProtocolPort(used)
	if err != nil {
		t.Fatalf("first allocation: %v", err)
	}
	if !used[first] {
		t.Errorf("allocated port %d not marked used", first)
	}
	second, err := RandomProtocolPort(used)
	if err != nil {
		t.Fatalf("second allocation: %v", err)
	}
	if second == first {
		t.Errorf("RandomProtocolPort returned the same port twice: %d", first)
	}
}

func TestRandomProtocolPortExhausts(t *testing.T) {
	used := map[int]bool{}
	for p := ProtocolPortMin; p <= ProtocolPortMax; p++ {
		used[p] = true
	}
	if _, err := RandomProtocolPort(used); err == nil {
		t.Fatalf("expected exhaustion error when every port is used")
	}
}
