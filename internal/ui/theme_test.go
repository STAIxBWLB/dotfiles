package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestDefaultSemanticRolePalette(t *testing.T) {
	cases := []struct {
		role  Role
		color string
		bold  bool
	}{
		{RoleHeader, "4", true}, {RoleSection, "5", true}, {RoleKey, "4", false},
		{RoleValue, "", false}, {RoleSuccess, "2", true}, {RoleWarning, "3", true},
		{RoleError, "1", true}, {RoleHint, "8", false}, {RoleAccent, "6", false},
	}
	if len(cases) != len(roles) {
		t.Fatal("role inventory incomplete")
	}
	for _, tc := range cases {
		t.Run(string(tc.role), func(t *testing.T) {
			style := roleStyle(palettes["ansi"], tc.role)
			var want any = lipgloss.NoColor{}
			if tc.color != "" {
				want = lipgloss.Color(tc.color)
			}
			if !reflect.DeepEqual(style.GetForeground(), want) || style.GetBold() != tc.bold {
				t.Fatalf("role %s color=%v bold=%v", tc.role, style.GetForeground(), style.GetBold())
			}
			rendered := style.Render("role")
			if strings.Contains(rendered, "38;") || strings.Contains(rendered, "48;") {
				t.Fatalf("ANSI role emitted extended color: %q", rendered)
			}
		})
	}
	if StyleKey.GetBold() || reflect.DeepEqual(StyleKey.GetForeground(), StyleSuccess.GetForeground()) {
		t.Fatal("key still shares success style")
	}
}

func relativeLuminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("invalid color %q", hex)
	}
	var channels [3]float64
	for i := range channels {
		n, err := strconv.ParseUint(hex[1+i*2:3+i*2], 16, 8)
		if err != nil {
			t.Fatal(err)
		}
		x := float64(n) / 255
		if x <= 0.04045 {
			channels[i] = x / 12.92
		} else {
			channels[i] = math.Pow((x+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*channels[0] + 0.7152*channels[1] + 0.0722*channels[2]
}

func TestOptionalTokyoNightThemesMeetContrast(t *testing.T) {
	for _, name := range []string{"tokyo-night-dark", "tokyo-night-light"} {
		t.Run(name, func(t *testing.T) {
			p := palettes[name]
			bg := relativeLuminance(t, p.background)
			for _, role := range roles {
				foreground := p.colors[role]
				if role == RoleValue {
					foreground = p.foreground
				}
				fg := relativeLuminance(t, foreground)
				ratio := (max(fg, bg) + 0.05) / (min(fg, bg) + 0.05)
				minimum := 4.5
				if role == RoleHint {
					minimum = 3
				}
				if ratio < minimum {
					t.Errorf("%s contrast %.3f < %.1f on %s", role, ratio, minimum, p.background)
				}
			}
		})
	}
}

func TestThemeIsExplicitAndRestoresOnExit(t *testing.T) {
	before := captureStyles()
	env := func(k string) string {
		if k == "DOT_COLOR_THEME" {
			return "tokyo-night-light"
		}
		if k == "LANG" {
			return "en_US.UTF-8"
		}
		return ""
	}
	restore, err := SetPresentation(env, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(StyleKey.GetForeground(), lipgloss.Color(palettes["tokyo-night-light"].colors[RoleKey])) {
		t.Fatal("explicit theme was not selected")
	}
	restore()
	if !reflect.DeepEqual(captureStyles(), before) {
		t.Fatal("theme escaped invocation")
	}
	_, err = SetPresentation(func(string) string { return "invalid" }, false)
	if err == nil || !strings.Contains(err.Error(), "DOT_COLOR_THEME") {
		t.Fatalf("invalid theme accepted: %v", err)
	}
	if !reflect.DeepEqual(captureStyles(), before) {
		t.Fatal("invalid theme changed styles")
	}
}

func TestSemanticReportGoldens(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mode  ColorMode
		ascii bool
	}{
		{"report-ansi-on.golden", ColorAlways, false},
		{"report-ansi-off.golden", ColorNever, false},
		{"report-ascii-off.golden", ColorNever, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore, err := SetPresentation(func(k string) string {
				if k == "LANG" {
					return "en_US.UTF-8"
				}
				return ""
			}, tc.ascii)
			if err != nil {
				t.Fatal(err)
			}
			defer restore()
			var out bytes.Buffer
			w := NewColorWriter(&out, tc.mode, false)
			WriteHeader(w, "dot Report")
			WriteSection(w, "State")
			WriteKV(w, "Profile", "minimal")
			for _, row := range []struct {
				mark, text string
				style      lipgloss.Style
			}{
				{MarkPresent, "ready", StyleSuccess}, {MarkFail, "failed", StyleError}, {MarkWarn, "warning", StyleWarning},
				{MarkAbsent, "absent", StyleHint}, {MarkUnknown, "unknown", StyleHint}, {MarkPending, "pending", StyleHint},
				{MarkStarred, "active", StyleSuccess}, {MarkNext, "dot status", StyleAccent}, {MarkPartial, "partial", StyleHint}, {MarkPreferred, "preferred", StyleSuccess},
			} {
				fmt.Fprintf(w, "  %s %s\n", MarkerColumn(row.style.Render(row.mark)), row.text)
			}
			// Encode every output line, including trailing spaces and the final
			// empty element, rather than trimming terminal layout from goldens.
			var snapshot bytes.Buffer
			encoder := json.NewEncoder(&snapshot)
			encoder.SetEscapeHTML(false)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(strings.SplitAfter(out.String(), "\n")); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join("testdata", tc.name)
			if os.Getenv("UPDATE_UI_GOLDENS") == "1" {
				if err := os.MkdirAll("testdata", 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, snapshot.Bytes(), 0644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, snapshot.Bytes()) {
				t.Fatalf("report differs: got %q, want %q", snapshot.Bytes(), want)
			}
		})
	}
}

func TestThemesLeaveDefaultForegroundAndBackgroundToTerminal(t *testing.T) {
	for name, p := range palettes {
		if !reflect.DeepEqual(roleStyle(p, RoleValue).GetForeground(), lipgloss.NoColor{}) {
			t.Errorf("%s paints the value role", name)
		}
		for _, role := range roles {
			if !reflect.DeepEqual(roleStyle(p, role).GetBackground(), lipgloss.NoColor{}) {
				t.Errorf("%s/%s paints a background", name, role)
			}
		}
	}
}
