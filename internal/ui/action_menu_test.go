package ui

import "testing"

type testAction int

const (
	testActionFirst testAction = iota
	testActionSecond
)

// A separator leaves action at its zero value, which equals the first iota
// constant of every action enum. currentActionLabel must skip separators so a
// leading group heading does not shadow the action it introduces.
func TestCurrentActionLabelSkipsSeparators(t *testing.T) {
	items := []actionItem[testAction]{
		{separator: true, label: "Group heading"},
		{action: testActionFirst, label: "First action"},
		{action: testActionSecond, label: "Second action"},
	}
	for _, tc := range []struct {
		action testAction
		want   string
	}{
		{testActionFirst, "First action"},
		{testActionSecond, "Second action"},
	} {
		if got := currentActionLabel(items, tc.action); got != tc.want {
			t.Errorf("currentActionLabel(%v) = %q, want %q", tc.action, got, tc.want)
		}
	}
}
