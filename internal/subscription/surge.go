package subscription

import "strings"

// surgeNameRE is not needed — Surge proxy lines use a simple "name = ..." format.

// RenameSurgeFragment rewrites every proxy name in a Surge proxy list fragment,
// replacing the node-name prefix with alias while preserving the suffix.
func RenameSurgeFragment(fragment, alias string) string {
	var lines []string
	for _, line := range strings.Split(fragment, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		idx := strings.Index(trimmed, " = ")
		if idx < 0 {
			lines = append(lines, trimmed)
			continue
		}
		oldName := trimmed[:idx]
		rest := trimmed[idx:]
		newName := RewriteRemoteNodeName(oldName, alias)
		lines = append(lines, newName+rest)
	}
	return strings.Join(lines, "\n")
}
