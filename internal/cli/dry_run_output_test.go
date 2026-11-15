package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/appsettings"
	"github.com/STAIxBWLB/dotfiles/internal/noindex"
	"github.com/STAIxBWLB/dotfiles/internal/secrets"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
	"github.com/STAIxBWLB/dotfiles/internal/tunnel"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// Render engine events only: these snapshots never invoke package managers,
// launchd, OAuth, a tunnel or a secrets writer.
func TestTask3PreviewGoldens(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("COLUMNS", "")
	for _, tc := range []struct {
		name, locale string
		mode         ui.ColorMode
	}{
		{"off", "en_US.UTF-8", ui.ColorNever},
		{"on", "en_US.UTF-8", ui.ColorAlways},
		{"ascii", "C", ui.ColorNever},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore, warning := ui.SetPresentation(func(key string) string {
				if key == "LANG" {
					return tc.locale
				}
				return ""
			}, false)
			if warning != "" {
				t.Fatal(warning)
			}
			defer restore()
			var out, errOut bytes.Buffer
			p := &Printer{Out: ui.NewColorWriter(&out, tc.mode, false), Err: ui.NewColorWriter(&errOut, tc.mode, false)}
			apps := renderAppsEvent(p)
			apps(appsettings.Event{Kind: appsettings.EventDryRunTap, Tokens: []string{"vendor/tools"}})
			apps(appsettings.Event{Kind: appsettings.EventDryRunInstall, Tokens: []string{"terminal-app"}})
			renderSecretsEvent(p)(secrets.Event{Kind: secrets.EventScaffoldPreview, Path: "/fixture/secret file"})
			printPeerHooks(p, []syncer.HookResult{
				{Phase: "on_activate", Action: "app-open Terminal", DryRun: true, Detail: "would open Terminal"},
				{Phase: "on_activate", Action: "app-open Terminal", DryRun: true, Detail: "skipped: needs macOS"},
				{Phase: "on_deactivate", Action: "launchd-bootout com.example.agent", DryRun: true, Detail: "would: 1 job disabled, 1 loaded job booted out"},
			})
			printPeerScheduleDryRun(p, &syncer.PeerScheduleResult{Plist: "/fixture/peer.plist", DryRun: true})
			printPeerScheduleDryRun(p, &syncer.PeerScheduleResult{Plist: "/fixture/peer.plist", DryRun: true, Off: true})
			for _, event := range []tunnel.Event{
				{Kind: tunnel.EventCloudflaredWouldInstall},
				{Kind: tunnel.EventCertWouldLogin, Path: "/fixture/cloudflared"},
				{Kind: tunnel.EventRemoteLoginWouldEnable},
			} {
				renderTunnelEvent(p, "fixture", "fixture.example", event)
			}
			if errOut.Len() != 0 {
				t.Fatalf("preview rows leaked to stderr: %q", errOut.String())
			}
			if strings.Contains(out.String(), "dry-run:") || strings.Contains(out.String(), "[dry-run]") {
				t.Fatalf("legacy preview prefix survived: %q", out.String())
			}
			var snapshot bytes.Buffer
			encoder := json.NewEncoder(&snapshot)
			encoder.SetEscapeHTML(false)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(strings.SplitAfter(out.String(), "\n")); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join("testdata", "task3-previews-"+tc.name+".golden")
			if os.Getenv("UPDATE_CLI_PREVIEW_GOLDENS") == "1" {
				if err := os.WriteFile(target, snapshot.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(snapshot.Bytes(), want) {
				t.Fatalf("preview output differs:\ngot %s\nwant %s", snapshot.String(), want)
			}
		})
	}
}

func TestNoindexDryRunReportsActionWithoutWritingMarker(t *testing.T) {
	home, root := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("COLUMNS", "")
	cache := filepath.Join(root, "node_modules")
	if err := os.Mkdir(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runDotForTest("--home", home, "--color=never", "--dry-run", "noindex", root)
	if err != nil || stderr != "" || !strings.Contains(out, "would mark 1 directories") {
		t.Fatalf("noindex preview: out=%q stderr=%q err=%v", out, stderr, err)
	}
	if _, err := os.Stat(filepath.Join(cache, noindex.Marker)); !os.IsNotExist(err) {
		t.Fatalf("preview wrote a marker: %v", err)
	}
}
