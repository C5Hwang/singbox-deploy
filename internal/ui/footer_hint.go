package ui

import "github.com/C5Hwang/singbox-deploy/internal/ui/common"

const (
	keyMove      = common.KeyMove
	keyMoveMouse = common.KeyMoveMouse
	keyBack      = common.KeyBack
	keyCancel    = common.KeyCancel
	keyConfirmNo = common.KeyConfirmNo
	keyAny       = common.KeyAny
	keyAnyOther  = common.KeyAnyOther
	keyReturn    = common.KeyReturn
	keyEnter     = common.KeyEnter
	keyEnterYes  = common.KeyEnterYes
	keySpace     = common.KeySpace
	keyRefresh   = common.KeyRefresh
)

type operationHint = common.OperationHint

func hint(key, action string) operationHint { return common.Hint(key, action) }

func hintText(hints ...operationHint) string { return common.HintText(hints...) }

func hintLine(hints ...operationHint) string { return common.HintLine(hints...) }

func menuFooterHints() []operationHint                { return common.MenuFooterHints() }
func actionFooterHints(action string) []operationHint { return common.ActionFooterHints(action) }
func actionBackFooterHints(action string) []operationHint {
	return common.ActionBackFooterHints(action)
}
func formInputFooterHints() []operationHint            { return common.FormInputFooterHints() }
func formSingleChoiceFooterHints() []operationHint     { return common.FormSingleChoiceFooterHints() }
func formMultiChoiceFooterHints() []operationHint      { return common.FormMultiChoiceFooterHints() }
func applyFooterHints(action string) []operationHint   { return common.ApplyFooterHints(action) }
func runningFooterHints(complete bool) []operationHint { return common.RunningFooterHints(complete) }
func doneFooterHints(runErr bool) []operationHint      { return common.DoneFooterHints(runErr) }
func returnFooterHints() []operationHint               { return common.ReturnFooterHints() }
func reorderFooterHints() []operationHint              { return common.ReorderFooterHints() }
func reorderGrabbedFooterHints() []operationHint       { return common.ReorderGrabbedFooterHints() }
