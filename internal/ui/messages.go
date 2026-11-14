package ui

import (
	"io"
	"strings"

	"github.com/charmbracelet/x/term"
)

// WriteWarning writes a non-fatal diagnostic and any known next steps to w.
// The prefix stays machine-readable on a pipe; only terminals gain a marker.
func WriteWarning(w io.Writer, msg string, next ...string) {
	stream := w
	if bound, ok := stream.(*colorWriter); ok {
		stream = bound.Forward
	}
	file, ok := stream.(term.File)
	writeWarning(w, msg, ok && term.IsTerminal(file.Fd()), next...)
}

func writeWarning(w io.Writer, msg string, tty bool, next ...string) {
	msg = "dot: warning: " + msg
	if tty {
		msg = MarkerColumn(MarkWarn) + " " + msg
	}
	WriteReportLine(w, StyleWarning.Render(msg))
	for _, command := range next {
		if command != "" {
			WriteNext(w, command)
		}
	}
}

// WriteNext writes a complete command or setting in the accent role.
// Callers choose the stream: a warning's next step belongs on stderr.
func WriteNext(w io.Writer, command string) {
	WriteNextIndented(w, 2, command)
}

// WriteNextIndented writes a next step with indent leading spaces. Report rows
// use four spaces below a bullet; WriteNext uses two for a top-level step.
func WriteNextIndented(w io.Writer, indent int, command string) {
	WriteReportLine(w, strings.Repeat(" ", max(0, indent))+StyleAccent.Render(MarkNext+" "+command))
}

// WriteDryRun writes an action that would run, without performing it.
// action omits the "would" prefix so every caller uses the same grammar.
func WriteDryRun(w io.Writer, action string) {
	WriteReportLine(w, "  "+MarkerColumn(RenderMarker(MarkPending))+" would "+action)
}

// WriteNote writes an informational library diagnostic without a level word.
func WriteNote(w io.Writer, msg string) { WriteReportLine(w, "dot: "+msg) }
