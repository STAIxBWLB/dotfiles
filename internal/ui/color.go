package ui

import (
	"fmt"
	"io"
	"os"
	"sync/atomic"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
)

// ColorMode is the global --color flag. It also implements pflag.Value so an
// invalid value is rejected during flag parsing, including with --help.
type ColorMode string

const (
	ColorAuto   ColorMode = "auto"
	ColorAlways ColorMode = "always"
	ColorNever  ColorMode = "never"
)

func (m *ColorMode) Set(value string) error {
	switch ColorMode(value) {
	case ColorAuto, ColorAlways, ColorNever:
		*m = ColorMode(value)
		return nil
	default:
		return fmt.Errorf("invalid color mode %q: choose auto, always or never", value)
	}
}
func (m *ColorMode) String() string { return string(*m) }
func (*ColorMode) Type() string     { return "string" }

// ResolveColor implements the shared CLI output standard v1, in table order.
// getenv and tty are inputs so both streams can be tested without a terminal.
// JSON never carries terminal styling, even with an explicit always override.
func ResolveColor(mode ColorMode, getenv func(string) string, tty, json bool) bool {
	if json {
		return false
	}
	if mode == ColorAlways {
		return true
	}
	if mode == ColorNever {
		return false
	}
	if getenv("NO_COLOR") != "" {
		return false
	}
	force, cliForce := getenv("FORCE_COLOR"), getenv("CLICOLOR_FORCE")
	if (force != "" && force != "0") || (cliForce != "" && cliForce != "0") {
		return true
	}
	if force == "0" || getenv("CLICOLOR") == "0" {
		return false
	}
	return tty && getenv("TERM") != "dumb"
}

type colorPolicy struct {
	mode ColorMode
	json bool
}

var currentColor atomic.Pointer[colorPolicy]

// SetColorPolicy scopes native UI output (interactive flows without a cobra
// writer) to the invocation. The caller must defer the returned restoration.
func SetColorPolicy(mode ColorMode, json bool) func() {
	previous := currentColor.Swap(&colorPolicy{mode: mode, json: json})
	return func() { currentColor.Store(previous) }
}

type colorWriter struct{ *colorprofile.Writer }

// Output applies the invocation's color policy to this particular stream.
// Already-bound writers retain their policy rather than being auto-detected
// again by lipgloss (which has a different environment precedence).
func Output(w io.Writer) io.Writer {
	if _, ok := w.(*colorWriter); ok {
		return w
	}
	policy := colorPolicy{mode: ColorAuto}
	if configured := currentColor.Load(); configured != nil {
		policy = *configured
	}
	return NewColorWriter(w, policy.mode, policy.json)
}

// NewColorWriter binds one writer's policy without changing process environment
// or lipgloss globals. Lipgloss Render provides styles; this writer alone
// decides whether the stream can carry them.
func NewColorWriter(w io.Writer, mode ColorMode, json bool) io.Writer {
	if bound, ok := w.(*colorWriter); ok {
		w = bound.Forward
	}
	tty := false
	if file, ok := w.(term.File); ok {
		tty = term.IsTerminal(file.Fd())
	}
	profile := colorprofile.NoTTY
	if ResolveColor(mode, os.Getenv, tty, json) {
		profile = colorprofile.TrueColor
	}
	return &colorWriter{&colorprofile.Writer{Forward: w, Profile: profile}}
}
