package wgnet

import (
	"strings"
	"testing"
)

const procRouteHeader = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n"

func TestCheckProcRouteTableConflict(t *testing.T) {
	// 10.90.0.0/24 is 00005A0A with a 00FFFFFF mask in /proc's
	// little-endian word representation.
	routes := procRouteHeader +
		"eth0\t00000000\t010200C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		"eth1\t00005A0A\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n"
	err := checkProcRouteTable(DefaultSubnet, InterfaceName, []byte(routes))
	if err == nil || !strings.Contains(err.Error(), "10.90.0.0/24") || !strings.Contains(err.Error(), "eth1") {
		t.Fatalf("expected clear route conflict, got %v", err)
	}
}

func TestCheckProcRouteTableIgnoresDefaultAndOverlayInterface(t *testing.T) {
	routes := procRouteHeader +
		"eth0\t00000000\t010200C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n" +
		InterfaceName + "\t00005A0A\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
		"docker0\t000011AC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0\n"
	if err := checkProcRouteTable(DefaultSubnet, InterfaceName, []byte(routes)); err != nil {
		t.Fatalf("non-conflicting routes rejected: %v", err)
	}
}
