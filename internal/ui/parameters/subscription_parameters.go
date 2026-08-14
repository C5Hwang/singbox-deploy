package parameters

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

// Labels and notes shared by the setup and edit forms, so the same input reads
// the same way in both.
const (
	LabelDisplayName   = "Node display name"
	LabelSubscribePort = "Subscription HTTPS port"
	LabelSubscribeSalt = "Subscription salt"

	LabelGroupAlias   = "Subscription group name"
	LabelGroupSalt    = "Subscription group salt"
	LabelGroupMembers = "Nodes published by this group"

	NoteDisplayName   = "The name this server's nodes show under in client apps."
	NoteSubscribePort = "The HTTPS port serving subscription links and the cover site."
	NoteSubscribeSalt = "The secret the subscription link is derived from."
	NoteGroupAlias    = "Names the group in these menus only."
	NoteGroupSalt     = NoteSubscribeSalt + "\nEach group needs its own."
	NoteGroupMembers  = "Only the nodes you tick appear in this group's subscription.\n" +
		"Order follows Reorder nodes."

	// noteRandomSalt is the same offer on both salt fields, so a blank answer
	// reads as an option rather than as a field left unfinished.
	noteRandomSalt = "\nBlank generates one."
)

func SubscriptionInstallFields() []Field {
	return []Field{
		{Key: "display_name", Label: LabelDisplayName, Def: deploy.DefaultDisplayName, Note: NoteDisplayName},
		{Key: "subscribe_port", Label: LabelSubscribePort, Def: strconv.Itoa(deploy.DefaultSubscribePort), Note: NoteSubscribePort},
		{Key: "subscribe_salt", Label: LabelSubscribeSalt + " (optional)", Note: NoteSubscribeSalt + noteRandomSalt},
	}
}

func SubscriptionDisplayNameField(cfg deploy.Config) Field {
	return Field{Key: "display_name", Label: LabelDisplayName, Def: cfg.DisplayName, Note: NoteDisplayName}
}

// SubscriptionLocalFields collects the hub-wide subscription settings. Salts
// belong to individual subscription groups, so only the shared public port is
// edited here.
func SubscriptionLocalFields(cfg deploy.Config) []Field {
	return []Field{
		{Key: "subscribe_port", Label: LabelSubscribePort, Def: strconv.Itoa(cfg.SubscribePort),
			Note: NoteSubscribePort + "\nChanging it rewrites every subscription link."},
	}
}

// SubscriptionGroupFields collects one subscription group's settings. Member
// options are supplied by the caller because they are derived from the live
// node registry.
func SubscriptionGroupFields(memberOptions []string, defaultMembers string) []Field {
	return []Field{
		{Key: "group_alias", Label: LabelGroupAlias, Note: NoteGroupAlias},
		{Key: "group_salt", Label: LabelGroupSalt + " (optional)", Note: NoteGroupSalt + noteRandomSalt},
		{Key: "group_members", Label: LabelGroupMembers, Def: defaultMembers, Options: memberOptions, Multi: true, Note: NoteGroupMembers},
	}
}

func ValidateSubscriptionParameterValue(key, val string) error {
	switch key {
	case "display_name":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("display name is required")
		}
	case "subscribe_salt", "remote_salt":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("salt is required")
		}
	case "group_alias":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("subscription group name is required")
		}
	case "group_members":
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("select at least one node for this group")
		}
	case "subscribe_port", "remote_subscribe_port":
		port, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
	}
	return nil
}
