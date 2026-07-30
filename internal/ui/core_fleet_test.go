package ui

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/deploy"
	"github.com/C5Hwang/singbox-deploy/internal/nodes"
	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestCoreManagementShowsFleetAndRunsCoordinatedChange(t *testing.T) {
	layout := protocolManagerState(t, "vless-reality-vision", "www.microsoft.com")
	if err := nodes.Save(layout, []nodes.Node{
		{
			ID: "11111111111111111111111111111111", Alias: "London",
			Installed: true, SingBoxVersion: "v1.12.2",
		},
		{
			ID: "22222222222222222222222222222222", Alias: "Tokyo",
			Installed: true, SingBoxVersion: "v1.12.3",
		},
	}); err != nil {
		t.Fatal(err)
	}
	withCoreDeps(t, layout)

	cm := newCoreManager()
	actionView := cm.View()
	for _, want := range []string{
		"Current version (Hub)", "Spokes (last authenticated health)",
		"London", "v1.12.2", "Tokyo", "v1.12.3",
		"Change sing-box version",
	} {
		if !strings.Contains(actionView, want) {
			t.Fatalf("fleet core view missing %q:\n%s", want, actionView)
		}
	}

	cm.action = coreActionChangeStable
	cm.targetTag = "v1.12.4"
	cm.phase = corePhaseConfirm
	confirm := cm.View()
	for _, want := range []string{
		"v1.12.4", "Hub + 2 installed Spoke(s)", "then the Hub",
		"rolled back to its previous version",
		"Change sing-box version",
	} {
		if !strings.Contains(confirm, want) {
			t.Fatalf("fleet confirmation missing %q:\n%s", want, confirm)
		}
	}
	// The action row must name the action, not the "Config" heading the action
	// sits under; the two are indistinguishable to a lookup that keeps
	// separators.
	if strings.Contains(confirm, "Config") {
		t.Fatalf("fleet confirmation reported a group heading as the action:\n%s", confirm)
	}

	var gotLayout paths.Layout
	var gotTarget string
	changeFleetCoreRun = func(
		_ context.Context,
		layout paths.Layout,
		target string,
		_ io.Writer,
		progress func(deploy.Event),
	) error {
		gotLayout, gotTarget = layout, target
		progress(deploy.Event{Index: 1, Total: 1, Label: "Fleet", Status: "ok"})
		return nil
	}
	cmd := cm.startRun()
	for i := 0; i < 10 && !cm.runComplete; i++ {
		if cmd == nil {
			t.Fatal("fleet run stopped waiting before completion")
		}
		msg := cmd()
		cmd, _ = cm.Update(msg)
	}
	if !cm.runComplete || cm.runErr != nil {
		t.Fatalf("fleet run complete=%v err=%v", cm.runComplete, cm.runErr)
	}
	if gotLayout.Root != layout.Root || gotTarget != "v1.12.4" || cm.resultTag != "v1.12.4" {
		t.Fatalf("fleet call layout=%q target=%q result=%q", gotLayout.Root, gotTarget, cm.resultTag)
	}

	// Completion stays on the running view until the operator acknowledges it,
	// matching every other streamed management operation.
	_, _ = cm.handleKey(tea.KeyMsg{Type: tea.KeyEnter})
	if cm.phase != corePhaseDone {
		t.Fatalf("acknowledging fleet completion left phase %v", cm.phase)
	}
}

func TestSpokeRowShowsLastAuthenticatedCoreVersion(t *testing.T) {
	row := renderNodeRow(nodes.Node{
		ID: "11111111111111111111111111111111", Alias: "London",
		Domain: "uk.example.com", WGIP: "10.90.0.2", Installed: true,
		AgentVersion: toolVersion, SingBoxVersion: "v1.12.4",
	})
	for _, want := range []string{"London", "agent " + toolVersion, "core v1.12.4"} {
		if !strings.Contains(row, want) {
			t.Fatalf("spoke row missing %q: %s", want, row)
		}
	}
}
