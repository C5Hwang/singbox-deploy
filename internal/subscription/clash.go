package subscription

import "regexp"

// clashNameRE matches a Clash proxy entry's quoted name field.
var clashNameRE = regexp.MustCompile(`name:\s*"([^"]+)"`)

// RenameClashFragment rewrites every `name: "..."` in a Clash proxies fragment,
// replacing the node-name prefix with alias while preserving the suffix.
func RenameClashFragment(fragment, alias string) string {
	return clashNameRE.ReplaceAllStringFunc(fragment, func(match string) string {
		parts := clashNameRE.FindStringSubmatch(match)
		if len(parts) != 2 {
			return match
		}
		return `name: "` + RewriteRemoteNodeName(parts[1], alias) + `"`
	})
}
