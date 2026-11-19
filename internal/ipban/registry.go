package ipban

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/state"
)

// bansDir is the hub's registry of bans, one numbered entry directory per
// banned address, using the same entry-tree machinery as the other registries.
const bansDir = "ip_bans"

// Ban forbids one address (or prefix) from using the listed nodes. A node is
// named by its relay-registry ID: "hub" for the hub, or a spoke's stable
// registry ID.
type Ban struct {
	Address string
	Nodes   []string
}

// Covers reports whether the ban names nodeID.
func (b Ban) Covers(nodeID string) bool {
	nodeID = normalizeID(nodeID)
	for _, id := range b.Nodes {
		if id == nodeID {
			return true
		}
	}
	return false
}

func bansPath(layout paths.Layout) string {
	if layout.Root == "" {
		layout = paths.DefaultLayout()
	}
	return filepath.Join(layout.StateDir, bansDir)
}

// LoadBans reads every ban in saved order.
func LoadBans(layout paths.Layout) ([]Ban, error) {
	return state.LoadEntryDirs(bansPath(layout), decodeBan)
}

// SetBan stores ban, replacing the entry for the same address when there is
// one. A ban must name at least one node; lifting it entirely is RemoveBan.
func SetBan(layout paths.Layout, ban Ban) error {
	normalized, err := ban.normalized()
	if err != nil {
		return err
	}
	if len(normalized.Nodes) == 0 {
		return fmt.Errorf("a ban needs at least one node")
	}
	_, err = transact(layout, func(list []Ban) ([]Ban, error) {
		if match := indexOf(list, normalized.Address); match >= 0 {
			list[match] = normalized
			return list, nil
		}
		return append(list, normalized), nil
	})
	return err
}

// AddBan stores a new ban, refusing an address that already has one so an add
// never silently rewrites an existing entry's node list.
func AddBan(layout paths.Layout, ban Ban) error {
	normalized, err := ban.normalized()
	if err != nil {
		return err
	}
	if len(normalized.Nodes) == 0 {
		return fmt.Errorf("a ban needs at least one node")
	}
	_, err = transact(layout, func(list []Ban) ([]Ban, error) {
		if indexOf(list, normalized.Address) >= 0 {
			return nil, fmt.Errorf("%s is already banned; edit that entry instead", normalized.Address)
		}
		return append(list, normalized), nil
	})
	return err
}

// RemoveBan lifts the ban on address. It is idempotent.
func RemoveBan(layout paths.Layout, address string) error {
	canonical, err := CanonicalAddress(address)
	if err != nil {
		return err
	}
	_, err = transact(layout, func(list []Ban) ([]Ban, error) {
		match := indexOf(list, canonical)
		if match < 0 {
			return list, nil
		}
		return append(list[:match], list[match+1:]...), nil
	})
	return err
}

// DropNode removes a node that left the fleet from every ban, so a later node
// cannot inherit bans through a recycled ID. A ban left naming no node is lifted.
func DropNode(layout paths.Layout, nodeID string) error {
	nodeID = normalizeID(nodeID)
	if nodeID == "" {
		return nil
	}
	_, err := transact(layout, func(list []Ban) ([]Ban, error) {
		kept := make([]Ban, 0, len(list))
		for _, ban := range list {
			remaining := make([]string, 0, len(ban.Nodes))
			for _, id := range ban.Nodes {
				if id != nodeID {
					remaining = append(remaining, id)
				}
			}
			if len(remaining) == 0 {
				continue
			}
			ban.Nodes = remaining
			kept = append(kept, ban)
		}
		return kept, nil
	})
	return err
}

// AddressesFor returns every address banned from nodeID, in registry order.
func AddressesFor(list []Ban, nodeID string) []string {
	var out []string
	for _, ban := range list {
		if ban.Covers(nodeID) {
			out = append(out, ban.Address)
		}
	}
	return out
}

func (b Ban) normalized() (Ban, error) {
	address, err := CanonicalAddress(b.Address)
	if err != nil {
		return Ban{}, err
	}
	seen := make(map[string]struct{}, len(b.Nodes))
	ids := make([]string, 0, len(b.Nodes))
	for _, id := range b.Nodes {
		id = normalizeID(id)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return Ban{Address: address, Nodes: ids}, nil
}

func transact(layout paths.Layout, mutate func([]Ban) ([]Ban, error)) ([]Ban, error) {
	return state.TransactEntryDirs(bansPath(layout), decodeBan, encodeBan, mutate)
}

func indexOf(list []Ban, address string) int {
	for i := range list {
		if list[i].Address == address {
			return i
		}
	}
	return -1
}

func normalizeID(v string) string { return strings.ToLower(strings.TrimSpace(v)) }

func decodeBan(root string) Ban {
	get := func(name string) string { return state.ReadEntryValue(root, name, "") }
	ban := Ban{Address: strings.TrimSpace(get("address"))}
	if canonical, err := CanonicalAddress(ban.Address); err == nil {
		ban.Address = canonical
	}
	for _, id := range strings.Split(get("nodes"), ",") {
		if id = normalizeID(id); id != "" {
			ban.Nodes = append(ban.Nodes, id)
		}
	}
	return ban
}

func encodeBan(b Ban) map[string]string {
	ids := append([]string(nil), b.Nodes...)
	sort.Strings(ids)
	return map[string]string{
		"address": b.Address,
		"nodes":   strings.Join(ids, ","),
	}
}
