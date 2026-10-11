package main

import (
	"context"
	"flag"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
	"github.com/C5Hwang/singbox-deploy/internal/subserve"
)

// runSubscription dispatches the "subscription serve" subcommand that runs the
// loopback service Nginx hands query-bearing subscription links to.
func runSubscription(args []string) error {
	if len(args) == 0 || args[0] != "serve" {
		return flag.ErrHelp
	}
	fs := flag.NewFlagSet("subscription serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:"+strconv.Itoa(deploy.DefaultSubscriptionServicePort), "listen address")
	layout := paths.DefaultLayout()
	fs.StringVar(&layout.SubscribeDir, "subscribe-dir", layout.SubscribeDir, "published subscription directory")
	fs.StringVar(&layout.SubscribeKeysDir, "keys-dir", layout.SubscribeKeysDir, "directory holding each subscription's auth key cipher key")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return subserve.Serve(ctx, *listen, layout)
}
