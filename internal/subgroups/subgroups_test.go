package subgroups_test

import (
	"strings"
	"testing"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subgroups"
)

func testLayout(t *testing.T) paths.Layout {
	t.Helper()
	return paths.LayoutForRoot(t.TempDir())
}

func TestAddLoadRoundTrip(t *testing.T) {
	layout := testLayout(t)
	added, err := subgroups.Add(layout, subgroups.Group{
		Alias: "Family", Salt: "familysalt",
		Members: []string{subgroups.HubMemberID, "aa11"},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if added.ID == "" {
		t.Fatal("Add should assign a stable ID")
	}
	list, err := subgroups.Load(layout)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("Load = %#v", list)
	}
	g := list[0]
	if g.ID != added.ID || g.Alias != "Family" || g.Salt != "familysalt" {
		t.Fatalf("round trip = %#v", g)
	}
	if !g.HasMember(subgroups.HubMemberID) || !g.HasMember("AA11") || g.HasMember("bb22") {
		t.Fatalf("members = %#v", g.Members)
	}
}

func TestAddRejectsDuplicateAliasAndSalt(t *testing.T) {
	layout := testLayout(t)
	if _, err := subgroups.Add(layout, subgroups.Group{
		Alias: "Family", Salt: "one", Members: []string{subgroups.HubMemberID},
	}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := subgroups.Add(layout, subgroups.Group{
		Alias: "family", Salt: "two", Members: []string{subgroups.HubMemberID},
	}); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("duplicate alias should be rejected: %v", err)
	}
	// Two groups sharing a salt share a URL token, so the second would silently
	// overwrite the first's published files.
	if _, err := subgroups.Add(layout, subgroups.Group{
		Alias: "Work", Salt: "one", Members: []string{subgroups.HubMemberID},
	}); err == nil || !strings.Contains(err.Error(), "same salt") {
		t.Fatalf("duplicate salt should be rejected: %v", err)
	}
}

func TestAddRejectsIncompleteGroup(t *testing.T) {
	layout := testLayout(t)
	for name, group := range map[string]subgroups.Group{
		"no alias":   {Salt: "s", Members: []string{subgroups.HubMemberID}},
		"no salt":    {Alias: "A", Members: []string{subgroups.HubMemberID}},
		"no members": {Alias: "A", Salt: "s"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := subgroups.Add(layout, group); err == nil {
				t.Fatalf("%s should be rejected", name)
			}
		})
	}
}

func TestUpdateReplacesByID(t *testing.T) {
	layout := testLayout(t)
	added, err := subgroups.Add(layout, subgroups.Group{
		Alias: "Family", Salt: "one", Members: []string{subgroups.HubMemberID},
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	added.Alias = "Household"
	added.Members = []string{"aa11"}
	if err := subgroups.Update(layout, added); err != nil {
		t.Fatalf("Update: %v", err)
	}
	list, _ := subgroups.Load(layout)
	if len(list) != 1 || list[0].Alias != "Household" || list[0].HasMember(subgroups.HubMemberID) {
		t.Fatalf("updated group = %#v", list)
	}
	if err := subgroups.Update(layout, subgroups.Group{
		ID: "deadbeef", Alias: "Ghost", Salt: "x", Members: []string{subgroups.HubMemberID},
	}); err == nil {
		t.Fatal("updating an unknown group should fail")
	}
}

// The hub publishes subscriptions only through groups, so removing the last one
// would take every client's URL offline.
func TestRemoveKeepsTheLastGroup(t *testing.T) {
	layout := testLayout(t)
	first, _ := subgroups.Add(layout, subgroups.Group{
		Alias: "Family", Salt: "one", Members: []string{subgroups.HubMemberID},
	})
	second, _ := subgroups.Add(layout, subgroups.Group{
		Alias: "Work", Salt: "two", Members: []string{subgroups.HubMemberID},
	})
	if err := subgroups.Remove(layout, second.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	err := subgroups.Remove(layout, first.ID)
	if err == nil || !strings.Contains(err.Error(), "only one left") {
		t.Fatalf("removing the last group should be refused: %v", err)
	}
	if list, _ := subgroups.Load(layout); len(list) != 1 {
		t.Fatalf("groups after removal = %#v", list)
	}
}

func TestAddAndDropMember(t *testing.T) {
	layout := testLayout(t)
	family, _ := subgroups.Add(layout, subgroups.Group{
		Alias: "Family", Salt: "one", Members: []string{subgroups.HubMemberID},
	})
	work, _ := subgroups.Add(layout, subgroups.Group{
		Alias: "Work", Salt: "two", Members: []string{subgroups.HubMemberID},
	})
	if err := subgroups.AddMember(layout, "aa11", []string{family.ID, "missing"}); err != nil {
		t.Fatalf("AddMember: %v", err)
	}
	// Adding twice must not duplicate the entry.
	if err := subgroups.AddMember(layout, "aa11", []string{family.ID}); err != nil {
		t.Fatalf("AddMember again: %v", err)
	}
	list, _ := subgroups.Load(layout)
	if len(list[0].Members) != 2 || !list[0].HasMember("aa11") {
		t.Fatalf("family members = %#v", list[0].Members)
	}
	if list[1].HasMember("aa11") {
		t.Fatalf("work should not have gained the member: %#v", list[1].Members)
	}
	_ = work

	if err := subgroups.DropMember(layout, "aa11"); err != nil {
		t.Fatalf("DropMember: %v", err)
	}
	list, _ = subgroups.Load(layout)
	for _, g := range list {
		if g.HasMember("aa11") {
			t.Fatalf("%s still names the removed node: %#v", g.Alias, g.Members)
		}
	}
}

func TestEnsureSeededOnlySeedsOnce(t *testing.T) {
	layout := testLayout(t)
	list, seeded, err := subgroups.EnsureSeeded(layout, "hubsalt", []string{subgroups.HubMemberID, "aa11"})
	if err != nil || !seeded {
		t.Fatalf("EnsureSeeded = %v, seeded=%v", err, seeded)
	}
	if len(list) != 1 || list[0].Alias != subgroups.DefaultAlias || list[0].Salt != "hubsalt" {
		t.Fatalf("seeded group = %#v", list)
	}
	// A second call must not add another group or change the existing salt.
	list, seeded, err = subgroups.EnsureSeeded(layout, "othersalt", []string{subgroups.HubMemberID})
	if err != nil {
		t.Fatalf("EnsureSeeded again: %v", err)
	}
	if seeded || len(list) != 1 || list[0].Salt != "hubsalt" {
		t.Fatalf("re-seed changed the registry: seeded=%v %#v", seeded, list)
	}
}

func TestConflictHelpersIgnoreTheExemptedGroup(t *testing.T) {
	layout := testLayout(t)
	family, _ := subgroups.Add(layout, subgroups.Group{
		Alias: "Family", Salt: "one", Members: []string{subgroups.HubMemberID},
	})
	list, _ := subgroups.Load(layout)
	if _, clash := subgroups.AliasConflict(list, "Family", family.ID); clash {
		t.Fatal("a group must not conflict with itself")
	}
	if _, clash := subgroups.AliasConflict(list, "family", ""); !clash {
		t.Fatal("alias comparison should ignore case")
	}
	if _, clash := subgroups.SaltConflict(list, "one", family.ID); clash {
		t.Fatal("a group's own salt must not conflict")
	}
	if _, clash := subgroups.SaltConflict(list, "one", ""); !clash {
		t.Fatal("duplicate salt should be reported")
	}
}

func TestEffectiveAliasFallsBackToID(t *testing.T) {
	g := subgroups.Group{ID: "0123456789abcdef"}
	if got := g.EffectiveAlias(); got != "group-01234567" {
		t.Fatalf("EffectiveAlias = %q", got)
	}
}
