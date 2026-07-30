package ui

import "strings"

type actionItem[T comparable] struct {
	action    T
	label     string
	separator bool
}

// currentActionLabel returns the label of the current action in items.
//
// Separators are skipped: they leave action at its zero value, which collides
// with the first iota constant of every action enum. Without the guard a
// leading separator shadows the action it introduces and its group heading is
// reported as the action name.
func currentActionLabel[T comparable](items []actionItem[T], current T) string {
	for _, item := range items {
		if !item.separator && item.action == current {
			return item.label
		}
	}
	return "unknown"
}

func moveActionCursor[T comparable](cursor int, items []actionItem[T], delta int) int {
	n := len(items)
	if n == 0 {
		return 0
	}
	next := cursor
	for {
		next = (next + delta) % n
		if next < 0 {
			next += n
		}
		if !items[next].separator {
			break
		}
		if next == cursor {
			break
		}
	}
	return next
}

func renderActionList[T comparable](items []actionItem[T], cursor int) string {
	var b strings.Builder
	for i, item := range items {
		if item.separator {
			b.WriteString("\n" + dimStyle.Render(item.label) + "\n")
			continue
		}
		row := "  " + item.label
		if i == cursor {
			row = selStyle.Render("> " + item.label)
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}
