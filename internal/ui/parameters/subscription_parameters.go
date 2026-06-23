package parameters

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
)

func SubscriptionInstallFields() []Field {
	return []Field{
		{Key: "display_name", Label: "Node display name", Def: deploy.DefaultDisplayName, Note: "Used only in generated node names shown by clients."},
		{Key: "subscribe_port", Label: "Subscription/Nginx HTTPS port", Def: strconv.Itoa(deploy.DefaultSubscribePort), Note: "Nginx listens on this public HTTPS port for /s subscriptions and the masquerade site."},
		{Key: "subscribe_salt", Label: "Subscription salt", Note: "Blank generates a random salt. The URL token is md5(salt + newline)."},
	}
}

func SubscriptionDisplayNameField(cfg deploy.Config) Field {
	return Field{Key: "display_name", Label: "Node display name", Def: cfg.DisplayName, Note: "Used only for generated node names shown by clients."}
}

func SubscriptionLocalFields(cfg deploy.Config) []Field {
	return []Field{
		{Key: "subscribe_salt", Label: "Subscription salt", Def: cfg.Salt, Note: "Changing salt changes all subscription URLs. Token is md5(salt + newline)."},
		{Key: "subscribe_port", Label: "Subscription/Nginx HTTPS port", Def: strconv.Itoa(cfg.SubscribePort), Note: "Changing this rewrites Nginx config and restarts Nginx."},
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
