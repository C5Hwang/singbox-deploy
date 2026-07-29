package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/paths"
)

func TestNodeErrorNoticeRendersAtTopAndClearsOnUserAction(t *testing.T) {
	m := &nodeManager{
		run:      newCommandRun(),
		form:     newParameterForm(nil),
		layout:   paths.LayoutForRoot(t.TempDir()),
		phase:    nodePhaseList,
		hubReady: true,
	}
	const message = "spoke operation failed"
	m.notice.setError(message)

	view := m.View()
	errorAt := strings.Index(view, message)
	actionsAt := strings.Index(view, "Add spoke node")
	if errorAt < 0 || actionsAt < 0 || errorAt > actionsAt {
		t.Fatalf("error notice is not above the action list:\n%s", view)
	}
	if !strings.Contains(view, flowErr.Render(message)) {
		t.Fatalf("error notice does not use the shared error style:\n%s", view)
	}

	_, _ = m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.notice.text == "" {
		t.Fatal("async message cleared the error notice")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.notice.text != "" {
		t.Fatalf("user action did not clear the error notice: %q", m.notice.text)
	}
}

func TestCertificateErrorNoticeUsesSameLifecycle(t *testing.T) {
	m := newCertificateManagerForTest(t)
	const message = "no certificates to renew"
	m.notice.setError(message)

	view := m.View()
	errorAt := strings.Index(view, message)
	actionsAt := strings.Index(view, "Add certificate")
	if errorAt < 0 || actionsAt < 0 || errorAt > actionsAt {
		t.Fatalf("error notice is not above the action list:\n%s", view)
	}
	if !strings.Contains(view, flowErr.Render(message)) {
		t.Fatalf("error notice does not use the shared error style:\n%s", view)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.notice.text != "" {
		t.Fatalf("user action did not clear the error notice: %q", m.notice.text)
	}
}
