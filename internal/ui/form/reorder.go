package form

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/ui/common"
)

type ReorderItem struct {
	Key   string
	Label string
}

type ReorderForm struct {
	Items   []ReorderItem
	Cursor  int
	Grabbed bool
}

func NewReorderForm(items []ReorderItem) ReorderForm {
	cp := make([]ReorderItem, len(items))
	copy(cp, items)
	return ReorderForm{Items: cp}
}

func (f *ReorderForm) HandleKey(msg tea.KeyMsg) (confirm, cancel bool) {
	switch {
	case common.IsSelectionPreviousKey(msg):
		if f.Grabbed {
			f.moveItem(-1)
		} else {
			f.moveCursor(-1)
		}
	case common.IsSelectionNextKey(msg):
		if f.Grabbed {
			f.moveItem(1)
		} else {
			f.moveCursor(1)
		}
	case common.IsSelectionToggleKey(msg):
		f.Grabbed = !f.Grabbed
	case common.IsSelectionConfirmKey(msg):
		if f.Grabbed {
			f.Grabbed = false
		} else {
			return true, false
		}
	case common.IsSelectionCancelKey(msg):
		if f.Grabbed {
			f.Grabbed = false
		} else {
			return false, true
		}
	}
	return false, false
}

func (f *ReorderForm) moveCursor(delta int) {
	f.Cursor = common.MoveSelection(f.Cursor, len(f.Items), delta)
}

func (f *ReorderForm) moveItem(delta int) {
	if len(f.Items) <= 1 {
		return
	}
	newPos := f.Cursor + delta
	if newPos < 0 || newPos >= len(f.Items) {
		return
	}
	f.Items[f.Cursor], f.Items[newPos] = f.Items[newPos], f.Items[f.Cursor]
	f.Cursor = newPos
}

func (f *ReorderForm) View(title string) string {
	var b strings.Builder
	b.WriteString(common.FlowTitle.Render(title) + "\n\n")
	for i, item := range f.Items {
		if i == f.Cursor {
			if f.Grabbed {
				b.WriteString(common.SelStyle.Render("▸ "+item.Label+" ◀") + "\n")
			} else {
				b.WriteString(common.SelStyle.Render("> "+item.Label) + "\n")
			}
		} else {
			b.WriteString("  " + item.Label + "\n")
		}
	}
	return b.String()
}

func (f *ReorderForm) FooterHints() []common.OperationHint {
	if f.Grabbed {
		return common.ReorderGrabbedFooterHints()
	}
	return common.ReorderFooterHints()
}
