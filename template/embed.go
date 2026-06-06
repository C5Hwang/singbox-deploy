// Package assets bundles every template and static asset into the binary via
// go:embed. Release binaries have no runtime dependency on an external
// ./template directory.
package assets

import "embed"

//go:embed nginx/* service/* site/*.zip sing-box/* subscription/* monitor-ui/*
var FS embed.FS
