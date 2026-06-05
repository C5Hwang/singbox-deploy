package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	uiparams "github.com/C5Hwang/singbox-deploy/internal/ui/parameters"
)

const (
	// bubbles/textinput truncates two-character placeholders to the first
	// character when Width is unset, so keep a real fallback before sizing.
	defaultParameterInputWidth = 80
	minParameterInputWidth     = 4
)

// field describes one parameter collected by the shared parameter form.
type field struct {
	key       string
	label     string
	def       string
	note      string
	options   []string
	multi     bool
	skip      func(vals map[string]string) bool
	noteFunc  func(vals map[string]string) string
	badgeFunc func(vals map[string]string) string
}

func fieldFromParameter(f uiparams.Field) field {
	return field{
		key:       f.Key,
		label:     f.Label,
		def:       f.Def,
		note:      f.Note,
		options:   append([]string(nil), f.Options...),
		multi:     f.Multi,
		skip:      f.Skip,
		noteFunc:  f.NoteFunc,
		badgeFunc: f.BadgeFunc,
	}
}

func fieldsFromParameters(params []uiparams.Field) []field {
	fields := make([]field, 0, len(params))
	for _, f := range params {
		fields = append(fields, fieldFromParameter(f))
	}
	return fields
}

func parameterFromField(f field) uiparams.Field {
	return uiparams.Field{
		Key:       f.key,
		Label:     f.label,
		Def:       f.def,
		Note:      f.note,
		Options:   append([]string(nil), f.options...),
		Multi:     f.multi,
		Skip:      f.skip,
		NoteFunc:  f.noteFunc,
		BadgeFunc: f.badgeFunc,
	}
}

type parameterForm struct {
	width          int
	height         int
	fields         []field
	fieldIx        int
	values         map[string]string
	input          textinput.Model
	optionIx       int
	optionSelected map[string]bool
	fieldErr       string
	validate       func(field, string, map[string]string) error
}

func newParameterForm(fields []field) parameterForm {
	return parameterForm{
		fields:  fields,
		fieldIx: -1,
		values:  map[string]string{},
		input:   newParameterInput(),
	}
}

func newParameterInput() textinput.Model {
	ti := textinput.New()
	ti.CharLimit = 256
	ti.Prompt = "› "
	ti.Width = defaultParameterInputWidth
	return ti
}

func (f *parameterForm) setSize(width, height int) {
	f.width = width
	f.height = height
	f.input.Width = parameterInputWidth(width)
}

func parameterInputWidth(width int) int {
	if width <= 0 {
		return defaultParameterInputWidth
	}
	return max(minParameterInputWidth, width-4)
}

func (f *parameterForm) setFields(fields []field) {
	f.fields = fields
	f.fieldIx = -1
	f.values = map[string]string{}
	f.optionIx = 0
	f.optionSelected = nil
	f.fieldErr = ""
}

func (f *parameterForm) setField(index int) {
	f.ensureValues()
	field := f.fields[index]
	f.fieldIx = index
	f.fieldErr = ""
	if len(field.options) > 0 {
		value := f.values[field.key]
		if value == "" {
			value = field.def
		}
		if field.multi {
			f.optionSelected = selectedOptions(value)
			f.optionIx = 0
			f.input.Blur()
			return
		}
		f.optionSelected = nil
		f.optionIx = optionIndex(field.options, value)
		f.input.Blur()
		return
	}
	f.optionSelected = nil
	f.input.SetValue(f.values[field.key])
	f.input.Placeholder = field.def
	f.input.Focus()
}

func (f *parameterForm) startForm() {
	f.fieldIx = -1
	f.advanceField()
}

func (f *parameterForm) advanceField() bool {
	f.ensureValues()
	for i := f.fieldIx + 1; i < len(f.fields); i++ {
		field := f.fields[i]
		if field.skip != nil && field.skip(f.values) {
			continue
		}
		f.setField(i)
		return false
	}
	return true
}

func (f *parameterForm) previousField() bool {
	if f.fieldIx <= 0 {
		return false
	}
	f.saveFieldDraft()
	for i := f.fieldIx - 1; i >= 0; i-- {
		field := f.fields[i]
		if field.skip != nil && field.skip(f.values) {
			continue
		}
		f.setField(i)
		return true
	}
	return false
}

func (f *parameterForm) backToLastField() {
	f.ensureValues()
	for i := len(f.fields) - 1; i >= 0; i-- {
		field := f.fields[i]
		if field.skip != nil && field.skip(f.values) {
			continue
		}
		f.setField(i)
		return
	}
}

func (f *parameterForm) saveFieldDraft() {
	f.ensureValues()
	if f.fieldIx < 0 || f.fieldIx >= len(f.fields) {
		return
	}
	field := f.fields[f.fieldIx]
	f.values[field.key] = f.fieldValue(field)
}

func (f *parameterForm) commitField() bool {
	f.ensureValues()
	field := f.fields[f.fieldIx]
	val := f.fieldValue(field)
	if f.validate != nil {
		if err := f.validate(field, val, f.values); err != nil {
			f.fieldErr = err.Error()
			return false
		}
	}
	f.fieldErr = ""
	f.values[field.key] = val
	return f.advanceField()
}

func (f *parameterForm) fieldValue(field field) string {
	if len(field.options) > 0 {
		if field.multi {
			return selectedOptionsValue(field.options, f.optionSelected)
		}
		return field.options[min(max(0, f.optionIx), len(field.options)-1)]
	}
	val := strings.TrimSpace(f.input.Value())
	if val == "" {
		return field.def
	}
	return val
}

func (f *parameterForm) updateInput(msg tea.Msg) tea.Cmd {
	f.fieldErr = ""
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *parameterForm) currentFieldHasOptions() bool {
	if f.fieldIx < 0 || f.fieldIx >= len(f.fields) {
		return false
	}
	return len(f.fields[f.fieldIx].options) > 0
}

func (f *parameterForm) currentFieldIsMulti() bool {
	if f.fieldIx < 0 || f.fieldIx >= len(f.fields) {
		return false
	}
	return f.fields[f.fieldIx].multi
}

func (f *parameterForm) moveOption(delta int) {
	if !f.currentFieldHasOptions() {
		return
	}
	options := f.fields[f.fieldIx].options
	f.optionIx = (f.optionIx + delta + len(options)) % len(options)
	f.fieldErr = ""
}

func (f *parameterForm) toggleOption() {
	if !f.currentFieldIsMulti() {
		return
	}
	options := f.fields[f.fieldIx].options
	if len(options) == 0 {
		return
	}
	if f.optionSelected == nil {
		f.optionSelected = map[string]bool{}
	}
	opt := options[min(max(0, f.optionIx), len(options)-1)]
	if f.optionSelected[opt] {
		delete(f.optionSelected, opt)
	} else {
		f.optionSelected[opt] = true
	}
	f.fieldErr = ""
}

func (f *parameterForm) View(title string) string {
	if f.fieldIx < 0 || f.fieldIx >= len(f.fields) {
		return ""
	}
	field := f.fields[f.fieldIx]
	var b strings.Builder
	b.WriteString(flowTitle.Render(title) + "\n\n")
	b.WriteString(field.label + "\n")
	if badge := f.fieldBadge(field); badge != "" {
		b.WriteString(flowOK.Render(badge) + "\n")
	}
	if note := f.fieldNote(field); note != "" {
		for _, line := range wrapFieldNote(note, f.width) {
			b.WriteString(dimStyle.Render(line) + "\n")
		}
	}
	if field.def != "" {
		b.WriteString(dimStyle.Render("default: "+field.def) + "\n")
	}
	if f.fieldErr != "" {
		b.WriteString(flowErr.Render(f.fieldErr) + "\n")
	}
	if len(field.options) > 0 {
		b.WriteString(f.optionsView(field) + "\n\n")
		if field.multi {
			b.WriteString(dimStyle.Render("space toggle · enter to continue · ↑/↓ or ←/→ move · shift+tab/ctrl+b back · esc to cancel"))
			return b.String()
		}
		b.WriteString(dimStyle.Render("enter to continue · ↑/↓ or ←/→ select · shift+tab/ctrl+b back · esc to cancel"))
		return b.String()
	}
	b.WriteString(f.input.View() + "\n\n")
	b.WriteString(dimStyle.Render("enter to continue · shift+tab/ctrl+b back · esc to cancel"))
	return b.String()
}

func (f *parameterForm) fieldNote(field field) string {
	if field.noteFunc != nil {
		return field.noteFunc(f.values)
	}
	return field.note
}

func (f *parameterForm) fieldBadge(field field) string {
	if field.badgeFunc == nil {
		return ""
	}
	return field.badgeFunc(f.values)
}

func (f *parameterForm) optionsView(field field) string {
	var rows []string
	for i, opt := range field.options {
		label := opt
		if field.multi {
			mark := "[ ]"
			if f.optionSelected[opt] {
				mark = "[x]"
			}
			label = mark + " " + opt
		}
		row := "  " + label
		if i == f.optionIx {
			row = selStyle.Render("> " + label)
		}
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (f *parameterForm) ensureValues() {
	if f.values == nil {
		f.values = map[string]string{}
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
