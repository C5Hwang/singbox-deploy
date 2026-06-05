package ui

import "strings"

const (
	keyMove      = "↑/↓/←/→"
	keyMoveMouse = "↑/↓/Mouse Wheel"
	keyBack      = "Shift+Tab/Ctrl+B"
	keyCancel    = "Esc/Q"
	keyConfirmNo = "Esc/N"
	keyAny       = "Any Key"
	keyAnyOther  = "Any Other Key"
	keyReturn    = "Enter/Esc/Q"
	keyEnter     = "Enter"
	keyEnterYes  = "Enter/Y"
	keySpace     = "Space"
	keyRefresh   = "R"
)

type operationHint struct {
	key    string
	action string
}

func hint(key, action string) operationHint {
	return operationHint{key: key, action: action}
}

func hintText(hints ...operationHint) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		if h.key == "" && h.action == "" {
			continue
		}
		if h.key == "" {
			parts = append(parts, h.action)
			continue
		}
		if h.action == "" {
			parts = append(parts, h.key)
			continue
		}
		parts = append(parts, h.key+": "+h.action)
	}
	return strings.Join(parts, " · ")
}

func hintLine(hints ...operationHint) string {
	return dimStyle.Render(hintText(hints...))
}

func menuFooterHints() []operationHint {
	return []operationHint{hint(keyMove, "Move"), hint(keyEnter, "Select"), hint(keyCancel, "Quit")}
}

func actionFooterHints(action string) []operationHint {
	return []operationHint{hint(keyMove, "Move"), hint(keyEnter, action), hint(keyCancel, "Cancel")}
}

func actionBackFooterHints(action string) []operationHint {
	return []operationHint{hint(keyMove, "Move"), hint(keyEnter, action), hint(keyBack, "Back"), hint(keyCancel, "Cancel")}
}

func formInputFooterHints() []operationHint {
	return []operationHint{hint(keyEnter, "Continue"), hint(keyBack, "Back"), hint("Esc", "Cancel")}
}

func formSingleChoiceFooterHints() []operationHint {
	return []operationHint{hint(keyEnter, "Continue"), hint(keyMove, "Select"), hint(keyBack, "Back"), hint("Esc", "Cancel")}
}

func formMultiChoiceFooterHints() []operationHint {
	return []operationHint{hint(keySpace, "Toggle"), hint(keyEnter, "Continue"), hint(keyMove, "Move"), hint(keyBack, "Back"), hint("Esc", "Cancel")}
}

func applyFooterHints(action string) []operationHint {
	return []operationHint{hint(keyEnterYes, action), hint(keyBack, "Back"), hint(keyConfirmNo, "Cancel")}
}

func runningFooterHints(complete bool) []operationHint {
	if complete {
		return []operationHint{hint(keyEnter, "Show summary"), hint(keyMoveMouse, "Scroll log")}
	}
	return []operationHint{hint(keyMoveMouse, "Scroll log")}
}

func doneFooterHints(runErr bool) []operationHint {
	if runErr {
		return []operationHint{hint(keyMoveMouse, "Scroll log"), hint(keyAnyOther, "Return")}
	}
	return []operationHint{hint(keyAny, "Return")}
}

func returnFooterHints() []operationHint {
	return []operationHint{hint(keyReturn, "Return")}
}
