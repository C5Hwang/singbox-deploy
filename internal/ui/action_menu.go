package ui

import "strings"

type actionItem[T comparable] struct {
	action    T
	label     string
	separator bool
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
