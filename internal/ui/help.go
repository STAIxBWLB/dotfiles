package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// WriteHanging writes one help item with a display-cell hanging indent. Explicit
// newlines and blank paragraphs are retained; a final newline is added only
// when needed. A pipe remains unwrapped unless COLUMNS selects a width.
func WriteHanging(w io.Writer, prefix, text string) {
	out := Output(w)
	width := OutputWidth(w)
	prefix = strings.ReplaceAll(prefix, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	indent := ansi.StringWidth(prefix)
	if width > 0 && indent > 0 && width-indent < 2 {
		// A long label gets its own line(s). Reserve two cells for a wide text
		// grapheme instead of passing a zero/negative width to the wrapper.
		for _, line := range hangingLines(prefix, width) {
			fmt.Fprintln(out, line)
		}
		if text == "" {
			return
		}
		indent = min(2, max(0, width-2))
		padding := strings.Repeat(" ", indent)
		for _, line := range hangingLines(text, max(1, width-indent)) {
			if line == "" {
				fmt.Fprintln(out)
			} else {
				fmt.Fprintln(out, padding+line)
			}
		}
		return
	}
	limit := 0
	if width > 0 {
		limit = max(1, width-indent)
	}
	padding := strings.Repeat(" ", indent)
	for i, line := range hangingLines(text, limit) {
		switch {
		case i == 0:
			fmt.Fprintln(out, prefix+line)
		case line == "":
			fmt.Fprintln(out)
		default:
			fmt.Fprintln(out, padding+line)
		}
	}
}

func hangingLines(text string, width int) []string {
	if width > 0 {
		text = ansi.Wrap(text, width, "/")
	}
	// A supplied final newline already terminates the last physical line; do
	// not turn it into an extra blank paragraph when Fprintln writes each row.
	return strings.Split(strings.TrimSuffix(text, "\n"), "\n")
}
