package ui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type reorderItem struct {
	key   string
	label string
}

type reorderForm struct {
	items   []reorderItem
	cursor  int
	grabbed bool
}

func newReorderForm(items []reorderItem) reorderForm {
	cp := make([]reorderItem, len(items))
	copy(cp, items)
	return reorderForm{items: cp}
}

func (f *reorderForm) handleKey(msg tea.KeyMsg) (confirm, cancel bool) {
	switch {
	case isSelectionPreviousKey(msg):
		if f.grabbed {
			f.moveItem(-1)
		} else {
			f.moveCursor(-1)
		}
	case isSelectionNextKey(msg):
		if f.grabbed {
			f.moveItem(1)
		} else {
			f.moveCursor(1)
		}
	case isSelectionToggleKey(msg):
		f.grabbed = !f.grabbed
	case isSelectionConfirmKey(msg):
		if f.grabbed {
			f.grabbed = false
		} else {
			return true, false
		}
	case isSelectionCancelKey(msg):
		if f.grabbed {
			f.grabbed = false
		} else {
			return false, true
		}
	}
	return false, false
}

func (f *reorderForm) moveCursor(delta int) {
	f.cursor = moveSelection(f.cursor, len(f.items), delta)
}

func (f *reorderForm) moveItem(delta int) {
	if len(f.items) <= 1 {
		return
	}
	newPos := f.cursor + delta
	if newPos < 0 || newPos >= len(f.items) {
		return
	}
	f.items[f.cursor], f.items[newPos] = f.items[newPos], f.items[f.cursor]
	f.cursor = newPos
}

func (f *reorderForm) View(title string) string {
	var b strings.Builder
	b.WriteString(flowTitle.Render(title) + "\n\n")
	for i, item := range f.items {
		if i == f.cursor {
			if f.grabbed {
				b.WriteString(selStyle.Render("▸ "+item.label+" ◀") + "\n")
			} else {
				b.WriteString(selStyle.Render("> "+item.label) + "\n")
			}
		} else {
			b.WriteString("  " + item.label + "\n")
		}
	}
	return b.String()
}

func (f *reorderForm) footerHints() []operationHint {
	if f.grabbed {
		return reorderGrabbedFooterHints()
	}
	return reorderFooterHints()
}
