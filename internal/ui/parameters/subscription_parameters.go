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

	NoteDisplayName    = "Names the hub's nodes in client apps."
	NoteSubscribePort  = "Nginx listens on this public HTTPS port for /s subscriptions and the masquerade site."
	NoteSubscribeToken = "The subscription URL token is md5(salt + newline)."
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

func SubscriptionLocalFields(cfg deploy.Config) []Field {
	return []Field{
		{Key: "subscribe_salt", Label: LabelSubscribeSalt, Def: cfg.Salt, Note: "Changing the salt changes every subscription URL. " + NoteSubscribeToken},
		{Key: "subscribe_port", Label: LabelSubscribePort, Def: strconv.Itoa(cfg.SubscribePort), Note: NoteSubscribePort + " Changing it rewrites the Nginx config and restarts Nginx."},
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
	case "subscribe_port", "remote_subscribe_port":
		port, err := strconv.Atoi(strings.TrimSpace(val))
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("port must be between 1 and 65535")
		}
	}
	return nil
}
