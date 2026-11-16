package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

func TestHelpGoldens(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("COLUMNS", "80")
	t.Setenv("DOT_COLOR_THEME", "")
	for _, command := range []struct {
		name string
		args []string
	}{{"root", nil}, {"sync-push", []string{"sync", "push"}}} {
		for _, mode := range []struct{ name, color, locale string }{
			{"never", "never", "en_US.UTF-8"},
			{"always", "always", "en_US.UTF-8"},
			{"ascii", "never", "C"},
		} {
			t.Run(command.name+"/"+mode.name, func(t *testing.T) {
				t.Setenv("LC_ALL", mode.locale)
				args := append(append([]string{}, command.args...), "--help", "--color="+mode.color)
				out, stderr, err := runDotForTest(args...)
				if err != nil || stderr != "" {
					t.Fatalf("help: err=%v stderr=%q", err, stderr)
				}
				for _, line := range strings.Split(out, "\n") {
					if width := ansi.StringWidth(line); width > 80 {
						t.Fatalf("help line is %d cells: %q", width, line)
					}
				}
				if strings.ContainsRune(out, 27) != (mode.color == "always") {
					t.Fatalf("help color=%s: %q", mode.color, out)
				}
				var snapshot bytes.Buffer
				encoder := json.NewEncoder(&snapshot)
				encoder.SetEscapeHTML(false)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(strings.SplitAfter(out, "\n")); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join("testdata", "help", command.name+"-"+mode.name+".golden")
				if os.Getenv("UPDATE_HELP_GOLDENS") == "1" {
					if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(target, snapshot.Bytes(), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want, err := os.ReadFile(target)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(snapshot.Bytes(), want) {
					t.Fatalf("help golden differs:\ngot %s\nwant %s", snapshot.String(), want)
				}
			})
		}
	}
}

func TestRootHelpGroupsAccountForEveryCommand(t *testing.T) {
	root := NewRootCmd("dev", "test")
	if len(root.Groups()) != 7 {
		t.Fatalf("groups=%d, want 7", len(root.Groups()))
	}
	for _, cmd := range root.Commands() {
		if !cmd.Hidden && (cmd.GroupID == "" || !root.ContainsGroup(cmd.GroupID)) {
			t.Errorf("command %s has no valid group", cmd.Name())
		}
	}
}

func TestHelpColorASCIIAndPipePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, color, noColor, force string
		ascii, escapes              bool
	}{
		{"pipe", "auto", "", "", false, false},
		{"force", "auto", "", "1", false, true},
		{"no color", "auto", "1", "1", false, false},
		{"explicit always", "always", "1", "", false, true},
		{"explicit ASCII", "never", "", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinColorEnv(t)
			t.Setenv("LC_ALL", "en_US.UTF-8")
			t.Setenv("COLUMNS", "")
			t.Setenv("NO_COLOR", tc.noColor)
			t.Setenv("FORCE_COLOR", tc.force)
			args := []string{"--help", "--color=" + tc.color}
			if tc.ascii {
				args = append(args, "--ascii")
			}
			out, stderr, err := runDotForTest(args...)
			if err != nil || stderr != "" || strings.ContainsRune(out, 27) != tc.escapes {
				t.Fatalf("help policy: err=%v stderr=%q out=%q", err, stderr, out)
			}
			if tc.ascii && (!strings.Contains(out, "> Usage") || strings.Contains(out, ui.CanonicalMarkerGlyph("section"))) {
				t.Fatalf("help ignored ASCII: %q", out)
			}
		})
	}
}

func TestHelpAndUsageShareRendererWithoutChangingUsageErrors(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("LC_ALL", "C")
	t.Setenv("COLUMNS", "80")
	root := NewRootCmd("dev", "test")
	var usage, stderr bytes.Buffer
	root.SetOut(&usage)
	root.SetErr(&stderr)
	if err := root.Usage(); err != nil {
		t.Fatal(err)
	}
	out, helpErr, err := runDotForTest("--help", "--color=never")
	if err != nil || helpErr != "" || stderr.Len() != 0 || usage.String() != out {
		t.Fatalf("usage/help diverge: err=%v usage=%q help=%q", err, usage.String(), out)
	}
	root = NewRootCmd("dev", "test")
	var stdout, diagnostic bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&diagnostic)
	root.SetArgs([]string{"sync", "push", "--unknown-flag"})
	err = root.Execute()
	PrintError(&diagnostic, err)
	if ErrorExitCode(err) != 2 || stdout.Len() != 0 || !strings.Contains(diagnostic.String(), "Usage: dot sync push") || !strings.Contains(diagnostic.String(), "Run 'dot help sync push' for details.") {
		t.Fatalf("usage error contract: err=%v out=%q stderr=%q", err, stdout.String(), diagnostic.String())
	}
}

func TestHelpIncludesOptionalSectionsAndFlagDefaults(t *testing.T) {
	root := NewRootCmd("dev", "test")
	probe := &cobra.Command{Use: "probe", Short: "Probe flags", Example: "dot probe --format json", Aliases: []string{"p"}}
	probe.Flags().String("format", "x y", "Choose `kind`")
	probe.Flags().Lookup("format").NoOptDefVal = "json"
	root.AddCommand(probe)
	root.SetArgs([]string{"probe", "--help", "--color=never"})
	var out bytes.Buffer
	root.SetOut(&out)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Examples", "Aliases", "dot probe --format json", `--format kind[="json"]`, `(default "x y")`} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("help lost %q: %s", text, out.String())
		}
	}
	if probe.Short != "Probe flags" || probe.Example != "dot probe --format json" || probe.Flags().Lookup("format").Usage != "Choose `kind`" {
		t.Fatal("renderer changed command metadata")
	}
}
