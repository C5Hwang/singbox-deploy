package monitor

import (
	"fmt"
	"net/netip"
	"strings"
)

// relayIPKeyPrefix marks an accounting key as relay-observed: traffic this node
// forwarded to a landing node rather than terminated itself. The prefix is part
// of the stored key and of the wire form of the ip-detail parameter, so one
// address can hold several independent histories — as a direct client and
// behind the relay, once per landing node — without any change to the storage
// schema, and rows written before the prefix existed keep reading as direct
// traffic.
const relayIPKeyPrefix = "relay:"

// relayIPKeySeparator ends the landing node's registry ID inside a relay key.
// Registry IDs are hex and addresses are dotted quads or colon-separated
// hextets, so a character neither of them can contain splits the two halves
// unambiguously. A relay key written before landings were told apart carries no
// separator at all and still decodes — as relay traffic to an unknown landing,
// which is exactly what it recorded.
const relayIPKeySeparator = "|"

// EncodeIPKey renders one accounted address as its storage and wire key.
// landing is the registry ID of the node the traffic was forwarded to, and is
// empty when this node terminated the traffic itself or no longer has a
// mapping for the port it arrived on.
func EncodeIPKey(address, landing string, relayed bool) string {
	if !relayed {
		return address
	}
	if landing == "" {
		return relayIPKeyPrefix + address
	}
	return relayIPKeyPrefix + landing + relayIPKeySeparator + address
}

// DecodeIPKey splits a storage or wire key back into the address, the landing
// node it was forwarded to, and whether it counts relay-observed traffic.
func DecodeIPKey(key string) (address, landing string, relayed bool) {
	rest, ok := strings.CutPrefix(key, relayIPKeyPrefix)
	if !ok {
		return key, "", false
	}
	if id, addr, split := strings.Cut(rest, relayIPKeySeparator); split {
		return addr, id, true
	}
	return rest, "", true
}

// ParseIPKey validates a caller-supplied accounting key — a literal IP address,
// optionally behind the relay marker and a landing node's registry ID — and
// re-serializes every part, so what reaches a query or a proxied request is
// always a value this process produced.
func ParseIPKey(raw string) (string, error) {
	address, landing, relayed := DecodeIPKey(strings.TrimSpace(raw))
	parsed, err := netip.ParseAddr(address)
	if err != nil {
		return "", err
	}
	return EncodeIPKey(parsed.String(), sanitizeLandingID(landing), relayed), nil
}

// IPKeyAddressSQL renders the SQLite expression that reduces the accounting key
// in column back to the bare address it belongs to — the same split DecodeIPKey
// performs, written a second time for the queries that have to weigh a client
// by everything recorded under it rather than by each strand on its own.
//
// The literals come from the constants above rather than being spelled again,
// so the key format keeps one definition even though two languages read it.
func IPKeyAddressSQL(column string) string {
	// Only a relayed key carries anything in front of the address, and an
	// address can hold neither the marker nor the separator, so one cut at each
	// is the whole decoding.
	rest := fmt.Sprintf("substr(%s, %d)", column, len(relayIPKeyPrefix)+1)
	cut := fmt.Sprintf("instr(%s, %s)", rest, sqlTextLiteral(relayIPKeySeparator))
	return fmt.Sprintf(
		"CASE WHEN substr(%s, 1, %d) = %s"+
			" THEN CASE WHEN %s > 0 THEN substr(%s, %s + 1) ELSE %s END"+
			" ELSE %s END",
		column, len(relayIPKeyPrefix), sqlTextLiteral(relayIPKeyPrefix),
		cut, rest, cut, rest, column)
}

// sqlTextLiteral quotes one of this package's own constants for embedding in a
// statement. Nothing caller-supplied reaches it — every value it is handed is a
// compile-time constant — and doubling the quote is what keeps that from being
// the thing the safety rests on.
func sqlTextLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// sanitizeLandingID keeps only what a registry ID is made of. A caller-supplied
// key reaches a SQL parameter and a proxied query string, so the half that is
// not an address is reduced to the alphabet the hub generates rather than
// trusted for being short.
func sanitizeLandingID(landing string) string {
	id := strings.ToLower(strings.TrimSpace(landing))
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return ""
		}
	}
	return id
}
