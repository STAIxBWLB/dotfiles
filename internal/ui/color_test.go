package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestColorPrecedenceTable(t *testing.T) {
	for _, tc := range []struct {
		name            string
		mode            ColorMode
		env             map[string]string
		tty, json, want bool
	}{
		{name: "always overrides no color and dumb pipe", mode: ColorAlways, env: map[string]string{"NO_COLOR": "1", "TERM": "dumb"}, want: true},
		{name: "never overrides force", mode: ColorNever, env: map[string]string{"FORCE_COLOR": "1"}, tty: true},
		{name: "json overrides always", mode: ColorAlways, json: true},
		{name: "json overrides force", env: map[string]string{"FORCE_COLOR": "1"}, json: true},
		{name: "nonempty no color overrides both forces", env: map[string]string{"NO_COLOR": "false", "FORCE_COLOR": "1", "CLICOLOR_FORCE": "1"}, tty: true},
		{name: "empty no color does not disable", env: map[string]string{"NO_COLOR": "", "TERM": "xterm"}, tty: true, want: true},
		{name: "force on pipe", env: map[string]string{"FORCE_COLOR": "1"}, want: true},
		{name: "force nonzero text", env: map[string]string{"FORCE_COLOR": "false"}, want: true},
		{name: "force overrides clicolor zero", env: map[string]string{"FORCE_COLOR": "2", "CLICOLOR": "0"}, want: true},
		{name: "cli force on dumb pipe", env: map[string]string{"CLICOLOR_FORCE": "1", "TERM": "dumb"}, want: true},
		{name: "cli force before force zero", env: map[string]string{"FORCE_COLOR": "0", "CLICOLOR_FORCE": "1"}, want: true},
		{name: "cli force before clicolor zero", env: map[string]string{"CLICOLOR_FORCE": "2", "CLICOLOR": "0"}, want: true},
		{name: "force zero disables tty", env: map[string]string{"FORCE_COLOR": "0", "TERM": "xterm"}, tty: true},
		{name: "clicolor zero disables tty", env: map[string]string{"CLICOLOR": "0", "TERM": "xterm"}, tty: true},
		{name: "cli force zero is not disable", env: map[string]string{"CLICOLOR_FORCE": "0", "TERM": "xterm"}, tty: true, want: true},
		{name: "clicolor one does not force pipe", env: map[string]string{"CLICOLOR": "1"}},
		{name: "tty", env: map[string]string{"TERM": "xterm"}, tty: true, want: true},
		{name: "empty term tty", tty: true, want: true},
		{name: "dumb tty", env: map[string]string{"TERM": "dumb"}, tty: true},
		{name: "pipe", env: map[string]string{"TERM": "xterm"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.mode
			if mode == "" {
				mode = ColorAuto
			}
			got := ResolveColor(mode, func(k string) string { return tc.env[k] }, tc.tty, tc.json)
			if got != tc.want {
				t.Fatalf("color=%v, want %v", got, tc.want)
			}
		})
	}
}

func clearColorEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"NO_COLOR", "FORCE_COLOR", "CLICOLOR_FORCE", "CLICOLOR", "TERM"} {
		t.Setenv(key, "")
	}
}

func TestColorWriterHonorsPolicyWithoutLipglossRedetection(t *testing.T) {
	clearColorEnv(t)
	styled := StyleSuccess.Render("ready")
	for _, tc := range []struct {
		name       string
		mode       ColorMode
		env        map[string]string
		json, want bool
	}{
		{name: "forced pipe", mode: ColorAlways, want: true},
		{name: "force env pipe", mode: ColorAuto, env: map[string]string{"FORCE_COLOR": "1"}, want: true},
		{name: "no color beats env force", mode: ColorAuto, env: map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}},
		{name: "never beats force", mode: ColorNever, env: map[string]string{"FORCE_COLOR": "1"}},
		{name: "always beats no color", mode: ColorAlways, env: map[string]string{"NO_COLOR": "1"}, want: true},
		{name: "json always", mode: ColorAlways, json: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			var buf bytes.Buffer
			w := NewColorWriter(&buf, tc.mode, tc.json)
			WriteHeader(w, "Report")
			WriteSection(w, "State")
			WriteKV(w, "result", styled)
			got := buf.String()
			if strings.ContainsRune(got, 27) != tc.want {
				t.Fatalf("ESC mismatch: %q", got)
			}
			if !strings.Contains(got, "ready") {
				t.Fatal("lost payload")
			}
		})
	}
}

func TestStreamsResolveIndependently(t *testing.T) {
	env := func(k string) string {
		if k == "TERM" {
			return "xterm"
		}
		return ""
	}
	for _, ttys := range [][2]bool{{true, false}, {false, true}} {
		out := ResolveColor(ColorAuto, env, ttys[0], false)
		errOut := ResolveColor(ColorAuto, env, ttys[1], false)
		if out != ttys[0] || errOut != ttys[1] || out == errOut {
			t.Fatalf("stdout/stderr TTYs %v => %v/%v", ttys, out, errOut)
		}
	}
}

func TestScopedColorRestoresNativeUIAndJSONRebind(t *testing.T) {
	clearColorEnv(t)
	var buf bytes.Buffer
	restore := SetColorPolicy(ColorAlways, false)
	WriteHeader(&buf, "forced")
	restore()
	if !strings.ContainsRune(buf.String(), 27) {
		t.Fatal("scoped native color missing")
	}
	buf.Reset()
	WriteHeader(&buf, "auto pipe")
	if strings.ContainsRune(buf.String(), 27) {
		t.Fatal("color scope leaked")
	}
	bound := NewColorWriter(&buf, ColorAlways, false)
	jsonWriter := NewColorWriter(bound, ColorAlways, true)
	buf.Reset()
	_, err := jsonWriter.Write([]byte(lipgloss.NewStyle().Bold(true).Render("json")))
	if err != nil || strings.ContainsRune(buf.String(), 27) {
		t.Fatalf("JSON rebind: %q, %v", buf.String(), err)
	}
}

func TestColorModeRejectsInvalidWithoutChangingValue(t *testing.T) {
	mode := ColorAuto
	for _, value := range []string{"always", "never", "auto"} {
		if err := mode.Set(value); err != nil || mode.String() != value {
			t.Fatalf("%s: %v", value, err)
		}
	}
	if err := mode.Set("sometimes"); err == nil || mode != ColorAuto {
		t.Fatalf("invalid mode accepted: %s, %v", mode, err)
	}
}
