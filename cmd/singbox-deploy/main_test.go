package main

import (
	"bytes"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	oldVersion := version
	version = "v2.3.4"
	t.Cleanup(func() { version = oldVersion })

	var out bytes.Buffer
	if !printVersion([]string{"singbox-deploy", "--version"}, &out) {
		t.Fatal("--version was not handled")
	}
	if got := out.String(); got != "v2.3.4\n" {
		t.Fatalf("version output = %q", got)
	}

	out.Reset()
	if printVersion([]string{"singbox-deploy"}, &out) {
		t.Fatal("interactive invocation was handled as --version")
	}
	if out.Len() != 0 {
		t.Fatalf("interactive invocation wrote %q", out.String())
	}
}

func TestCandidateInspectionAndAgentExportSkipHubMigration(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{args: []string{"singbox-deploy"}, want: true},
		{args: []string{"singbox-deploy", "monitor", "serve"}, want: true},
		{args: []string{"singbox-deploy", "cert", "renew"}, want: true},
		{args: []string{"singbox-deploy", "--version"}, want: false},
		{args: []string{"singbox-deploy", "agent", "export", "--arch", "amd64"}, want: false},
	}
	for _, tt := range tests {
		if got := shouldMigrateHubSubscriptions(tt.args); got != tt.want {
			t.Errorf("shouldMigrateHubSubscriptions(%q) = %v, want %v", tt.args, got, tt.want)
		}
	}
}
