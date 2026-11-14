package ui

import (
	"fmt"
	"io"

	"charm.land/lipgloss/v2"
)

const kvKeyWidth = 22 // width of "Pull+intake scheduler:"

// WriteHeader writes a styled report title to w with one leading blank line.
// The title is rendered with a single space of horizontal padding so the
// header role frames the text without painting a background.
func WriteHeader(w io.Writer, title string) {
	_, _ = fmt.Fprintln(Output(w))
	WriteReportLine(w, StyleHeader.Render(" "+title+" "))
}

// WriteSection writes a section divider with one leading blank line and the
// canonical "▸ " prefix. Callers pass the section name only.
func WriteSection(w io.Writer, title string) {
	_, _ = fmt.Fprintln(Output(w))
	WriteReportLine(w, StyleSection.Render(MarkSection+" "+title))
}

// WriteKV writes a styled key/value row at the 2-space top-level indent.
// An empty value renders as "(unset)" in the hint style.
func WriteKV(w io.Writer, key, value string) {
	if value == "" {
		value = StyleHint.Render("(unset)")
	} else {
		value = StyleValue.Render(value)
	}
	label := key + ":"
	width := kvKeyWidth
	if labelWidth := lipgloss.Width(label); labelWidth > width {
		width = labelWidth
	}
	if lipgloss.Width(label) > 28 {
		WriteReportLine(w, "  "+StyleKey.Width(0).Render(label))
		WriteReportLine(w, "    "+value)
		return
	}
	WriteReportLine(w, fmt.Sprintf("  %s  %s", StyleKey.Width(width).Render(label), value))
}
