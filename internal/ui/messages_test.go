package ui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func messagePresentation(t *testing.T, locale string, ascii bool) func() {
	t.Helper()
	clearColorEnv(t)
	t.Setenv("COLUMNS", "")
	restore, warning := SetPresentation(func(key string) string {
		if key == "LANG" {
			return locale
		}
		return ""
	}, ascii)
	if warning != "" {
		t.Fatal(warning)
	}
	return restore
}

func TestMessageHelperGoldens(t *testing.T) {
	for _, tc := range []struct {
		name, locale string
		ascii, tty   bool
		mode         ColorMode
		golden       string
	}{
		{"unicode pipe off", "en_US.UTF-8", false, false, ColorNever, "messages-unicode-off.golden"},
		{"unicode pipe on", "en_US.UTF-8", false, false, ColorAlways, "messages-unicode-on.golden"},
		{"unicode tty off", "en_US.UTF-8", false, true, ColorNever, "messages-unicode-tty-off.golden"},
		{"unicode tty on", "en_US.UTF-8", false, true, ColorAlways, "messages-unicode-tty-on.golden"},
		{"C locale pipe off", "C", false, false, ColorNever, "messages-ascii-off.golden"},
		{"C locale pipe on", "C", false, false, ColorAlways, "messages-ascii-on.golden"},
		{"C locale tty off", "C", false, true, ColorNever, "messages-ascii-tty-off.golden"},
		{"C locale tty on", "C", false, true, ColorAlways, "messages-ascii-tty-on.golden"},
		{"forced ASCII pipe off", "en_US.UTF-8", true, false, ColorNever, "messages-ascii-off.golden"},
		{"forced ASCII pipe on", "en_US.UTF-8", true, false, ColorAlways, "messages-ascii-on.golden"},
		{"forced ASCII tty off", "en_US.UTF-8", true, true, ColorNever, "messages-ascii-tty-off.golden"},
		{"forced ASCII tty on", "en_US.UTF-8", true, true, ColorAlways, "messages-ascii-tty-on.golden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := messagePresentation(t, tc.locale, tc.ascii)
			defer restore()
			var out bytes.Buffer
			w := NewColorWriter(&out, tc.mode, false)
			if tc.tty {
				writeWarning(w, "cache is unavailable", true, "dot status", "", "DOT_CACHE=local")
			} else {
				WriteWarning(w, "cache is unavailable", "dot status", "", "DOT_CACHE=local")
			}
			WriteNext(w, "dot apply --yes")
			WriteDryRun(w, "write profile")
			WriteNote(w, "using default profile")
			var snapshot bytes.Buffer
			encoder := json.NewEncoder(&snapshot)
			encoder.SetEscapeHTML(false)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(strings.SplitAfter(out.String(), "\n")); err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", tc.golden))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(snapshot.Bytes(), want) {
				t.Fatalf("message golden differs:\ngot %s\nwant %s", snapshot.String(), want)
			}
			if strings.ContainsRune(out.String(), 27) != (tc.mode == ColorAlways) {
				t.Fatalf("unexpected color mode: %q", out.String())
			}
		})
	}
}

func TestWarningMarkerFollowsPrefixOnlyOnTTY(t *testing.T) {
	for _, tc := range []struct{ locale, marker, next string }{
		{"en_US.UTF-8", CanonicalMarkerGlyph("warn"), CanonicalMarkerGlyph("next")}, {"C", "!", "->"},
	} {
		t.Run(tc.locale, func(t *testing.T) {
			restore := messagePresentation(t, tc.locale, false)
			defer restore()
			var pipe, tty bytes.Buffer
			WriteWarning(NewColorWriter(&pipe, ColorNever, false), "needs attention", "dot status")
			writeWarning(NewColorWriter(&tty, ColorNever, false), "needs attention", true, "dot status")
			if got, want := pipe.String(), "dot: warning: needs attention\n  "+tc.next+" dot status\n"; got != want {
				t.Fatalf("pipe warning=%q,want %q", got, want)
			}
			if got, want := tty.String(), "dot: warning: "+tc.marker+"  needs attention\n  "+tc.next+" dot status\n"; got != want {
				t.Fatalf("TTY warning=%q,want %q", got, want)
			}
		})
	}
}

func TestMessageJSONSinkStripsEscapesEvenWhenColorForced(t *testing.T) {
	restore := messagePresentation(t, "en_US.UTF-8", false)
	defer restore()
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	w := NewColorWriter(&out, ColorAlways, true)
	writeWarning(w, StyleError.Render("cache is unavailable"), true, StyleAccent.Render("dot status"))
	WriteDryRun(w, StyleSuccess.Render("write profile"))
	note := strings.Repeat("x", 120)
	WriteNote(w, StyleHint.Render(note))
	want := "dot: warning: ⚠  cache is unavailable\n  → dot status\n  ~  would write profile\ndot: " + note + "\n"
	if got := out.String(); got != want || strings.ContainsRune(got, 27) {
		t.Fatalf("JSON diagnostic sink changed payload or retained ESC: %q", got)
	}
}

func TestMessageHelpersKeepSuppliedStreams(t *testing.T) {
	restore := messagePresentation(t, "en_US.UTF-8", false)
	defer restore()
	var stdout, stderr bytes.Buffer
	out := NewColorWriter(&stdout, ColorNever, false)
	errOut := NewColorWriter(&stderr, ColorNever, false)
	WriteWarning(errOut, "disk is low", "dot clean")
	WriteDryRun(out, "write profile")
	WriteNote(out, "using defaults")
	if got := stderr.String(); got != "dot: warning: disk is low\n  → dot clean\n" {
		t.Fatalf("warning lost supplied stream: %q", got)
	}
	if got := stdout.String(); got != "  ~  would write profile\ndot: using defaults\n" {
		t.Fatalf("action/note lost supplied stream: %q", got)
	}
}
