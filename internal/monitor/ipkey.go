package monitor

import (
	"net/netip"
	"strings"
)

// relayIPKeyPrefix marks an accounting key as relay-observed: traffic this node
// forwarded to a landing node rather than terminated itself. The prefix is part
// of the stored key and of the wire form of the ip-detail parameter, so one
// address can hold two independent histories — as a direct client and behind
// the relay — without any change to the storage schema, and rows written before
// the prefix existed keep reading as direct traffic.
const relayIPKeyPrefix = "relay:"

// EncodeIPKey renders one accounted address as its storage and wire key.
func EncodeIPKey(address string, relayed bool) string {
	if relayed {
		return relayIPKeyPrefix + address
	}
	return address
}

// DecodeIPKey splits a storage or wire key back into the address and whether it
// counts relay-observed traffic.
func DecodeIPKey(key string) (address string, relayed bool) {
	if rest, ok := strings.CutPrefix(key, relayIPKeyPrefix); ok {
		return rest, true
	}
	return key, false
}

// ParseIPKey validates a caller-supplied accounting key — a literal IP address,
// optionally behind the relay marker — and re-serializes both parts, so what
// reaches a query or a proxied request is always a value this process produced.
func ParseIPKey(raw string) (string, error) {
	address, relayed := DecodeIPKey(strings.TrimSpace(raw))
	parsed, err := netip.ParseAddr(address)
	if err != nil {
		return "", err
	}
	return EncodeIPKey(parsed.String(), relayed), nil
}
