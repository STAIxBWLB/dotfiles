package module

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/template"
)

// BUG-20, first site: Config.TemplateData resolved {{.Home}} with
// os.UserHomeDir, so `dot apply --home /tmp/sandbox` rendered the INVOKING
// user's home into files it then wrote inside the sandbox. The assertion has
// to be on the rendered CONTENT: nothing about the write target reveals it,
// because the file lands in the right place carrying the wrong path.
func twoHomeRunContext(t *testing.T, cfg *config.Config) (rc *RunContext, invoking, target string) {
	t.Helper()
	invoking, target = t.TempDir(), t.TempDir()
	t.Setenv("HOME", invoking)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &RunContext{
		Config:   cfg,
		Runner:   dotexec.NewRunner(false, logger),
		Template: template.NewEngine(),
		HomeDir:  target,
	}, invoking, target
}

// The pnpm npmrc is the one template that interpolates {{.Home}} from
// TemplateData. (The two sync unit templates named in BUG-20 read .Home from
// syncer.SchedulerTemplateData, which plan 05-04 already parameterized; see
// internal/syncer/home_flag_test.go.)
func TestTemplatedFiles_RenderTheTargetHome(t *testing.T) {
	rc, invoking, target := twoHomeRunContext(t, &config.Config{})

	if _, err := applyTemplatedFiles(rc, []templatedFile{pnpmNpmrcFile(rc)}); err != nil {
		t.Fatalf("applyTemplatedFiles: %v", err)
	}

	body, err := os.ReadFile(pnpmNpmrcPath(rc))
	if err != nil {
		t.Fatalf("reading the rendered npmrc: %v", err)
	}
	got := string(body)

	// Presence and absence both: a run that rendered neither home would
	// satisfy the absence half on its own.
	if !strings.Contains(got, "store-dir="+target+"/") {
		t.Errorf("the npmrc written into the target home does not point at it:\n%s", got)
	}
	if strings.Contains(got, invoking) {
		t.Errorf("the npmrc written into the target home embeds the invoking user's home %q:\n%s", invoking, got)
	}
}

// The vault half reaches rendered content too: which directory detection
// picks decides what VAULT is exported as. Seeded so the two homes disagree.
func TestTemplateData_VaultPathFollowsTheTargetHome(t *testing.T) {
	cfg := &config.Config{Modules: config.ModulesConfig{
		Workspace: config.WorkConfig{Enabled: true, Path: "~/workspace"},
	}}
	rc, invoking, target := twoHomeRunContext(t, cfg)
	if err := os.MkdirAll(filepath.Join(invoking, "workspace", "work", "vault"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "workspace", "vault"), 0o755); err != nil {
		t.Fatal(err)
	}

	// The same call internal/module/workspace.go makes.
	body, err := rc.Template.Render("shell/40-workspace.sh.tmpl", rc.Config.TemplateData(rc.HomeDir))
	if err != nil {
		t.Fatalf("rendering shell/40-workspace.sh.tmpl: %v", err)
	}
	got := string(body)

	if !strings.Contains(got, `export VAULT="$HOME/workspace/vault"`) {
		t.Errorf("VAULT was resolved against the invoking user's tree:\n%s", got)
	}
	if strings.Contains(got, "/workspace/work/vault") {
		t.Errorf("VAULT names the layout only the invoking user has:\n%s", got)
	}
}
