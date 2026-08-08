// Package subgroups is the hub's registry of subscription groups. A group is
// one published subscription: it owns a salt (and therefore a URL token), an
// operator-facing alias, and the set of fleet members whose nodes it
// aggregates. Each group is one numbered directory of small state files under
// state/subscription_groups/, using the same entry-tree machinery as the spoke
// registry.
//
// Groups are the only mechanism that publishes hub subscriptions; there is no
// separate "all nodes" subscription behind them. An installation upgraded from
// a single-salt layout is seeded with one group carrying the previous salt, so
// existing subscription URLs keep working unchanged.
package subgroups

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

const (
	groupsDir = "subscription_groups"

	// HubMemberID names the hub's own nodes in a group's member list. Spoke
	// members are stored as their stable registry IDs, which are hex and can
	// therefore never collide with this literal.
	HubMemberID = "hub"

	// DefaultAlias names the group seeded from a pre-groups installation.
	DefaultAlias = "Default"
)

// Group is one published subscription.
type Group struct {
	// ID is the stable registry identity, independent of the mutable alias.
	ID string

	// Alias names the group in the TUI. It never appears in generated
	// subscription output: node names come from the hub display name and each
	// spoke's subscription alias.
	Alias string

	// Salt derives this group's URL token as md5(salt + newline), the same
	// convention every other subscription token uses.
	Salt string

	// Members lists HubMemberID and/or spoke node IDs. Order is not
	// significant: generated output follows the node registry order and the
	// saved hub position, so one reordering applies to every group.
	Members []string
}

// HasMember reports whether id takes part in this group.
func (g Group) HasMember(id string) bool {
	id = normalize(id)
	if id == "" {
		return false
	}
	for _, member := range g.Members {
		if normalize(member) == id {
			return true
		}
	}
	return false
}

// EffectiveAlias returns the display alias, falling back to a short form of the
// stable ID so a group written without an alias is still selectable.
func (g Group) EffectiveAlias() string {
	if alias := strings.TrimSpace(g.Alias); alias != "" {
		return alias
	}
	id := strings.TrimSpace(g.ID)
	if len(id) > 8 {
		id = id[:8]
	}
	if id == "" {
		return "group"
	}
	return "group-" + id
}

func groupsPath(layout paths.Layout) string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return filepath.Join(layout.StateDir, groupsDir)
}

// Load reads all groups in saved order.
func Load(layout paths.Layout) ([]Group, error) {
	list, err := state.LoadEntryDirs(groupsPath(layout), decodeGroup)
	if err != nil {
		return nil, err
	}
	migrated, err := normalizeGroupIDs(list)
	if err != nil {
		return nil, err
	}
	if migrated {
		// Re-read under the tree's transaction lock so persisting the assigned
		// IDs cannot discard a group written after LoadEntryDirs returned.
		list, err = transact(layout, func(current []Group) ([]Group, error) { return current, nil })
		if err != nil {
			return nil, fmt.Errorf("persist migrated subscription group IDs: %w", err)
		}
	}
	return list, nil
}

// Save persists the group list, one directory per group.
func Save(layout paths.Layout, list []Group) error {
	return state.SaveEntryDirs(groupsPath(layout), list, encodeGroup)
}

// Add appends a group and persists the list.
func Add(layout paths.Layout, g Group) (Group, error) {
	var err error
	g.ID = normalize(g.ID)
	if g.ID == "" {
		g.ID, err = GenerateID()
		if err != nil {
			return Group{}, err
		}
	}
	_, err = transact(layout, func(list []Group) ([]Group, error) {
		if err := validateUnique(list, g, -1); err != nil {
			return nil, err
		}
		return append(list, g), nil
	})
	if err != nil {
		return Group{}, err
	}
	return g, nil
}

// Update replaces a group by stable ID.
func Update(layout paths.Layout, g Group) error {
	g.ID = normalize(g.ID)
	if g.ID == "" {
		return fmt.Errorf("subscription group ID is required")
	}
	_, err := transact(layout, func(list []Group) ([]Group, error) {
		match := indexOf(list, g.ID)
		if match < 0 {
			return nil, fmt.Errorf("subscription group %s not found", g.ID)
		}
		if err := validateUnique(list, g, match); err != nil {
			return nil, err
		}
		list[match] = g
		return list, nil
	})
	return err
}

// Remove deletes a group by stable ID. The last remaining group is retained:
// with no group left the hub would publish no subscription at all, silently
// breaking every subscribed client.
func Remove(layout paths.Layout, id string) error {
	id = normalize(id)
	_, err := transact(layout, func(list []Group) ([]Group, error) {
		match := indexOf(list, id)
		if match < 0 {
			return list, nil
		}
		if len(list) == 1 {
			return nil, fmt.Errorf("subscription group %q is the only one left; add another before removing it",
				list[match].EffectiveAlias())
		}
		return append(list[:match], list[match+1:]...), nil
	})
	return err
}

// DropMember removes memberID from every group. It is called when a spoke
// leaves the node registry so a later spoke cannot inherit its membership
// through a recycled ID, and so the member list never names a dead node.
func DropMember(layout paths.Layout, memberID string) error {
	memberID = normalize(memberID)
	if memberID == "" {
		return nil
	}
	_, err := transact(layout, func(list []Group) ([]Group, error) {
		for i := range list {
			kept := make([]string, 0, len(list[i].Members))
			for _, member := range list[i].Members {
				if normalize(member) != memberID {
					kept = append(kept, member)
				}
			}
			list[i].Members = kept
		}
		return list, nil
	})
	return err
}

// AddMember adds memberID to each named group, ignoring groups that no longer
// exist so a concurrent deletion cannot fail spoke provisioning.
func AddMember(layout paths.Layout, memberID string, groupIDs []string) error {
	memberID = normalize(memberID)
	if memberID == "" || len(groupIDs) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(groupIDs))
	for _, id := range groupIDs {
		if id := normalize(id); id != "" {
			wanted[id] = struct{}{}
		}
	}
	_, err := transact(layout, func(list []Group) ([]Group, error) {
		for i := range list {
			if _, ok := wanted[list[i].ID]; !ok || list[i].HasMember(memberID) {
				continue
			}
			list[i].Members = append(list[i].Members, memberID)
		}
		return list, nil
	})
	return err
}

// EnsureSeeded creates the initial group for an installation that has none,
// carrying over the salt already published so existing subscription URLs keep
// resolving. It reports the resulting list and whether a group was created.
func EnsureSeeded(layout paths.Layout, salt string, members []string) ([]Group, bool, error) {
	salt = strings.TrimSpace(salt)
	if salt == "" {
		return nil, false, fmt.Errorf("subscription salt is required to seed the first group")
	}
	id, err := GenerateID()
	if err != nil {
		return nil, false, err
	}
	seeded := false
	list, err := transact(layout, func(current []Group) ([]Group, error) {
		if len(current) > 0 {
			return current, nil
		}
		seeded = true
		return []Group{{ID: id, Alias: DefaultAlias, Salt: salt, Members: members}}, nil
	})
	if err != nil {
		return nil, false, err
	}
	return list, seeded, nil
}

func transact(layout paths.Layout, mutate func([]Group) ([]Group, error)) ([]Group, error) {
	return state.TransactEntryDirs(groupsPath(layout), decodeGroup, encodeGroup, func(list []Group) ([]Group, error) {
		if _, err := normalizeGroupIDs(list); err != nil {
			return nil, err
		}
		return mutate(list)
	})
}

func indexOf(list []Group, id string) int {
	for i := range list {
		if list[i].ID == id {
			return i
		}
	}
	return -1
}

func normalizeGroupIDs(list []Group) (bool, error) {
	migrated := false
	used := make(map[string]struct{}, len(list))
	for i := range list {
		if normalized := normalize(list[i].ID); normalized != list[i].ID {
			list[i].ID = normalized
			migrated = true
		}
		if list[i].ID == "" {
			id, err := GenerateID()
			if err != nil {
				return false, err
			}
			list[i].ID = id
			migrated = true
		}
		if _, duplicate := used[list[i].ID]; duplicate {
			return false, fmt.Errorf("duplicate subscription group ID %q in registry", list[i].ID)
		}
		used[list[i].ID] = struct{}{}
	}
	return migrated, nil
}

// GenerateID returns a cryptographically random 128-bit registry identity.
func GenerateID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate subscription group ID: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

// ValidateNew rejects a group that would collide with an existing one.
func ValidateNew(list []Group, g Group) error { return validateUnique(list, g, -1) }

func validateUnique(list []Group, candidate Group, skip int) error {
	alias := aliasKey(candidate)
	salt := saltKey(candidate)
	if alias == "" {
		return fmt.Errorf("subscription group alias is required")
	}
	if salt == "" {
		return fmt.Errorf("subscription group %q needs a salt", candidate.EffectiveAlias())
	}
	if len(candidate.Members) == 0 {
		return fmt.Errorf("subscription group %q needs at least one member", candidate.EffectiveAlias())
	}
	for i, existing := range list {
		if i == skip {
			continue
		}
		if candidate.ID != "" && candidate.ID == existing.ID {
			return fmt.Errorf("subscription group ID %q is already registered", candidate.ID)
		}
		if alias == aliasKey(existing) {
			return fmt.Errorf("subscription group alias %q is already used", candidate.EffectiveAlias())
		}
		// Two groups sharing a salt share a URL token, so whichever is written
		// last would silently overwrite the other's published files.
		if salt == saltKey(existing) {
			return fmt.Errorf("subscription group %q uses the same salt as %q; each group needs its own salt",
				candidate.EffectiveAlias(), existing.EffectiveAlias())
		}
	}
	return nil
}

// AliasConflict reports the group already using alias, ignoring the entry whose
// stable ID is exemptID. Forms use it to reject a duplicate while the operator
// is still typing.
func AliasConflict(list []Group, alias, exemptID string) (Group, bool) {
	return conflict(list, aliasKey(Group{Alias: alias}), exemptID, aliasKey)
}

// SaltConflict reports the group already using salt, ignoring exemptID.
func SaltConflict(list []Group, salt, exemptID string) (Group, bool) {
	return conflict(list, saltKey(Group{Salt: salt}), exemptID, saltKey)
}

func conflict(list []Group, key, exemptID string, keyOf func(Group) string) (Group, bool) {
	if key == "" {
		return Group{}, false
	}
	exemptID = normalize(exemptID)
	for _, existing := range list {
		if exemptID != "" && existing.ID == exemptID {
			continue
		}
		if keyOf(existing) == key {
			return existing, true
		}
	}
	return Group{}, false
}

func aliasKey(g Group) string { return strings.ToLower(strings.TrimSpace(g.Alias)) }
func saltKey(g Group) string  { return strings.TrimSpace(g.Salt) }

func normalize(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

func decodeGroup(root string) Group {
	get := func(name, fallback string) string { return state.ReadEntryValue(root, name, fallback) }
	var members []string
	for _, member := range strings.Split(get("members", ""), ",") {
		if member = normalize(member); member != "" {
			members = append(members, member)
		}
	}
	return Group{
		ID:      get("id", ""),
		Alias:   get("alias", ""),
		Salt:    get("salt", ""),
		Members: members,
	}
}

func encodeGroup(g Group) map[string]string {
	return map[string]string{
		"id":      normalize(g.ID),
		"alias":   strings.TrimSpace(g.Alias),
		"salt":    strings.TrimSpace(g.Salt),
		"members": strings.Join(g.Members, ","),
	}
}
