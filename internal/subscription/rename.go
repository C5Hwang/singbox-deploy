package subscription

import "strings"

// prefixFlags maps a recognized two-letter node-name prefix to its flag emoji.
// This mirrors the reference install.sh mapping verbatim, including its
// TW->Samoa-flag quirk, so aggregated node names match across versions. It is
// the single source of country flags; deploy's country grouping reads it via
// FlagForCode.
var prefixFlags = map[string]string{
	"US": "🇺🇸", "CA": "🇨🇦", "SG": "🇸🇬", "JP": "🇯🇵", "HK": "🇭🇰", "TW": "🇼🇸",
	"KR": "🇰🇷", "UK": "🇬🇧", "DE": "🇩🇪", "FR": "🇫🇷", "NL": "🇳🇱", "AU": "🇦🇺",
}

// FlagForCode returns the flag emoji for a two-letter country code, or "" if the
// code is not recognized.
func FlagForCode(code string) string {
	return prefixFlags[strings.ToUpper(code)]
}

// AddNodePrefixFlag prepends the flag emoji for a node name's prefix. If the
// name already starts with a known flag, it is returned unchanged.
func AddNodePrefixFlag(name string) string {
	for _, flag := range prefixFlags {
		if strings.HasPrefix(name, flag+" ") {
			return name
		}
	}
	prefix := nodePrefix(name)
	if flag, ok := prefixFlags[strings.ToUpper(prefix)]; ok {
		return flag + " " + name
	}
	return name
}

// RewriteRemoteNodeName replaces a remote node name's prefix with the local
// alias while preserving the numbering/suffix, then re-applies the flag.
func RewriteRemoteNodeName(currentName, alias string) string {
	current := strings.TrimSpace(stripFlag(currentName))
	alias = strings.TrimSpace(stripFlag(alias))
	if current == alias || hasAliasPrefix(current, alias) {
		return AddNodePrefixFlag(current)
	}
	// Spoke-generated names have stable protocol suffixes. Find that boundary
	// instead of treating the first word as the old alias, so multi-word aliases
	// can be replaced without producing names such as "UK Sub Sub-Hysteria2".
	for _, suffix := range []string{
		"-VLESS-Reality-Vision",
		"-VLESS-Reality-gRPC",
		"-Hysteria2",
		"-TUIC",
		"-AnyTLS",
	} {
		if strings.HasSuffix(current, suffix) {
			return AddNodePrefixFlag(alias + suffix)
		}
	}
	prefix := nodePrefix(current)
	suffix := ""
	if prefix != "" && len(current) > len(prefix) {
		suffix = current[len(prefix):]
	}
	if prefix == "" || current == prefix {
		return AddNodePrefixFlag(alias)
	}
	return AddNodePrefixFlag(alias + suffix)
}

func hasAliasPrefix(name, alias string) bool {
	if alias == "" || !strings.HasPrefix(name, alias) || len(name) == len(alias) {
		return false
	}
	switch name[len(alias)] {
	case '-', '_', ' ':
		return true
	default:
		return false
	}
}

// stripFlag removes a leading known flag emoji and its separating space.
func stripFlag(name string) string {
	for _, flag := range prefixFlags {
		if strings.HasPrefix(name, flag+" ") {
			return strings.TrimPrefix(name, flag+" ")
		}
	}
	return name
}

// nodePrefix returns the substring before the first '-', '_', or ' '.
func nodePrefix(name string) string {
	for i, r := range name {
		if r == '-' || r == '_' || r == ' ' {
			return name[:i]
		}
	}
	return name
}
