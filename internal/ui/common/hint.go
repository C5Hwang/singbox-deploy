package common

import "strings"

const (
	KeyMove      = "↑/↓/←/→"
	KeyMoveMouse = "↑/↓/Mouse Wheel"
	KeyBack      = "Shift+Tab/Ctrl+B"
	KeyCancel    = "Esc/Q"
	KeyConfirmNo = "Esc/N"
	KeyAny       = "Any Key"
	KeyAnyOther  = "Any Other Key"
	KeyReturn    = "Enter/Esc/Q"
	KeyEnter     = "Enter"
	KeyEnterYes  = "Enter/Y"
	KeySpace     = "Space"
	KeyRefresh   = "R"
)

type OperationHint struct {
	Key    string
	Action string
}

func Hint(key, action string) OperationHint {
	return OperationHint{Key: key, Action: action}
}

func HintText(hints ...OperationHint) string {
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		if h.Key == "" && h.Action == "" {
			continue
		}
		if h.Key == "" {
			parts = append(parts, h.Action)
			continue
		}
		if h.Action == "" {
			parts = append(parts, h.Key)
			continue
		}
		parts = append(parts, h.Key+": "+h.Action)
	}
	return strings.Join(parts, " · ")
}

func HintLine(hints ...OperationHint) string {
	return DimStyle.Render(HintText(hints...))
}

func MenuFooterHints() []OperationHint {
	return []OperationHint{Hint(KeyMove, "Move"), Hint(KeyEnter, "Select"), Hint(KeyCancel, "Quit")}
}

func ActionFooterHints(action string) []OperationHint {
	return []OperationHint{Hint(KeyMove, "Move"), Hint(KeyEnter, action), Hint(KeyCancel, "Cancel")}
}

func ActionBackFooterHints(action string) []OperationHint {
	return []OperationHint{Hint(KeyMove, "Move"), Hint(KeyEnter, action), Hint(KeyBack, "Back"), Hint(KeyCancel, "Cancel")}
}

func FormInputFooterHints() []OperationHint {
	return []OperationHint{Hint(KeyEnter, "Continue"), Hint(KeyBack, "Back"), Hint("Esc", "Cancel")}
}

func FormSingleChoiceFooterHints() []OperationHint {
	return []OperationHint{Hint(KeyEnter, "Continue"), Hint(KeyMove, "Select"), Hint(KeyBack, "Back"), Hint("Esc", "Cancel")}
}

func FormMultiChoiceFooterHints() []OperationHint {
	return []OperationHint{Hint(KeySpace, "Toggle"), Hint(KeyEnter, "Continue"), Hint(KeyMove, "Move"), Hint(KeyBack, "Back"), Hint("Esc", "Cancel")}
}

func ApplyFooterHints(action string) []OperationHint {
	return []OperationHint{Hint(KeyEnterYes, action), Hint(KeyBack, "Back"), Hint(KeyConfirmNo, "Cancel")}
}

func RunningFooterHints(complete bool) []OperationHint {
	if complete {
		return []OperationHint{Hint(KeyEnter, "Show summary"), Hint(KeyMoveMouse, "Scroll log")}
	}
	return []OperationHint{Hint(KeyMoveMouse, "Scroll log")}
}

func DoneFooterHints(runErr bool) []OperationHint {
	if runErr {
		return []OperationHint{Hint(KeyMoveMouse, "Scroll log"), Hint(KeyAnyOther, "Return")}
	}
	return []OperationHint{Hint(KeyAny, "Return")}
}

func ReturnFooterHints() []OperationHint {
	return []OperationHint{Hint(KeyReturn, "Return")}
}

func ReorderFooterHints() []OperationHint {
	return []OperationHint{Hint(KeyMove, "Navigate"), Hint(KeySpace, "Grab"), Hint(KeyEnter, "Confirm"), Hint(KeyCancel, "Cancel")}
}

func ReorderGrabbedFooterHints() []OperationHint {
	return []OperationHint{Hint(KeyMove, "Move item"), Hint(KeySpace, "Release"), Hint(KeyEnter, "Release"), Hint(KeyCancel, "Release")}
}
