package ui

import tea "github.com/charmbracelet/bubbletea"

type noticeSeverity uint8

const (
	noticeNone noticeSeverity = iota
	noticeInfo
	noticeError
)

// transientNotice is a list-screen message with an explicit severity and a
// user-action lifecycle. Async messages leave it visible; the next real
// keyboard or mouse action dismisses it before that action is handled.
type transientNotice struct {
	severity noticeSeverity
	text     string
}

func (n *transientNotice) setError(text string) {
	n.severity = noticeError
	n.text = text
}

func (n *transientNotice) setInfo(text string) {
	n.severity = noticeInfo
	n.text = text
}

func (n *transientNotice) clear() {
	n.severity = noticeNone
	n.text = ""
}

func (n *transientNotice) clearForUserAction(msg tea.Msg) {
	switch msg.(type) {
	case tea.KeyMsg, tea.MouseMsg:
		n.clear()
	}
}

func (n transientNotice) view() string {
	if n.text == "" {
		return ""
	}
	if n.severity == noticeError {
		return flowErr.Render(n.text)
	}
	return summaryInfo.Render(n.text)
}
