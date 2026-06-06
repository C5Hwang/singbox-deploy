package ui

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type summaryLineKind int

const (
	summaryLineBlank summaryLineKind = iota
	summaryLineText
	summaryLineRow
)

type summaryLine struct {
	kind   summaryLineKind
	indent int
	label  string
	value  string
	text   string
}

func summaryRow(label, value string) summaryLine {
	return summaryLine{kind: summaryLineRow, label: label, value: value}
}

func summaryIndentedRow(indent int, label, value string) summaryLine {
	return summaryLine{kind: summaryLineRow, indent: indent, label: label, value: value}
}

func summaryText(text string) summaryLine {
	return summaryLine{kind: summaryLineText, text: text}
}

func summaryBlank() summaryLine {
	return summaryLine{kind: summaryLineBlank}
}

func renderSummary(rows []summaryLine) string {
	labelWidth := summaryLabelWidth(rows)
	var b strings.Builder
	for _, row := range rows {
		switch row.kind {
		case summaryLineRow:
			b.WriteString(renderSummaryLabel(row, labelWidth) + " " + highlightSummaryText(row.value))
		case summaryLineText:
			b.WriteString(strings.Repeat(" ", row.indent) + highlightSummaryText(row.text))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}

func summaryLabelWidth(rows []summaryLine) int {
	width := 0
	for _, row := range rows {
		if row.kind != summaryLineRow {
			continue
		}
		width = max(width, row.indent+len(row.label)+1)
	}
	return width
}

func renderSummaryLabel(row summaryLine, width int) string {
	label := strings.Repeat(" ", row.indent) + row.label + ":"
	if pad := width - len(label); pad > 0 {
		label += strings.Repeat(" ", pad)
	}
	return dimStyle.Render(label)
}

type summaryHighlightRule struct {
	pattern *regexp.Regexp
	style   lipgloss.Style
	keep    func(string) bool
}

type summaryHighlightSpan struct {
	start int
	end   int
	style lipgloss.Style
}

var summaryHighlightRules = []summaryHighlightRule{
	{pattern: regexp.MustCompile(`(?i)\b(?:not running|not valid yet|failed|failure|error|expired|invalid|inactive|stopped|dead)\b`), style: statusBad},
	{pattern: regexp.MustCompile(`(?i)\brandom\b`), style: flowRandom},
	{pattern: regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(?:[ T]\d{2}:\d{2}(?::\d{2})?)?\b`), style: summaryDate},
	{pattern: regexp.MustCompile(`(?i)\breset day \d{1,2}(?: hour \d{1,2} GMT)?\b`), style: summaryDate},
	{pattern: regexp.MustCompile(`\bv?\d+(?:\.\d+){1,3}(?:[-+][0-9A-Za-z][0-9A-Za-z.-]*)?\b`), style: summaryInfo, keep: isLikelyVersion},
	{pattern: regexp.MustCompile(`(?i)\b(?:running|active|ok|healthy|valid|complete|refreshed|enabled|ready|yes)\b`), style: statusOK},
	{pattern: regexp.MustCompile(`(?i)(?:\bunknown\b|\bnot set\b|\bnone\b|\bnot installed\b|\bdisabled\b|\bgenerate/default\b|\bgenerate/keep current\b)`), style: statusWarn},
}

func highlightSummaryText(text string) string {
	spans := summaryHighlightSpans(text)
	if len(spans) == 0 {
		return text
	}
	var b strings.Builder
	last := 0
	for _, span := range spans {
		if span.start < last {
			continue
		}
		b.WriteString(text[last:span.start])
		b.WriteString(span.style.Render(text[span.start:span.end]))
		last = span.end
	}
	b.WriteString(text[last:])
	return b.String()
}

func summaryHighlightSpans(text string) []summaryHighlightSpan {
	var spans []summaryHighlightSpan
	for _, rule := range summaryHighlightRules {
		for _, loc := range rule.pattern.FindAllStringIndex(text, -1) {
			match := text[loc[0]:loc[1]]
			if rule.keep != nil && !rule.keep(match) {
				continue
			}
			spans = append(spans, summaryHighlightSpan{start: loc[0], end: loc[1], style: rule.style})
		}
	}
	sort.SliceStable(spans, func(i, j int) bool {
		if spans[i].start == spans[j].start {
			return spans[i].end-spans[i].start > spans[j].end-spans[j].start
		}
		return spans[i].start < spans[j].start
	})
	return spans
}

func isLikelyVersion(match string) bool {
	raw := strings.ToLower(match)
	hasVPrefix := strings.HasPrefix(raw, "v")
	core := strings.TrimPrefix(raw, "v")
	if idx := strings.IndexAny(core, "-+"); idx >= 0 {
		core = core[:idx]
	}
	parts := strings.Split(core, ".")
	if hasVPrefix || len(parts) != 4 {
		return true
	}
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return true
		}
	}
	return false
}
