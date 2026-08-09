package ui

import (
	"context"
	"fmt"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/credentials"
	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/hubctl"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subgroups"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

// groupMember pairs a member option label with the registry ID it selects, so
// the multi-choice form can work in labels while the registry stores IDs.
type groupMember struct {
	id    string
	label string
}

// groupMemberOptions lists the hub followed by every registered spoke, in the
// order their nodes appear in generated subscriptions.
func groupMemberOptions(displayName string, list []nodes.Node) []groupMember {
	members := make([]groupMember, 0, len(list)+1)
	members = append(members, groupMember{
		id:    subgroups.HubMemberID,
		label: optionLabel("Hub (" + or(displayName, deploy.DefaultDisplayName) + ")"),
	})
	for _, node := range list {
		members = append(members, groupMember{id: node.ID, label: spokeOptionLabel(node)})
	}
	return members
}

func groupMemberLabels(members []groupMember) []string {
	labels := make([]string, len(members))
	for i, member := range members {
		labels[i] = member.label
	}
	return labels
}

// groupMemberValue renders the selected IDs as the comma-joined option-label
// value the multi-choice form round-trips. Unknown IDs are dropped: they name a
// node that has since left the registry.
func groupMemberValue(members []groupMember, selected []string) string {
	chosen := make(map[string]bool, len(selected))
	for _, id := range selected {
		chosen[strings.ToLower(strings.TrimSpace(id))] = true
	}
	labels := make([]string, 0, len(members))
	for _, member := range members {
		if chosen[strings.ToLower(strings.TrimSpace(member.id))] {
			labels = append(labels, member.label)
		}
	}
	return strings.Join(labels, ",")
}

// groupMemberIDs maps a form value back to registry IDs.
func groupMemberIDs(members []groupMember, value string) []string {
	byLabel := make(map[string]string, len(members))
	for _, member := range members {
		byLabel[member.label] = member.id
	}
	var ids []string
	for _, label := range strings.Split(value, ",") {
		if id, ok := byLabel[strings.TrimSpace(label)]; ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// optionLabel makes a runtime-derived string safe as a multi-choice option: the
// form joins selected options with commas, so an alias containing one would be
// split into two unmatchable halves.
func optionLabel(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, ",", " "))
}

// groupOptionLabel identifies one group in a single-choice picker. The member
// count is included because two groups differ mainly in what they publish.
func groupOptionLabel(g subgroups.Group, list []nodes.Node) string {
	return fmt.Sprintf("%s (%s)", optionLabel(g.EffectiveAlias()), groupMemberSummary(g, list))
}

func groupLabels(groups []subgroups.Group, list []nodes.Node) []string {
	labels := make([]string, len(groups))
	for i, g := range groups {
		labels[i] = groupOptionLabel(g, list)
	}
	return labels
}

// groupMemberSummary describes a group's membership in one short phrase.
func groupMemberSummary(g subgroups.Group, list []nodes.Node) string {
	spokes := 0
	for _, node := range list {
		if g.HasMember(node.ID) {
			spokes++
		}
	}
	parts := make([]string, 0, 2)
	if g.HasMember(subgroups.HubMemberID) {
		parts = append(parts, "hub")
	}
	if spokes > 0 {
		parts = append(parts, fmt.Sprintf("%d spoke%s", spokes, plural(spokes)))
	}
	if len(parts) == 0 {
		return "no nodes"
	}
	return strings.Join(parts, " + ")
}

// labelGroupNotPublished stands in for a subscription URL that is not served.
const labelGroupNotPublished = "not published"

// groupPublishes reports whether a group currently has nodes to publish. It
// mirrors deploy.SubscriptionGroupSpec.PublishesNodes: the hub always
// contributes its own nodes, and a spoke contributes only while the registry
// still knows it. A group with neither is skipped by the publisher, so quoting
// its URL would send the operator to a 404.
func groupPublishes(g subgroups.Group, list []nodes.Node) bool {
	if g.HasMember(subgroups.HubMemberID) {
		return true
	}
	for _, node := range list {
		if g.HasMember(node.ID) {
			return true
		}
	}
	return false
}

// groupMemberNames lists a group's members by the name their nodes carry in
// generated subscriptions.
func groupMemberNames(g subgroups.Group, displayName string, list []nodes.Node) []string {
	var names []string
	if g.HasMember(subgroups.HubMemberID) {
		names = append(names, or(displayName, deploy.DefaultDisplayName))
	}
	for _, node := range list {
		if g.HasMember(node.ID) {
			names = append(names, node.EffectiveSubscriptionAlias())
		}
	}
	return names
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// resolveGroupSalt returns the salt to persist for a group, generating a random
// one when the operator left the field blank.
func resolveGroupSalt(value string) (string, error) {
	if salt := strings.TrimSpace(value); salt != "" {
		return salt, nil
	}
	return credentials.Salt()
}

// applyGroupChange persists one subscription-group registry change and
// republishes every group's outputs. The registry write is a separate step from
// the refresh so a failure to reach a spoke cannot make the edit look rejected.
func applyGroupChange(ctx context.Context, layout paths.Layout, label, detail string,
	progress func(deploy.Event), change func() error) error {
	return deploy.RunSteps(ctx, progress, []deploy.Step{
		{Label: label, Detail: detail, Run: func(context.Context) error { return change() }},
		{Label: "Subscriptions", Detail: "republish every subscription group", Run: func(ctx context.Context) error {
			return (&hubctl.Controller{Layout: layout, ExpectedVersion: toolVersion}).RefreshSubscriptions(ctx)
		}},
	})
}

// groupSubscriptionURLs returns the four published URLs for one group.
func groupSubscriptionURLs(domain string, port int, salt string) map[string]string {
	token := deploy.SubscriptionToken(salt)
	base := fmt.Sprintf("https://%s:%d/s", domain, port)
	return map[string]string{
		"default":           base + "/default/" + token,
		"clashMetaProfiles": base + "/clashMetaProfiles/" + token,
		"singboxProfiles":   base + "/singboxProfiles/" + token,
		"surgeProfiles":     base + "/surgeProfiles/" + token,
	}
}

// groupFields builds the add/edit form for one group.
func groupFields(members []groupMember, defaultMembers string) []field {
	return fieldsFromParameters(uiparams.SubscriptionGroupFields(groupMemberLabels(members), defaultMembers))
}
