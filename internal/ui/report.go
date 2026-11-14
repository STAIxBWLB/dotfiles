package ui

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// OutputWidth uses the actual terminal width; a pipe wraps only when COLUMNS
// explicitly requests it. It is independent of whether color is enabled.
func OutputWidth(w io.Writer) int {
	if bound, ok := w.(*colorWriter); ok {
		if bound.json {
			return 0
		}
		w = bound.Forward
	}
	if file, ok := w.(term.File); ok && term.IsTerminal(file.Fd()) {
		if width, _, err := term.GetSize(file.Fd()); err == nil && width > 0 {
			return width
		}
	}
	width, _ := strconv.Atoi(os.Getenv("COLUMNS"))
	if width > 0 {
		return width
	}
	return 0
}

// WriteReportLine wraps owned human text, never raw child payloads or JSON.
// ANSI-aware wrapping preserves styles and Unicode grapheme boundaries.
func WriteReportLine(w io.Writer, text string) {
	if width := OutputWidth(w); width > 0 {
		text = ansi.Wrap(text, width, "/")
	}
	_, _ = fmt.Fprintln(Output(w), text)
}

// CheckRow has fixed identifier/state columns and a flexible finding column.
type CheckRow struct{ Marker, ID, State, Detail string }

func middleCell(text string, limit int) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\n", " "), "\r", " ")
	width := lipgloss.Width(text)
	if width <= limit {
		return text
	}
	if limit <= 3 {
		return ansi.Truncate(text, limit, "")
	}
	left := (limit - 3) / 2
	right := limit - 3 - left
	return ansi.Cut(text, 0, left) + "..." + ansi.Cut(text, width-right, width)
}
func padCell(text string, width int) string {
	return text + strings.Repeat(" ", max(0, width-lipgloss.Width(text)))
}

// WriteCheckRows caps non-flexible columns even on a pipe. Long identifiers
// have an explicit ellipsis; verbose emits their full value in the finding.
func WriteCheckRows(w io.Writer, rows []CheckRow, verbose bool) {
	if len(rows) == 0 {
		return
	}
	rows = append([]CheckRow(nil), rows...)
	priority := func(marker string) int {
		switch marker {
		case MarkFail:
			return 0
		case MarkWarn:
			return 1
		case MarkUnknown:
			return 2
		default:
			return 3
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return priority(rows[i].Marker) < priority(rows[j].Marker) })
	idWidth, stateWidth := 5, 5
	for _, row := range rows {
		idWidth = max(idWidth, min(32, lipgloss.Width(row.ID)))
		stateWidth = max(stateWidth, min(32, lipgloss.Width(row.State)))
	}
	width := OutputWidth(w)
	if width > 0 {
		idWidth = min(idWidth, max(1, width-stateWidth-19))
	}
	prefixWidth := 9 + idWidth + stateWidth
	header := "     " + padCell("CHECK", idWidth) + "  " + padCell("STATE", stateWidth) + "  FINDING"
	WriteReportLine(w, StyleKey.Width(0).Render(header))
	for _, row := range rows {
		id, state := middleCell(row.ID, idWidth), middleCell(row.State, stateWidth)
		detail := row.Detail
		if verbose && id != row.ID {
			detail += "\nidentifier: " + row.ID
		}
		if width > 0 {
			detail = ansi.Wrap(detail, max(1, width-prefixWidth), "/")
		}
		lines := strings.Split(detail, "\n")
		prefix := "  " + MarkerColumn(RenderMarker(row.Marker)) + " " + padCell(StyleAccent.Render(id), idWidth) + "  " + padCell(state, stateWidth) + "  "
		_, _ = fmt.Fprintln(Output(w), prefix+lines[0])
		for _, line := range lines[1:] {
			_, _ = fmt.Fprintln(Output(w), strings.Repeat(" ", prefixWidth)+line)
		}
	}
}
