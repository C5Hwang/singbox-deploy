package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subgroups"
)

// subscriptionGroupState returns an installed hub layout with two registered
// spokes and one subscription group publishing the hub plus the first spoke.
func subscriptionGroupState(t *testing.T) (paths.Layout, []nodes.Node) {
	t.Helper()
	layout := protocolManagerState(t, "hysteria2", "www.microsoft.com")
	list := []nodes.Node{
		{
			ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Alias: "tokyo", SubscriptionAlias: "JP",
			Domain: "tokyo.example.com", WGIP: "10.90.0.2", Installed: true,
			IncludeInSubscription: true, EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		},
		{
			ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Alias: "london", SubscriptionAlias: "UK",
			Domain: "london.example.com", WGIP: "10.90.0.3", Installed: true,
			IncludeInSubscription: true, EnabledProtocols: []string{"hysteria2"}, Hysteria2Port: 9443,
		},
	}
	if err := nodes.Save(layout, list); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	if err := subgroups.Save(layout, []subgroups.Group{{
		ID: "aa11", Alias: "Family", Salt: "familysalt",
		Members: []string{subgroups.HubMemberID, list[0].ID},
	}}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	return layout, list
}

func TestAddGroupFormPreselectsEveryNode(t *testing.T) {
	layout, list := subscriptionGroupState(t)
	withSubscriptionDeps(t, layout)
	sm := newSubscriptionManager()
	sm.cursor = subscriptionActionCursor(t, sm, subscriptionActionAddGroup)
	sm.activateAction()

	if sm.phase != subscriptionPhaseForm {
		t.Fatalf("add group should open a form, phase=%v err=%q", sm.phase, sm.fieldErr)
	}
	keys := make([]string, len(sm.fields))
	for i, f := range sm.fields {
		keys[i] = f.key
	}
	if strings.Join(keys, ",") != "group_alias,group_salt,group_members" {
		t.Fatalf("add group fields = %v", keys)
	}
	members := sm.fields[2]
	if len(members.options) != len(list)+1 || !members.multi {
		t.Fatalf("member options = %#v", members.options)
	}
	// Everything is preselected: adding a group usually means publishing the
	// fleet and narrowing from there.
	if members.def != strings.Join(members.options, ",") {
		t.Fatalf("member default = %q, want every option", members.def)
	}
	if !strings.HasPrefix(members.options[0], "Hub (") {
		t.Fatalf("the hub must be selectable as a member: %q", members.options[0])
	}
}

func TestFormGroupMapsMemberLabelsBackToRegistryIDs(t *testing.T) {
	layout, list := subscriptionGroupState(t)
	withSubscriptionDeps(t, layout)
	sm := newSubscriptionManager()
	sm.action = subscriptionActionAddGroup
	sm.members = groupMemberOptions(sm.cfg.DisplayName, sm.nodes)
	sm.values = map[string]string{
		"group_alias":   "Work",
		"group_salt":    "worksalt",
		"group_members": strings.Join([]string{sm.members[0].label, sm.members[2].label}, ","),
	}

	group := sm.formGroup()
	if group.Alias != "Work" || group.Salt != "worksalt" {
		t.Fatalf("form group = %#v", group)
	}
	if len(group.Members) != 2 ||
		!group.HasMember(subgroups.HubMemberID) || !group.HasMember(list[1].ID) {
		t.Fatalf("members = %#v", group.Members)
	}
	if group.HasMember(list[0].ID) {
		t.Fatalf("unselected spoke leaked into the group: %#v", group.Members)
	}
}

func TestEditGroupFormSeedsCurrentMembership(t *testing.T) {
	layout, list := subscriptionGroupState(t)
	withSubscriptionDeps(t, layout)
	sm := newSubscriptionManager()
	sm.action = subscriptionActionEditGroup
	sm.editGroupIndex = 0
	sm.startEditGroupForm()

	if sm.values["group_alias"] != "Family" || sm.values["group_salt"] != "familysalt" {
		t.Fatalf("seeded values = %#v", sm.values)
	}
	selected := sm.values["group_members"]
	if !strings.Contains(selected, sm.members[0].label) || !strings.Contains(selected, sm.members[1].label) {
		t.Fatalf("seeded members = %q", selected)
	}
	// The unpublished spoke starts deselected.
	if strings.Contains(selected, sm.members[2].label) {
		t.Fatalf("non-member spoke was preselected: %q", selected)
	}
	if ids := groupMemberIDs(sm.members, selected); len(ids) != 2 || ids[1] != list[0].ID {
		t.Fatalf("round-tripped members = %v", ids)
	}
}

func TestGroupFormRejectsDuplicateAliasAndSalt(t *testing.T) {
	layout, _ := subscriptionGroupState(t)
	withSubscriptionDeps(t, layout)
	sm := newSubscriptionManager()
	sm.cursor = subscriptionActionCursor(t, sm, subscriptionActionAddGroup)
	sm.activateAction()

	// The add form must reject a collision as it is typed, not only when the
	// registry write is attempted.
	sm.input.SetValue("Family")
	sm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(sm.fieldErr, "already used") {
		t.Fatalf("add form accepted a duplicate name, fieldErr=%q", sm.fieldErr)
	}

	aliasField := field{key: "group_alias"}
	if err := sm.validateGroupField(aliasField, "family", nil); err == nil {
		t.Fatal("a duplicate group name should be rejected while typing")
	}
	saltField := field{key: "group_salt"}
	if err := sm.validateGroupField(saltField, "familysalt", nil); err == nil {
		t.Fatal("a duplicate salt should be rejected while typing")
	}
	// A blank salt is generated at apply time and cannot collide.
	if err := sm.validateGroupField(saltField, "", nil); err != nil {
		t.Fatalf("blank salt should be accepted: %v", err)
	}

	// Editing a group must not report the group's own values as duplicates.
	sm.action = subscriptionActionEditGroup
	sm.editGroupIndex = 0
	if err := sm.validateGroupField(aliasField, "Family", nil); err != nil {
		t.Fatalf("a group must not conflict with itself: %v", err)
	}
	if err := sm.validateGroupField(saltField, "familysalt", nil); err != nil {
		t.Fatalf("a group must not conflict with its own salt: %v", err)
	}
}

func TestDeleteGroupRefusesTheLastGroup(t *testing.T) {
	layout, _ := subscriptionGroupState(t)
	withSubscriptionDeps(t, layout)
	sm := newSubscriptionManager()
	sm.cursor = subscriptionActionCursor(t, sm, subscriptionActionDeleteGroup)
	sm.activateAction()

	if sm.phase != subscriptionPhaseAction {
		t.Fatalf("deleting the only group should not open a form, phase=%v", sm.phase)
	}
	if !strings.Contains(sm.fieldErr, "last subscription group") {
		t.Fatalf("fieldErr = %q", sm.fieldErr)
	}
}

func TestAddSpokeFormOffersSubscriptionGroups(t *testing.T) {
	layout, _ := subscriptionGroupState(t)
	if err := subgroups.Save(layout, []subgroups.Group{
		{ID: "aa11", Alias: "Family", Salt: "familysalt", Members: []string{subgroups.HubMemberID}},
		{ID: "bb22", Alias: "Work", Salt: "worksalt", Members: []string{subgroups.HubMemberID}},
	}); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	m := &nodeManager{run: newCommandRun(), form: newParameterForm(nil), layout: layout, phase: nodePhaseList}
	m.reload()
	m.beginForm()

	var groupField *field
	for i := range m.form.fields {
		if m.form.fields[i].key == "subscription_groups" {
			groupField = &m.form.fields[i]
		}
	}
	if groupField == nil {
		t.Fatalf("add spoke form has no subscription group field")
	}
	if len(groupField.options) != 2 || !groupField.multi {
		t.Fatalf("group options = %#v", groupField.options)
	}
	if groupField.def != strings.Join(groupField.options, ",") {
		t.Fatalf("groups should default to all selected, got %q", groupField.def)
	}
	// Deselecting one group must leave the spoke out of exactly that group.
	if ids := m.selectedGroupIDs(groupField.options[1]); len(ids) != 1 || ids[0] != "bb22" {
		t.Fatalf("selected group IDs = %v", ids)
	}
	if ids := m.selectedGroupIDs(""); len(ids) != 0 {
		t.Fatalf("no selection should join no group, got %v", ids)
	}
}

func statusWithGroups(groups ...SubscriptionGroupStatus) Status {
	return Status{Domain: "hub.example.com", ToolVersion: "v1.2.3", Groups: groups}
}

func TestSubscriptionGroupPanelShowsSelectedGroup(t *testing.T) {
	m := &Model{groups: defaultGroups(), status: statusWithGroups(
		SubscriptionGroupStatus{Alias: "Family", Salt: "familysalt", Members: "HUB, JP", Published: true,
			Subscription: "https://hub.example.com:443/s/default/aaa"},
		SubscriptionGroupStatus{Alias: "Work", Salt: "worksalt", Members: "UK", Published: true,
			Subscription: "https://hub.example.com:443/s/default/bbb"},
	)}
	m.SetSize(120, 40)

	view := m.subscriptionGroupsView(60)
	if !strings.Contains(view, "Family") || !strings.Contains(view, "familysalt") ||
		!strings.Contains(view, "/s/default/aaa") {
		t.Fatalf("panel should describe the first group:\n%s", view)
	}
	if !strings.Contains(view, "[1/2]") {
		t.Fatalf("panel should show the group position:\n%s", view)
	}
	if strings.Contains(view, "worksalt") {
		t.Fatalf("panel should show one group at a time:\n%s", view)
	}

	// The subscription URLs live in this panel, not the status list.
	if status := m.statusView(); strings.Contains(status, "/s/default/") || strings.Contains(status, "familysalt") {
		t.Fatalf("status panel should not repeat subscription details:\n%s", status)
	}
	// Both panels are rendered on the main screen.
	body := m.bodyView(120, 40)
	if !strings.Contains(body, "Status") || !strings.Contains(body, "Subscription groups") {
		t.Fatalf("main screen should show both panels:\n%s", body)
	}
}

func TestBracketKeysCycleSubscriptionGroups(t *testing.T) {
	m := &Model{groups: defaultGroups(), status: statusWithGroups(
		SubscriptionGroupStatus{Alias: "Family", Subscription: "a"},
		SubscriptionGroupStatus{Alias: "Work", Subscription: "b"},
		SubscriptionGroupStatus{Alias: "Guest", Subscription: "c"},
	)}
	press := func(key string) {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	}
	press("]")
	if m.groupIndex != 1 {
		t.Fatalf("] should advance, groupIndex=%d", m.groupIndex)
	}
	press("[")
	press("[")
	if m.groupIndex != 2 {
		t.Fatalf("[ should wrap to the last group, groupIndex=%d", m.groupIndex)
	}
	press("]")
	if m.groupIndex != 0 {
		t.Fatalf("] should wrap to the first group, groupIndex=%d", m.groupIndex)
	}
	if !strings.Contains(m.footerView(), "[ / ]") {
		t.Fatalf("footer should advertise the switch key:\n%s", m.footerView())
	}
}

// The panel is only on screen on the main menu, so its keys must not be
// consumed while a sub-flow owns the content column.
func TestBracketKeysAreInertInsideASubFlow(t *testing.T) {
	m := &Model{groups: defaultGroups(), status: statusWithGroups(
		SubscriptionGroupStatus{Alias: "Family"},
		SubscriptionGroupStatus{Alias: "Work"},
	)}
	m.core = newCoreManager()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("]")})
	if m.groupIndex != 0 {
		t.Fatalf("a sub-flow key must not move the group panel, groupIndex=%d", m.groupIndex)
	}
}

func TestSubscriptionGroupPanelHiddenBeforeInstall(t *testing.T) {
	m := &Model{groups: defaultGroups()}
	m.SetSize(120, 40)
	if view := m.subscriptionGroupsView(60); view != "" {
		t.Fatalf("an uninstalled hub should render no group panel:\n%s", view)
	}
	if strings.Contains(m.footerView(), "[ / ]") {
		t.Fatalf("footer should not advertise a panel that is not shown")
	}
}

// A group deleted behind the UI must not leave the panel pointing past the end
// of the list.
func TestGroupIndexIsClampedAfterRefresh(t *testing.T) {
	if got := clampGroupIndex(5, 2); got != 1 {
		t.Fatalf("clampGroupIndex(5, 2) = %d", got)
	}
	if got := clampGroupIndex(3, 0); got != 0 {
		t.Fatalf("clampGroupIndex(3, 0) = %d", got)
	}
	if got := clampGroupIndex(-1, 4); got != 0 {
		t.Fatalf("clampGroupIndex(-1, 4) = %d", got)
	}
}

// A comma in an operator-chosen alias would split one multi-choice option into
// two unmatchable halves.
func TestOptionLabelStripsCommas(t *testing.T) {
	node := nodes.Node{ID: "aaaaaaaaaaaaaaaa", Alias: "Tokyo, Japan", WGIP: "10.90.0.2"}
	label := spokeOptionLabel(node)
	if strings.Contains(label, ",") {
		t.Fatalf("member label must not contain a comma: %q", label)
	}
	members := []groupMember{{id: node.ID, label: label}}
	if ids := groupMemberIDs(members, label); len(ids) != 1 || ids[0] != node.ID {
		t.Fatalf("label did not round-trip to its node: %v", ids)
	}
}

// A group whose last node left keeps its registry entry, but the hub writes no
// files for its token. The panel must say so instead of quoting four URLs that
// answer 404.
func TestSubscriptionGroupPanelReportsAnUnpublishedGroup(t *testing.T) {
	m := &Model{groups: defaultGroups(), status: statusWithGroups(
		SubscriptionGroupStatus{Alias: "Emptied", Salt: "emptiedsalt"},
	)}
	m.SetSize(120, 40)

	view := m.subscriptionGroupsView(60)
	if strings.Contains(view, "/s/default/") {
		t.Fatalf("panel quoted a URL for a group with no nodes:\n%s", view)
	}
	if strings.Count(view, labelGroupNotPublished) != 4 {
		t.Fatalf("every format should report %q:\n%s", labelGroupNotPublished, view)
	}
	// The salt still identifies the group, so it stays visible.
	if !strings.Contains(view, "emptiedsalt") {
		t.Fatalf("panel dropped the group salt:\n%s", view)
	}
}

// loadStatus derives Published from the registry, so a group naming only nodes
// that no longer exist reports no URLs at all.
func TestSubscriptionGroupStatusesOmitURLsForAGroupWithNoNodes(t *testing.T) {
	list := []nodes.Node{{ID: "aaaa1111", Alias: "UK"}}
	groups := []subgroups.Group{
		{ID: "g1", Alias: "Live", Salt: "livesalt", Members: []string{subgroups.HubMemberID}},
		{ID: "g2", Alias: "Spokes", Salt: "spokesalt", Members: []string{"aaaa1111"}},
		{ID: "g3", Alias: "Emptied", Salt: "emptiedsalt", Members: []string{"deadbeef"}},
	}
	for _, tc := range []struct {
		group     subgroups.Group
		published bool
	}{
		{groups[0], true},
		{groups[1], true},
		{groups[2], false},
	} {
		if got := groupPublishes(tc.group, list); got != tc.published {
			t.Errorf("groupPublishes(%s) = %v, want %v", tc.group.Alias, got, tc.published)
		}
	}

	layout := paths.LayoutForRoot(t.TempDir())
	if err := subgroups.Save(layout, groups); err != nil {
		t.Fatalf("save groups: %v", err)
	}
	if err := nodes.Save(layout, list); err != nil {
		t.Fatalf("save nodes: %v", err)
	}
	oldGroups, oldNodes := loadStatusGroups, loadStatusNodes
	t.Cleanup(func() { loadStatusGroups, loadStatusNodes = oldGroups, oldNodes })
	loadStatusGroups = func(paths.Layout) ([]subgroups.Group, error) { return subgroups.Load(layout) }
	loadStatusNodes = func(paths.Layout) ([]nodes.Node, error) { return nodes.Load(layout) }

	statuses := subscriptionGroupStatuses(layout, "hub.example.com", "2096", "HUB")
	if len(statuses) != 3 {
		t.Fatalf("statuses = %d, want 3", len(statuses))
	}
	for _, s := range statuses[:2] {
		if !s.Published || s.Subscription == "" || s.SingBoxSub == "" {
			t.Fatalf("live group %q lost its URLs: %+v", s.Alias, s)
		}
	}
	emptied := statuses[2]
	if emptied.Published {
		t.Fatalf("group naming only a departed node reported as published: %+v", emptied)
	}
	for _, url := range []string{emptied.Subscription, emptied.ClashMetaSub, emptied.SingBoxSub, emptied.SurgeSub} {
		if url != "" {
			t.Fatalf("unpublished group still carries %q", url)
		}
	}
}
