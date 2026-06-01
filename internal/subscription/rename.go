package subscription

import "strings"

// prefixFlags maps a recognized two-letter node-name prefix to its flag emoji.
// This mirrors the reference install.sh mapping verbatim, including its
// TW->Samoa-flag quirk, so aggregated node names match across versions.
var prefixFlags = map[string]string{
	"US": "🇺🇸", "CA": "🇨🇦", "SG": "🇸🇬", "JP": "🇯🇵", "HK": "🇭🇰", "TW": "🇼🇸",
	"KR": "🇰🇷", "UK": "🇬🇧", "DE": "🇩🇪", "FR": "🇫🇷", "NL": "🇳🇱", "AU": "🇦🇺",
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
	current := stripFlag(currentName)
	alias = stripFlag(alias)
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
