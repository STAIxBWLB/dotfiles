package cli

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/spf13/cobra"
)

func TestCommandLocaleAndASCIIMarkers(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("DOT_COLOR_THEME", "")
	t.Setenv("LC_CTYPE", "")
	for _, tc := range []struct {
		name, locale      string
		force             bool
		ok, next, section string
	}{
		{"UTF8", "en_US.UTF-8", false, ui.CanonicalMarkerGlyph("ok"), ui.CanonicalMarkerGlyph("next"), ui.CanonicalMarkerGlyph("section")},
		{"LANG C", "C", false, "ok", "->", ">"},
		{"explicit ASCII", "en_US.UTF-8", true, "ok", "->", ">"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LC_ALL", "")
			t.Setenv("LANG", tc.locale)
			root := NewRootCmd("dev", "test")
			probe := &cobra.Command{Use: "marker-report", RunE: func(c *cobra.Command, _ []string) error {
				p := printerFrom(c)
				p.Section("State")
				p.Bullet(ui.StyleSuccess.Render(ui.MarkPresent), "ready")
				p.Bullet(ui.StyleAccent.Render(ui.MarkNext), "dot status")
				return nil
			}}
			bindPresentation(probe)
			root.AddCommand(probe)
			var out bytes.Buffer
			root.SetOut(&out)
			args := []string{"marker-report", "--color=never"}
			if tc.force {
				args = append(args, "--ascii")
			}
			root.SetArgs(args)
			if err := root.Execute(); err != nil {
				t.Fatal(err)
			}
			want := fmt.Sprintf("\n%s State\n  %s ready\n  %s dot status\n", tc.section, ui.MarkerColumn(tc.ok), ui.MarkerColumn(tc.next))
			if out.String() != want {
				t.Fatalf("locale report = %q, want %q", out.String(), want)
			}
			// A real registered report also resolves its section marker at entry.
			flags := []string{"usecase", "--color=never"}
			if tc.force {
				flags = append(flags, "--ascii")
			}
			report, _, err := runDotForTest(flags...)
			if err != nil || !strings.Contains(report, tc.section+" Global flags") {
				t.Fatalf("real report locale failed: %v", err)
			}
			if (tc.force || tc.locale == "C") && strings.ContainsAny(report, "✓✗⚠▸★◆") {
				t.Fatal("cached report text bypassed ASCII markers")
			}
		})
	}
}

func TestThemeChoiceIsExplicitOnCLI(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("LC_ALL", "en_US.UTF-8")
	for _, name := range []string{"", "ansi", "tokyo-night-dark", "tokyo-night-light"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("DOT_COLOR_THEME", name)
			out, _, err := runDotForTest("usecase", "--color=always")
			if err != nil {
				t.Fatal(err)
			}
			trueColor := strings.Contains(out, "38;2;")
			want := strings.HasPrefix(name, "tokyo-night-")
			if trueColor != want {
				t.Fatalf("theme %q truecolor=%v,want %v", name, trueColor, want)
			}
		})
	}
}

func TestPresentationRestoresThemeAndMarkersOnError(t *testing.T) {
	t.Setenv("DOT_COLOR_THEME", "tokyo-night-dark")
	t.Setenv("LC_ALL", "C")
	beforeColor, beforeMark := ui.StyleKey.GetForeground(), ui.MarkPresent
	c := &cobra.Command{Use: "probe"}
	err := withPresentation(c, func() error {
		if ui.MarkPresent != "ok" {
			t.Fatal("C locale missing inside handler")
		}
		return errors.New("fixture failure")
	})
	if err == nil || ui.MarkPresent != beforeMark || !reflect.DeepEqual(ui.StyleKey.GetForeground(), beforeColor) {
		t.Fatal("presentation leaked after failed handler")
	}
}

func TestFailureAndUnknownMarkersAreDistinct(t *testing.T) {
	absent, _ := appsInstallMarker(true, false)
	unknown, _ := appsInstallMarker(false, false)
	failed, _ := skillStatusMarker(aisettings.SkillStatusInvalid)
	if absent != ui.MarkAbsent || unknown != ui.MarkUnknown || failed != ui.MarkFail || absent == unknown || absent == failed || unknown == failed {
		t.Fatalf("absent=%q unknown=%q failed=%q", absent, unknown, failed)
	}
}

// Owned CLI presentation may use semantic styles, never define colors or raw
// terminal styling itself. Test fixtures are excluded from this source guard.
func TestNoRawCLIColorsOutsideUI(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		tree, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(tree, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Color" {
					if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "lipgloss" {
						t.Errorf("%s defines a raw color outside UI", name)
					}
				}
			}
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				value, err := strconv.Unquote(lit.Value)
				if err == nil && strings.ContainsRune(value, 27) {
					t.Errorf("%s embeds raw terminal styling outside UI", name)
				}
			}
			return true
		})
	}
}

func TestApplyUsesASCIIMarkersUnderCLocale(t *testing.T) {
	pinColorEnv(t)
	_, home := newStatusHomeFixture(t)
	pinNoTerminal(t)
	t.Setenv("LC_ALL", "C")
	t.Setenv("DOT_COLOR_THEME", "")
	out, _, err := runDotForTest("--home", home, "apply", "--module", "git", "--dry-run", "--color=never")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out, "  -> ") {
		t.Fatalf("missing ASCII plan marker: %s", out)
	}
	if strings.ContainsAny(out, "✓✗⚠▸★◆") {
		t.Fatalf("module progress bypassed ASCII policy: %s", out)
	}
}

func TestInvalidThemeDoesNotHideHelp(t *testing.T) {
	t.Setenv("DOT_COLOR_THEME", "invalid")
	out, stderr, err := runDotForTest("--help")
	if err != nil || !strings.Contains(out, "Usage:") || !strings.Contains(stderr, "DOT_COLOR_THEME") {
		t.Fatalf("help hidden by theme error: %q, %q, %v", out, stderr, err)
	}
}

func TestInvalidThemeWarnsAndRunsRunAndRunECommands(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("DOT_COLOR_THEME", "tokyo")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	for _, tc := range []struct {
		name    string
		args    []string
		payload string
	}{
		{"Run", []string{"version", "--color=always"}, "dot dev (test)"},
		{"RunE", []string{"usecase", "--color=always"}, "dot Use Cases"},
		{"RunE help", []string{"peer", "--color=always"}, "Usage:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runDotForTest(tc.args...)
			if err != nil || !strings.Contains(out, tc.payload) {
				t.Fatalf("cosmetic theme blocked command: out=%q err=%v", out, err)
			}
			if strings.Count(stderr, "dot: warning:") != 1 {
				t.Fatalf("expected one warning: %q", stderr)
			}
			for _, want := range []string{`"tokyo"`, "using ansi", "tokyo-night-dark", "tokyo-night-light"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("warning lacks %q: %s", want, stderr)
				}
			}
			if strings.Contains(out, "38;2;") {
				t.Fatal("fallback still used a truecolor theme")
			}
		})
	}
}
