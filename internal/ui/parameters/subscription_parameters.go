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

	NoteDisplayName    = "Names the hub's nodes in client apps."
	NoteSubscribePort  = "Nginx listens on this public HTTPS port for /s subscriptions and the masquerade site."
	NoteSubscribeToken = "The subscription URL token is md5(salt + newline)."
	NoteGroupAlias     = "Names the group in this UI only; it never appears in generated node names."
	NoteGroupSalt      = "Each group needs its own salt: it derives the group's subscription URL. " + NoteSubscribeToken
	NoteGroupMembers   = "Only the selected nodes appear in this group's subscription. Ordering follows Reorder nodes."
)

func SubscriptionInstallFields() []Field {
	return []Field{
		{Key: "display_name", Label: LabelDisplayName, Def: deploy.DefaultDisplayName, Note: NoteDisplayName},
		{Key: "subscribe_port", Label: LabelSubscribePort, Def: strconv.Itoa(deploy.DefaultSubscribePort), Note: NoteSubscribePort},
		{Key: "subscribe_salt", Label: LabelSubscribeSalt + " (optional)", Note: "Blank generates a random salt. " + NoteSubscribeToken},
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
		{Key: "subscribe_port", Label: LabelSubscribePort, Def: strconv.Itoa(cfg.SubscribePort), Note: NoteSubscribePort + " Changing it rewrites the Nginx config, restarts Nginx, and changes the host and port of every group's subscription URL."},
	}
}

// SubscriptionGroupFields collects one subscription group's settings. Member
// options are supplied by the caller because they are derived from the live
// node registry.
func SubscriptionGroupFields(memberOptions []string, defaultMembers string) []Field {
	return []Field{
		{Key: "group_alias", Label: LabelGroupAlias, Note: NoteGroupAlias},
		{Key: "group_salt", Label: LabelGroupSalt + " (optional)", Note: "Blank generates a random salt. " + NoteGroupSalt},
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
