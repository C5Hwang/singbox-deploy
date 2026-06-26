package form

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/C5Hwang/singbox-deploy/internal/ui/common"
	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

const (
	// bubbles/textinput truncates two-character placeholders to the first
	// character when Width is unset, so keep a real fallback before sizing.
	defaultParameterInputWidth = 80
	minParameterInputWidth     = 4
)

// Field describes one parameter collected by the shared parameter form.
type Field struct {
	Key       string
	Label     string
	Def       string
	Note      string
	Options   []string
	Multi     bool
	Skip      func(vals map[string]string) bool
	NoteFunc  func(vals map[string]string) string
	BadgeFunc func(vals map[string]string) string
}

func FieldFromParameter(f uiparams.Field) Field {
	return Field{
		Key:       f.Key,
		Label:     f.Label,
		Def:       f.Def,
		Note:      f.Note,
		Options:   append([]string(nil), f.Options...),
		Multi:     f.Multi,
		Skip:      f.Skip,
		NoteFunc:  f.NoteFunc,
		BadgeFunc: f.BadgeFunc,
	}
}

func FieldsFromParameters(params []uiparams.Field) []Field {
	fields := make([]Field, 0, len(params))
	for _, f := range params {
		fields = append(fields, FieldFromParameter(f))
	}
	return fields
}

func ParameterFromField(f Field) uiparams.Field {
	return uiparams.Field{
		Key:       f.Key,
		Label:     f.Label,
		Def:       f.Def,
		Note:      f.Note,
		Options:   append([]string(nil), f.Options...),
		Multi:     f.Multi,
		Skip:      f.Skip,
		NoteFunc:  f.NoteFunc,
		BadgeFunc: f.BadgeFunc,
	}
}

type ParameterForm struct {
	Width          int
	Height         int
	Fields         []Field
	FieldIx        int
	Values         map[string]string
	Input          textinput.Model
	OptionIx       int
	OptionSelected map[string]bool
	FieldErr       string
	Validate       func(Field, string, map[string]string) error
}

func NewParameterForm(fields []Field) ParameterForm {
	return ParameterForm{
		Fields:  fields,
		FieldIx: -1,
		Values:  map[string]string{},
		Input:   newParameterInput(),
	}
}

func newParameterInput() textinput.Model {
	ti := textinput.New()
	ti.CharLimit = 256
	ti.Prompt = "› "
	ti.Width = defaultParameterInputWidth
	return ti
}

func (f *ParameterForm) SetSize(width, height int) {
	f.Width = width
	f.Height = height
	f.Input.Width = parameterInputWidth(width)
}

func parameterInputWidth(width int) int {
	if width <= 0 {
		return defaultParameterInputWidth
	}
	return max(minParameterInputWidth, width-4)
}

func (f *ParameterForm) SetFields(fields []Field) {
	f.Fields = fields
	f.FieldIx = -1
	f.Values = map[string]string{}
	f.OptionIx = 0
	f.OptionSelected = nil
	f.FieldErr = ""
}

func (f *ParameterForm) SetField(index int) {
	f.ensureValues()
	field := f.Fields[index]
	f.FieldIx = index
	f.FieldErr = ""
	if len(field.Options) > 0 {
		value := f.Values[field.Key]
		if value == "" {
			value = field.Def
		}
		if field.Multi {
			f.OptionSelected = selectedOptions(value)
			f.OptionIx = 0
			f.Input.Blur()
			return
		}
		f.OptionSelected = nil
		f.OptionIx = optionIndex(field.Options, value)
		f.Input.Blur()
		return
	}
	f.OptionSelected = nil
	f.Input.SetValue(f.Values[field.Key])
	f.Input.Placeholder = field.Def
	f.Input.Focus()
}

func (f *ParameterForm) StartForm() {
	f.FieldIx = -1
	f.AdvanceField()
}

func (f *ParameterForm) AdvanceField() bool {
	f.ensureValues()
	for i := f.FieldIx + 1; i < len(f.Fields); i++ {
		field := f.Fields[i]
		if field.Skip != nil && field.Skip(f.Values) {
			continue
		}
		f.SetField(i)
		return false
	}
	return true
}

func (f *ParameterForm) PreviousField() bool {
	if f.FieldIx <= 0 {
		return false
	}
	f.SaveFieldDraft()
	for i := f.FieldIx - 1; i >= 0; i-- {
		field := f.Fields[i]
		if field.Skip != nil && field.Skip(f.Values) {
			continue
		}
		f.SetField(i)
		return true
	}
	return false
}

func (f *ParameterForm) BackToLastField() {
	f.ensureValues()
	for i := len(f.Fields) - 1; i >= 0; i-- {
		field := f.Fields[i]
		if field.Skip != nil && field.Skip(f.Values) {
			continue
		}
		f.SetField(i)
		return
	}
}

// BackToFieldKey moves the cursor to the named field, or to the first
// non-skipped field if no such key exists. Used when an external transition
// (e.g. cancelled sub-form) needs to return focus to a specific field.
func (f *ParameterForm) BackToFieldKey(key string) {
	f.ensureValues()
	for i, field := range f.Fields {
		if field.Key != key {
			continue
		}
		if field.Skip != nil && field.Skip(f.Values) {
			continue
		}
		f.SetField(i)
		return
	}
	f.StartForm()
}

func (f *ParameterForm) SaveFieldDraft() {
	f.ensureValues()
	if f.FieldIx < 0 || f.FieldIx >= len(f.Fields) {
		return
	}
	field := f.Fields[f.FieldIx]
	f.Values[field.Key] = f.FieldValue(field)
}

func (f *ParameterForm) CommitField() bool {
	f.ensureValues()
	field := f.Fields[f.FieldIx]
	val := f.FieldValue(field)
	if f.Validate != nil {
		if err := f.Validate(field, val, f.Values); err != nil {
			f.FieldErr = err.Error()
			return false
		}
	}
	f.FieldErr = ""
	f.Values[field.Key] = val
	return f.AdvanceField()
}

func (f *ParameterForm) FieldValue(field Field) string {
	if len(field.Options) > 0 {
		if field.Multi {
			return selectedOptionsValue(field.Options, f.OptionSelected)
		}
		return field.Options[min(max(0, f.OptionIx), len(field.Options)-1)]
	}
	val := strings.TrimSpace(f.Input.Value())
	if val == "" {
		return field.Def
	}
	return val
}

func (f *ParameterForm) UpdateInput(msg tea.Msg) tea.Cmd {
	// Only clear FieldErr on real key presses.
	if _, ok := msg.(tea.KeyMsg); ok {
		f.FieldErr = ""
	}
	var cmd tea.Cmd
	f.Input, cmd = f.Input.Update(msg)
	return cmd
}

// CurrentFieldKey returns the key of the field the cursor is on, or "" if no
// field is active (e.g. all fields have been committed).
func (f *ParameterForm) CurrentFieldKey() string {
	if f.FieldIx < 0 || f.FieldIx >= len(f.Fields) {
		return ""
	}
	return f.Fields[f.FieldIx].Key
}

func (f *ParameterForm) CurrentFieldHasOptions() bool {
	if f.FieldIx < 0 || f.FieldIx >= len(f.Fields) {
		return false
	}
	return len(f.Fields[f.FieldIx].Options) > 0
}

func (f *ParameterForm) CurrentFieldIsMulti() bool {
	if f.FieldIx < 0 || f.FieldIx >= len(f.Fields) {
		return false
	}
	return f.Fields[f.FieldIx].Multi
}

func (f *ParameterForm) MoveOption(delta int) {
	if !f.CurrentFieldHasOptions() {
		return
	}
	options := f.Fields[f.FieldIx].Options
	f.OptionIx = common.MoveSelection(f.OptionIx, len(options), delta)
	f.FieldErr = ""
}

func (f *ParameterForm) ToggleOption() {
	if !f.CurrentFieldIsMulti() {
		return
	}
	options := f.Fields[f.FieldIx].Options
	if len(options) == 0 {
		return
	}
	if f.OptionSelected == nil {
		f.OptionSelected = map[string]bool{}
	}
	if common.ToggleStringSelection(f.OptionSelected, options, f.OptionIx) {
		f.FieldErr = ""
	}
}

type ParameterFormKeyHandlers struct {
	Complete func()
	Back     func()
	Cancel   func() (tea.Cmd, bool)
}

func (f *ParameterForm) HandleKey(msg tea.KeyMsg, h ParameterFormKeyHandlers) (tea.Cmd, bool, bool) {
	if common.IsSelectionConfirmKey(msg) {
		if f.CommitField() && h.Complete != nil {
			h.Complete()
		}
		return nil, false, true
	}
	if common.IsSelectionToggleKey(msg) {
		if f.CurrentFieldHasOptions() {
			if f.CurrentFieldIsMulti() {
				f.ToggleOption()
			}
			return nil, false, true
		}
		return f.UpdateInput(msg), false, true
	}
	if common.IsSelectionPreviousKey(msg) {
		if f.CurrentFieldHasOptions() {
			f.MoveOption(-1)
			return nil, false, true
		}
		return f.UpdateInput(msg), false, true
	}
	if common.IsSelectionNextKey(msg) {
		if f.CurrentFieldHasOptions() {
			f.MoveOption(1)
			return nil, false, true
		}
		return f.UpdateInput(msg), false, true
	}
	if common.IsSelectionBackKey(msg) {
		if h.Back != nil {
			h.Back()
		} else {
			f.PreviousField()
		}
		return nil, false, true
	}
	if msg.String() == "esc" {
		if h.Cancel != nil {
			cmd, done := h.Cancel()
			return cmd, done, true
		}
		return nil, true, true
	}
	if f.CurrentFieldHasOptions() {
		return nil, false, true
	}
	return f.UpdateInput(msg), false, true
}

func (f *ParameterForm) View(title string) string {
	if f.FieldIx < 0 || f.FieldIx >= len(f.Fields) {
		return ""
	}
	field := f.Fields[f.FieldIx]
	var b strings.Builder
	b.WriteString(common.FlowTitle.Render(title) + "\n\n")
	b.WriteString(field.Label + "\n")
	if badge := f.fieldBadge(field); badge != "" {
		b.WriteString(common.FlowOK.Render(badge) + "\n")
	}
	if note := f.fieldNote(field); note != "" {
		for _, line := range wrapFieldNote(note, f.Width) {
			b.WriteString(common.DimStyle.Render(line) + "\n")
		}
	}
	if field.Def != "" {
		b.WriteString(common.DimStyle.Render("default: "+field.Def) + "\n")
	}
	if f.FieldErr != "" {
		b.WriteString(common.FlowErr.Render(f.FieldErr) + "\n")
	}
	if len(field.Options) > 0 {
		b.WriteString(f.optionsView(field))
		return b.String()
	}
	b.WriteString(f.Input.View())
	return b.String()
}

func (f *ParameterForm) FooterHints() []common.OperationHint {
	if f.FieldIx < 0 || f.FieldIx >= len(f.Fields) {
		return nil
	}
	if f.CurrentFieldHasOptions() {
		if f.CurrentFieldIsMulti() {
			return common.FormMultiChoiceFooterHints()
		}
		return common.FormSingleChoiceFooterHints()
	}
	return common.FormInputFooterHints()
}

func (f *ParameterForm) fieldNote(field Field) string {
	if field.NoteFunc != nil {
		return field.NoteFunc(f.Values)
	}
	return field.Note
}

func (f *ParameterForm) fieldBadge(field Field) string {
	if field.BadgeFunc == nil {
		return ""
	}
	return field.BadgeFunc(f.Values)
}

func (f *ParameterForm) optionsView(field Field) string {
	var rows []string
	for i, opt := range field.Options {
		label := opt
		if field.Multi {
			mark := "[ ]"
			if f.OptionSelected[opt] {
				mark = "[x]"
			}
			label = mark + " " + opt
		}
		row := "  " + label
		if i == f.OptionIx {
			row = common.SelStyle.Render("> " + label)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (f *ParameterForm) ensureValues() {
	if f.Values == nil {
		f.Values = map[string]string{}
	}
}

func optionIndex(options []string, value string) int {
	for i, opt := range options {
		if opt == value {
			return i
		}
	}
	return 0
}

func selectedOptions(value string) map[string]bool {
	selected := map[string]bool{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			selected[part] = true
		}
	}
	return selected
}

func selectedOptionsValue(options []string, selected map[string]bool) string {
	values := make([]string, 0, len(options))
	for _, opt := range options {
		if selected[opt] {
			values = append(values, opt)
		}
	}
	return strings.Join(values, ",")
}

func wrapFieldNote(s string, width int) []string {
	if width <= 0 {
		width = 80
	}
	wrapWidth := max(24, width-4)
	var lines []string
	for _, part := range strings.Split(s, "\n") {
		lines = append(lines, wrapWords(part, wrapWidth)...)
	}
	return lines
}

func wrapWords(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	if width <= 0 {
		return []string{s}
	}
	var lines []string
	line := words[0]
	for _, word := range words[1:] {
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}
